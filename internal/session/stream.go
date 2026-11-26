package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/phaselock"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

const (
	captureWait  = 100 * time.Millisecond
	reopenDelay  = 500 * time.Millisecond
	ptsRingSize  = 256
	encodeWindow = 600
)

// stream runs one client session: video, audio and their stats.
type stream struct {
	h       *Host
	sess    *transport.Session
	params  VideoParams
	profile *defaults.ResolvedProfile
	path    ratectl.Path
	addr    string

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	// inputDone is set under h.injMu once the stream's input is torn down.
	inputDone bool

	invalid chan invalidGap
	// keyNeeded asks the video loop for a keyframe: an invalidation that
	// did not fit in invalid.
	keyNeeded atomic.Bool
	// epoch counts the streams a resize switched to; every frame carries
	// it. Only the video loop touches it.
	epoch uint8
	// resize holds the client window's latest size, in pixels, for the
	// video loop.
	resize chan resizeReq
	// display is the client display a Resize said the window moved to;
	// nil keeps the one offered at the start. Only the video loop
	// touches it.
	display *defaults.ClientDisplay
	// switchTo holds the host display the client asked for last, for the
	// video loop; pick is the one the stream moved to, "" while the
	// profile chose. Only the video loop touches pick.
	switchTo chan string
	pick     string
	pts      [ptsRingSize]struct {
		id  uint32
		pts int64
		// dropped: the sender dropped this frame; invalidated: the encoder
		// no longer predicts from it.
		dropped, invalidated bool
	}

	mu        sync.Mutex
	info      VideoInfo
	vdName    string
	encodeMs  *window
	sent      rate
	parityPct float64
	// captureW and encodeW split encodeMs at the moment the picture was
	// acquired, for HostStats.
	captureW, encodeW *window

	frames, keyframes, dropped, audioPkts atomic.Uint64

	// mic plays the client's microphone, nil when the host takes none.
	mic       *micReceiver
	micDevice string
	micHint   string
	bg        sync.WaitGroup

	trace *tracer[hostTraceRow]

	// phase steers the video loop's frames by the client's display phase;
	// nil until a pipe opens. phaseLog is the state last logged.
	phase    atomic.Pointer[phaselock.Controller]
	phaseMu  sync.Mutex
	phaseLog phaselock.State

	// endedByHost is set when this machine ended the stream: a stop, a
	// kick, a revoke, or another client taking over.
	endedByHost atomic.Bool
	// unmute puts the speakers back while MuteSpeakers holds them; ended
	// keeps them unmuted once the stream is over.
	muteMu sync.Mutex
	unmute func() error
	ended  bool
	// restoreDisplays turns the other displays back on in privacy mode;
	// only the video loop touches it.
	restoreDisplays func() error
}

func newStream(h *Host, sess *transport.Session) *stream {
	acc := sess.Config()
	p := VideoParams{
		Display: h.cfg.Display, Adapter: h.cfg.Adapter, Synth: h.cfg.Synth,
		Codec: codecFromWire(acc.Codec), Chroma: h.chroma(acc),
		Width: int(acc.Width), Height: int(acc.Height), FPS: int(acc.FPS),
		BitrateKbps: sess.TargetBitrateKbps(), Slices: h.cfg.Slices, SubFrame: acc.SubFrame,
	}
	if p.BitrateKbps <= 0 {
		p.BitrateKbps = int(acc.BitrateKbps)
	}
	st := &stream{h: h, sess: sess, params: p, done: make(chan struct{}), invalid: make(chan invalidGap, 16), resize: make(chan resizeReq, 1), switchTo: make(chan string, 1),
		encodeMs: newWindow(encodeWindow), captureW: newWindow(encodeWindow), encodeW: newWindow(encodeWindow)}
	if ua, ok := sess.RemoteControlAddr().(*net.UDPAddr); ok {
		st.addr = ua.String()
		st.path = ratectl.ClassifyAddr(ua.AddrPort().Addr())
	}
	st.startMic()
	b := sess.SocketBuffers()
	h.logf("planck: media socket buffers %d KiB receive, %d KiB send", b.Recv>>10, b.Send>>10)
	prof, err := ParseProfile(sess.Profile())
	if err != nil {
		h.logf("planck: %v; using the legacy config", err)
	}
	if prof != nil {
		st.profile = prof
		st.params = paramsFromProfile(st.params, prof, sess.ShardSize())
		var cc clientCaps
		if json.Unmarshal(sess.PeerCapabilities(), &cc) == nil {
			st.params.ScalePct = scalePercent(cc.Client.Display.Scale)
		}
		if prof.Route == defaults.RouteLAN {
			sess.SetPacing(lanPacing(prof.LinkMbps, cc.Client.OS))
		}
		h.logf("planck: profile %s %dx%d@%d %d-bit 4:4:4=%v on %s (%s), virtual display %v, %d/%d kbps, slices %d split %v",
			prof.Codec, prof.Width, prof.Height, prof.FPS, prof.BitDepth, prof.Chroma444, prof.EncodeAdapter.Name, prof.EncoderAPI,
			prof.VirtualDisplay, prof.BitrateKbps, prof.MaxBitrateKbps, prof.Slices, prof.Split)
		return st
	}
	st.params.Split = h.cfg.Split
	if st.params.Split == encode.SplitAuto && st.path != ratectl.PathLAN {
		st.params.Split = encode.SplitOff
	}
	return st
}

// VirtualDisplay is a session's virtual display, which the pipeline
// captures.
type VirtualDisplay interface {
	// Name is the display capture opens: a GDI name on Windows.
	Name() string
	// Resize gives the display a new size; on an error it is gone.
	Resize(w, h int) error
	Close() error
}

// openVirtualDisplay makes the session's virtual display and points the
// pipeline at it. Without one the stream falls back to the physical
// display on its own GPU.
func (st *stream) openVirtualDisplay() (VirtualDisplay, error) {
	fp := st.sess.PeerFingerprint()
	open := st.h.cfg.OpenVirtualDisplay
	if open == nil {
		open = st.h.vdd.create
	}
	vd, err := open(st.params, fp[:], st.h.cfg.VDDPrimary)
	if err != nil {
		st.params.VirtualDisplay, st.params.Adapter = false, ""
		return nil, err
	}
	st.useVirtualDisplay(vd)
	st.h.logf("planck: virtual display %s %dx%d@%d on %s", vd.Name(), st.params.Width, st.params.Height, st.params.FPS, st.params.Adapter)
	st.isolate(vd)
	return vd, nil
}

func (st *stream) useVirtualDisplay(vd VirtualDisplay) {
	if s, ok := vd.(interface{ Scale() (int, error) }); ok {
		if pct, err := s.Scale(); err != nil || pct != st.params.ScalePct {
			st.h.logf("planck: virtual display %s at %d%% scale, not %d%%: %v", vd.Name(), pct, st.params.ScalePct, err)
		}
	}
	st.params.Display = vd.Name()
	st.mu.Lock()
	st.vdName = vd.Name()
	st.mu.Unlock()
}

func (st *stream) closeVirtualDisplay(vd VirtualDisplay) {
	name := vd.Name()
	st.unisolate()
	err := vd.Close()
	st.mu.Lock()
	st.vdName = ""
	st.mu.Unlock()
	st.h.logf("planck: virtual display %s removed (err %v)", name, err)
}

// resizeReq is a client window's size in pixels and, when the client
// sent it, the display the window is on.
type resizeReq struct {
	w, h    int
	display *defaults.ClientDisplay
}

// requestResize leaves the client window's size for the video loop,
// replacing one it has not taken yet but keeping that one's display when
// r has none.
func (st *stream) requestResize(r resizeReq) {
	for {
		select {
		case st.resize <- r:
			return
		default:
		}
		select {
		case old := <-st.resize:
			if r.display == nil {
				r.display = old.display
			}
		default:
		}
	}
}

// resizeTo moves a stream on a virtual display to the profile resolved
// for a w x h client window. A client that follows stream switches gets
// the whole profile resolved anew, which may change the codec (PyroWave
// on a small window, HEVC on a large one); an older client keeps its
// codec and only takes the new size. Either way the pipeline closes, the
// display takes the new size (or is made anew on another GPU or rate),
// the client hears of it, and the loop opens the pipeline again with a
// keyframe. A display that fails to resize is gone; the loop makes a new
// one. A window moved to another client display resolves for that
// display: its refresh caps the rate, and its scale the virtual display's.
// It reports whether the stream changed.
func (st *stream) resizeTo(r resizeReq, pipe *VideoPipe, vd *VirtualDisplay) bool {
	w, h := r.w, r.h
	if r.display != nil {
		st.display = r.display
	}
	switches := st.sess.PeerFeatures()&wire.FeatureStreamSwitch != 0
	if st.profile == nil || !st.params.VirtualDisplay || (st.params.Codec == encode.PyroWave && !switches) {
		return false
	}
	cc, ok := st.peerCaps()
	if !ok {
		return false
	}
	cc.Client.Display.ViewWidthPx, cc.Client.Display.ViewHeightPx = w, h
	p := st.reresolve(cc)
	if !p.VirtualDisplay {
		return false
	}
	if !switches {
		return st.resizeLegacy(w, h, &p, pipe, vd)
	}
	next := paramsFromProfile(st.params, &p, st.sess.ShardSize())
	next.ScalePct = scalePercent(cc.Client.Display.Scale)
	if sameStream(st.profile, &p) && next.ScalePct == st.params.ScalePct {
		return false
	}
	from := st.restream(&p, next, pipe, vd)
	st.h.logf("planck: stream %d moved from %s %dx%d to %s %dx%d@%d for a %dx%d window", st.epoch, from.Codec, from.Width, from.Height,
		p.Codec, p.Width, p.Height, p.FPS, w, h)
	return true
}

// peerCaps are the capabilities the client offered, with what the session
// learned since: the client display its window moved to and the host
// display it picked.
func (st *stream) peerCaps() (clientCaps, bool) {
	var cc clientCaps
	if err := json.Unmarshal(st.sess.PeerCapabilities(), &cc); err != nil || cc.V != clientCapsVersion {
		return cc, false
	}
	if st.display != nil {
		cc.Client.Display = *st.display
	}
	if st.pick != "" {
		cc.Overrides.Display = st.pick
	}
	cc.Route.Kind = st.profile.Route
	return cc, true
}

// reresolve settles the stream anew for cc, on the session's route.
func (st *stream) reresolve(cc clientCaps) defaults.ResolvedProfile {
	var remote netip.Addr
	if ua, ok := st.sess.RemoteControlAddr().(*net.UDPAddr); ok {
		remote = ua.AddrPort().Addr()
	}
	return st.h.resolveProfile(cc, remote, st.path, st.sess.MaxDatagram() > wire.MaxDatagram)
}

// restream moves the session to p, piped as next, without leaving it: the
// pipeline closes, the virtual display takes the new size, is made anew,
// or goes when p captures a physical display, and the client hears of
// the new stream, whose first frame the loop forces to a keyframe. It
// returns the profile streamed before.
func (st *stream) restream(p *defaults.ResolvedProfile, next VideoParams, pipe *VideoPipe, vd *VirtualDisplay) *defaults.ResolvedProfile {
	if *pipe != nil {
		(*pipe).Close()
		*pipe = nil
	}
	from := st.profile
	if *vd != nil {
		switch {
		case !next.VirtualDisplay || next.Adapter != st.params.Adapter || next.FPS != st.params.FPS || next.ScalePct != st.params.ScalePct:
			st.closeVirtualDisplay(*vd)
			*vd = nil
		case next.Width != st.params.Width || next.Height != st.params.Height:
			if err := (*vd).Resize(next.Width, next.Height); err != nil {
				st.h.fail("virtual display resize to %dx%d: %v", next.Width, next.Height, err)
				st.closeVirtualDisplay(*vd)
				*vd = nil
			}
		}
	}
	st.params = next
	if *vd != nil {
		st.useVirtualDisplay(*vd)
	}
	st.epoch++
	start := 0
	if p.Codec != from.Codec || p.Codec == defaults.CodecPyroWave {
		start = p.BitrateKbps
	}
	st.sess.SetStream(st.h.profileConfig(p, st.sess.Config()), start, p.MaxBitrateKbps, transport.ParityProfile{
		MinPercent: p.FEC.MinParityPercent, KeyframePercent: p.FEC.KeyframeParityPercent, CriticalPercent: p.FEC.CriticalParityPercent})
	st.mu.Lock()
	st.profile = p
	st.mu.Unlock()
	if js, err := json.Marshal(p); err == nil {
		_ = st.sess.SendControl(&wire.Stream{Epoch: st.epoch, Profile: js})
	}
	return from
}

// encoderWatcher is a VideoPipe that opens its encoder on the first
// picture (sourcePipe), and tells fn of each one before it encodes.
type encoderWatcher interface {
	watchEncoder(fn func(encode.Config))
}

// encodeGate is a VideoPipe that runs fn after it captured a picture and
// before it encodes it (sourcePipe); fn true makes the picture a keyframe.
type encodeGate interface {
	gateEncode(fn func() bool)
}

// hdrCaptured8Bit is why a stream the profile settled as HDR10 is SDR.
const hdrCaptured8Bit = "the host captured its HDR desktop 8-bit"

// followEncoder gives the client the stream the encoder settled when it
// differs from the profile on HDR10: an HDR desktop that capture delivers
// 8-bit encodes SDR, and the client's sink must show it as SDR, not PQ. It
// runs before the encoder's first picture, so every frame of the new epoch
// is the encoder's. It reports whether the stream changed.
func (st *stream) followEncoder(hdr10 bool) bool {
	if st.profile == nil || st.profile.HDR10 == hdr10 {
		return false
	}
	if st.sess.PeerFeatures()&wire.FeatureStreamSwitch == 0 {
		st.h.logf("planck: the encoder settled HDR10 %v, but the client cannot follow a stream switch", hdr10)
		return false
	}
	p := *st.profile
	p.HDR10 = hdr10
	p.Decisions = slices.Clone(p.Decisions)
	for i := range p.Decisions {
		if d := &p.Decisions[i]; d.Knob == "hdr10" {
			d.Value, d.Why = "off", hdrCaptured8Bit
			if hdr10 {
				d.Value, d.Why = "on", "HDR desktop and HDR client display"
			}
		}
	}
	st.epoch++
	st.mu.Lock()
	st.profile = &p
	st.mu.Unlock()
	if js, err := json.Marshal(&p); err == nil {
		_ = st.sess.SendControl(&wire.Stream{Epoch: st.epoch, Profile: js})
	}
	st.h.logf("planck: stream %d is HDR10 %v, as the encoder settled", st.epoch, hdr10)
	return true
}

// requestDisplay leaves the host display the client asked for to the
// video loop, replacing one it has not taken yet.
func (st *stream) requestDisplay(id string) {
	for {
		select {
		case st.switchTo <- id:
			return
		default:
		}
		select {
		case <-st.switchTo:
		default:
		}
	}
}

// switchDisplayTo moves the stream to the host display id, physical
// capture at that display's size and refresh, in the same session. It
// reports whether the stream changed: not for a display the host does not
// have, one it pins another over, or the one streaming now.
func (st *stream) switchDisplayTo(id string, pipe *VideoPipe, vd *VirtualDisplay) bool {
	if st.profile == nil || st.sess.PeerFeatures()&wire.FeatureStreamSwitch == 0 {
		st.h.logf("planck: display %s asked for by a client that cannot follow a stream switch", id)
		return false
	}
	if !st.params.VirtualDisplay && st.params.Display == id {
		return false
	}
	cc, ok := st.peerCaps()
	if !ok {
		return false
	}
	cc.Overrides.Display = id
	p := st.reresolve(cc)
	if p.VirtualDisplay || p.CaptureDisplay != id {
		st.h.logf("planck: display %s not streamed: %s", id, decisionWhy(&p, "display"))
		return false
	}
	st.pick = id
	next := paramsFromProfile(st.params, &p, st.sess.ShardSize())
	from := st.restream(&p, next, pipe, vd)
	st.h.logf("planck: stream %d moved from %s %dx%d to display %s, %s %dx%d@%d", st.epoch, from.Codec, from.Width, from.Height,
		id, p.Codec, p.Width, p.Height, p.FPS)
	return true
}

// decisionWhy is why p settled knob as it did.
func decisionWhy(p *defaults.ResolvedProfile, knob string) string {
	for _, d := range p.Decisions {
		if d.Knob == knob {
			return d.Why
		}
	}
	return "no decision"
}

// sameStream reports whether b would stream exactly as a does.
func sameStream(a, b *defaults.ResolvedProfile) bool {
	return a.Codec == b.Codec && a.Chroma444 == b.Chroma444 && a.BitDepth == b.BitDepth && a.Width == b.Width && a.Height == b.Height &&
		a.FPS == b.FPS && a.EncodeAdapter.LUID == b.EncodeAdapter.LUID && a.Slices == b.Slices && a.SubFrame == b.SubFrame && a.Split == b.Split
}

// resizeLegacy gives a client that cannot follow a codec change the new
// size only, in a Profile.
func (st *stream) resizeLegacy(w, h int, p *defaults.ResolvedProfile, pipe *VideoPipe, vd *VirtualDisplay) bool {
	if p.Width == st.params.Width && p.Height == st.params.Height {
		return false
	}
	if *pipe != nil {
		(*pipe).Close()
		*pipe = nil
	}
	from := fmt.Sprintf("%dx%d", st.params.Width, st.params.Height)
	st.params.Width, st.params.Height = p.Width, p.Height
	if *vd != nil {
		if err := (*vd).Resize(p.Width, p.Height); err != nil {
			st.h.fail("virtual display resize to %dx%d: %v", p.Width, p.Height, err)
			st.closeVirtualDisplay(*vd)
			*vd = nil
		} else {
			st.useVirtualDisplay(*vd)
		}
	}
	prof := *st.profile
	prof.Width, prof.Height = p.Width, p.Height
	isRes := func(d defaults.Decision) bool { return d.Knob == "resolution" }
	if i, j := slices.IndexFunc(prof.Decisions, isRes), slices.IndexFunc(p.Decisions, isRes); i >= 0 && j >= 0 {
		prof.Decisions = slices.Clone(prof.Decisions)
		prof.Decisions[i] = p.Decisions[j]
	}
	st.mu.Lock()
	st.profile = &prof
	st.mu.Unlock()
	if js, err := json.Marshal(&prof); err == nil {
		_ = st.sess.SendControl(&wire.Profile{JSON: js})
	}
	st.h.logf("planck: stream resized from %s to %dx%d for a %dx%d window", from, p.Width, p.Height, w, h)
	return true
}

func (st *stream) run(parent context.Context) {
	defer close(st.done)
	st.ctx, st.cancel = context.WithCancel(parent)
	defer st.cancel()
	defer st.endMute()
	st.applyMute(st.h.SessionSettings().MuteSpeakers)
	st.h.lastInput.Store(st.h.now())
	st.sendPeerState()
	var wg sync.WaitGroup
	wg.Go(st.videoLoop)
	wg.Go(st.idleLoop)
	if st.sess.Config().AudioChannels > 0 && !st.h.cfg.NoAudio {
		wg.Go(st.audioLoop)
	}
	if st.sess.PeerFeatures()&wire.FeatureCursorState != 0 && !st.h.cfg.Synth {
		wg.Go(st.cursorLoop)
	}
	select {
	case <-st.sess.Done():
		if err := st.sess.Err(); err != nil && !errors.Is(err, transport.ErrClosed) {
			st.h.logf("planck: %s left: %v", st.sess.Identity(), err)
		}
	case <-st.ctx.Done():
	}
	st.cancel()
	wg.Wait()
	if st.mic != nil {
		st.mic.cancel()
	}
	st.bg.Wait()
	st.sess.Close()
}

// startMic finds the virtual mic before any packet comes, so the client
// can show where its microphone goes, or why it cannot.
func (st *stream) startMic() {
	h := st.h
	dev, hint := h.micStatus()
	st.micDevice, st.micHint = dev.Input, hint
	if h.cfg.NoMic || hint != "" {
		return
	}
	open := h.cfg.OpenMicSink
	if open == nil {
		open = audio.OpenMicSink
	}
	st.mic = &micReceiver{dev: dev, open: open, logf: h.logf, wg: &st.bg}
	st.mic.ctx, st.mic.cancel = context.WithCancel(context.Background())
}

// sendPeerState tells a client that reads it what this host does with the
// microphone and the clipboard.
func (st *stream) sendPeerState() {
	if st.sess.PeerFeatures()&wire.FeaturePeerState == 0 {
		return
	}
	ps := &wire.PeerState{MicDevice: st.micDevice, MicHint: st.micHint}
	if c := st.h.clip; c != nil {
		cfg := c.config()
		ps.ClipboardSend, ps.ClipboardRecv = cfg.Send, cfg.Receive
	}
	_ = st.sess.SendControl(ps)
}

// end closes the session; once it returns, no more of its client's input
// arrives.
func (st *stream) end() {
	st.once.Do(func() {
		st.endedByHost.Store(true)
		st.sess.Close()
	})
}

// senderDropped runs on the video loop for a frame the sender dropped.
func (st *stream) senderDropped(id uint32) {
	st.dropped.Add(1)
	if slot := &st.pts[id%ptsRingSize]; slot.id == id {
		slot.dropped = true
	}
	st.invalidate(id)
}

func (st *stream) invalidate(frameID uint32) { st.invalidateGap(frameID, frameID) }

// invalidGap is a run of frames a client lost, from to through inclusive.
type invalidGap struct{ from, through uint32 }

func (st *stream) invalidateGap(from, through uint32) {
	select {
	case st.invalid <- invalidGap{from, through}:
	default:
		st.keyNeeded.Store(true)
	}
}

func (st *stream) videoLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if revert, err := boostVideoThread(); err != nil {
		st.h.logf("planck: video thread priority unchanged: %v", err)
	} else {
		defer revert()
	}
	var pipe VideoPipe
	var vd VirtualDisplay
	st.trace = newTracer[hostTraceRow](traceHostEnv)
	// Deferred calls run while a panic unwinds this goroutine, so the
	// virtual display goes with the session however it ends. A crash
	// elsewhere stops the driver pings and its watchdog removes it.
	defer func() {
		if pipe != nil {
			pipe.Close()
		}
		if vd != nil {
			st.closeVirtualDisplay(vd)
		}
		if err := st.trace.flush(); err != nil {
			st.h.logf("planck: trace: %v", err)
		}
		if err := st.h.inputTrace.flush(); err != nil {
			st.h.logf("planck: trace: %v", err)
		}
	}()
	// Frames go out from a sender goroutine of their own; one that falls
	// two frames behind drops the oldest, and the encoder stops predicting
	// from it before the next frame, a round trip before the client's
	// RefInvalidate for the gap would arrive.
	send := newVideoSender(st.sess, 2, st.senderDropped, st.frameSent)
	defer send.close()
	buf := send.buffer()
	kbps := st.params.BitrateKbps
	var frameID uint32
	forceKey := true
	// recovery marks the next frame encoded after an invalidation.
	recovery := false
	mirror := fec.NewPolicy()
	mirror.MinLoss = st.h.cfg.FECMinLoss
	shard := st.sess.ShardSize()
	slices := max(1, int(st.sess.Config().Slices))
	out := &sliceSender{send: send, slices: uint8(slices)}
	emit := out.emit
	for st.ctx.Err() == nil {
		select {
		case req := <-st.resize:
			if st.resizeTo(req, &pipe, &vd) {
				kbps = st.sess.TargetBitrateKbps()
				slices = max(1, int(st.sess.Config().Slices))
				out.slices, out.epoch = uint8(slices), st.epoch
			}
		case id := <-st.switchTo:
			if st.switchDisplayTo(id, &pipe, &vd) {
				kbps = st.sess.TargetBitrateKbps()
				slices = max(1, int(st.sess.Config().Slices))
				out.slices, out.epoch = uint8(slices), st.epoch
			}
		default:
		}
		if pipe == nil && st.params.VirtualDisplay && vd == nil {
			var err error
			if vd, err = st.openVirtualDisplay(); err != nil {
				st.h.fail("virtual display: %v; capturing the physical display", err)
			}
		}
		if pipe == nil {
			p := st.params
			p.BitrateKbps = kbps
			var err error
			if pipe, err = st.h.open(p); err != nil {
				st.h.fail("video: %v", err)
				pipe = nil
				if !st.sleep(reopenDelay) {
					return
				}
				continue
			}
			if w, ok := pipe.(encoderWatcher); ok {
				w.watchEncoder(func(cfg encode.Config) {
					if st.followEncoder(cfg.HDR10) {
						out.epoch = st.epoch
					}
				})
			}
			if g, ok := pipe.(encodeGate); ok {
				// An invalidation that comes while the pipe waits for the
				// display applies to the picture captured, not the next:
				// a frame sooner, which at 120 fps keeps a report of a lost
				// frame inside maxInvalidSpan instead of a keyframe.
				g.gateEncode(func() bool {
					key, rec := st.takeInvalidations(pipe, frameID)
					forceKey, recovery = forceKey || key, recovery || rec
					out.recovery = recovery
					return forceKey
				})
			}
			info := pipe.Info()
			st.mu.Lock()
			st.info = info
			st.mu.Unlock()
			st.openPhase(pipe, info.FPS)
			st.h.targetInput(info)
			st.h.logf("planck: video %s %dx%d@%d on %s (%s), power %s, GPU priority %d", info.Codec, info.Width, info.Height, info.FPS,
				info.Adapter.Name, info.Source, info.Power, info.GPUPriority)
			forceKey = true
		}
		if st.sess.KeyframeRequested() || st.keyNeeded.Swap(false) {
			forceKey = true
		}
		key, rec := st.takeInvalidations(pipe, frameID)
		forceKey, recovery = forceKey || key, recovery || rec
		var setRate int64
		if t := st.sess.TargetBitrateKbps(); t > 0 && abs(t-kbps)*20 > kbps {
			t0 := transport.Mono()
			if err := pipe.SetBitrate(t); err == nil {
				kbps = t
			}
			setRate = transport.Mono() - t0
		}
		var phaseStep int64
		if ctl, steer := st.phase.Load(), steererOf(pipe); ctl != nil && steer != nil {
			if phaseStep = ctl.Advance(frameID); phaseStep != 0 {
				steer.steerPhase(phaseStep)
			}
		}
		var pkt encode.Packet
		var captureTS int64
		var err error
		streamed := false
		if sp, ok := pipe.(slicePipe); ok && st.params.SubFrame {
			out.start(frameID, recovery)
			pkt, captureTS, streamed, err = sp.NextSlices(captureWait, forceKey, buf, emit)
			if err != nil && out.begun {
				// Some slices of this frame went out: its ID is taken, and
				// its buffer is the sender's until they are sent.
				frameID++
				send.push(&sendJob{kind: sendEnd, info: out.info, buf: buf})
				buf = send.buffer()
			}
		} else {
			pkt, captureTS, err = pipe.Next(captureWait, forceKey, buf)
		}
		switch {
		case errors.Is(err, transport.ErrClosed) || st.ctx.Err() != nil:
			return
		case errors.Is(err, capture.ErrTimeout):
			continue
		case errors.Is(err, encode.ErrDropped):
			st.dropped.Add(1)
			continue
		case errors.Is(err, capture.ErrAccessLost):
			st.h.logf("planck: capture access lost; reopening")
			pipe.Close()
			pipe = nil
			continue
		case err != nil:
			st.h.fail("video: %v", err)
			pipe.Close()
			pipe = nil
			if !st.sleep(reopenDelay) {
				return
			}
			continue
		}
		if !streamed && len(pkt.Data) < slices {
			continue
		}
		if pkt.Keyframe {
			forceKey = false
			st.keyframes.Add(1)
		}
		info := wire.VideoInfo{FrameID: frameID, Keyframe: pkt.Keyframe, Recovery: recovery && !pkt.Keyframe, CaptureTS: captureTS, EncodeTS: transport.Mono(), Epoch: st.epoch}
		recovery = false
		slot := &st.pts[frameID%ptsRingSize]
		slot.id, slot.pts, slot.dropped, slot.invalidated = frameID, pkt.PTS, false, false
		job := &sendJob{kind: sendFrame, info: info, data: pkt.Data, buf: pkt.Data, bytes: len(pkt.Data), sent: true}
		switch {
		case streamed:
			info = out.info
			job.kind, job.info, job.data = sendEnd, info, nil
		case st.params.Codec == encode.PyroWave && len(pkt.SliceEnds) > 0:
			job.kind, job.critical = sendChunked, pkt.Critical
			job.nEnds = copy(job.ends[:], pkt.SliceEnds)
		}
		frameID++
		acquired := captureTS
		sp, staged := pipe.(interface{ stages() pipeStages })
		if staged {
			acquired = sp.stages().Acquired
		}
		if st.trace != nil {
			job.traced = true
			job.row = hostTraceRow{Frame: info.FrameID, Key: pkt.Keyframe, Bytes: len(pkt.Data), Present: captureTS, Kbps: kbps, SetRateNs: setRate, PhaseNs: phaseStep}
			if staged {
				g := sp.stages()
				job.row.WaitStart, job.row.Acquired, job.row.EncStart, job.row.EncEnd, job.row.FirstSlice, job.row.Accum = g.WaitStart, g.Acquired, g.EncStart, g.EncEnd, g.FirstSlice, g.Accum
				job.row.SubmitNs, job.row.QueryNs, job.row.Tries = g.SubmitNs, g.QueryNs, g.Tries
			}
		}
		send.push(job)
		buf = send.buffer()
		mirror.SetLoss(st.sess.FECLoss())
		data, parity := 0, 0
		for s := 0; s < slices; s++ {
			n := len(pkt.Data)*(s+1)/slices - len(pkt.Data)*s/slices
			d := (n + shard - 1) / shard
			blocks := max(1, (d+fec.MaxDataPerBlock-1)/fec.MaxDataPerBlock)
			for b := 0; b < blocks; b++ {
				db := d / blocks
				if b < d%blocks {
					db++
				}
				parity += max(mirror.Parity(db), fec.BurstFloor(db, blocks, pkt.Keyframe))
			}
			data += d
		}
		st.mu.Lock()
		st.encodeMs.add(ms(info.EncodeTS - captureTS))
		st.captureW.add(ms(acquired - captureTS))
		st.encodeW.add(ms(info.EncodeTS - acquired))
		st.parityPct = 100 * float64(parity) / float64(max(data, 1))
		st.mu.Unlock()
	}
}

// slicePipe is a VideoPipe that can send slices as the encoder finishes
// them (sourcePipe).
type slicePipe interface {
	NextSlices(timeout time.Duration, forceKey bool, dst []byte, emit sliceEmit) (encode.Packet, int64, bool, error)
}

// sliceSender queues one frame's slices for the sender as they come. Every
// slice of a frame carries the same VideoInfo, stamped when the first one
// was ready: EncodeTS is then when sending began.
type sliceSender struct {
	send     *videoSender
	slices   uint8
	epoch    uint8
	frame    uint32
	recovery bool
	info     wire.VideoInfo
	begun    bool
}

func (s *sliceSender) start(frame uint32, recovery bool) {
	s.frame, s.recovery, s.begun = frame, recovery, false
}

func (s *sliceSender) emit(captureTS int64, index int, slice []byte, key bool) error {
	if !s.begun {
		s.info = wire.VideoInfo{FrameID: s.frame, Keyframe: key, Recovery: s.recovery && !key, CaptureTS: captureTS, EncodeTS: transport.Mono(), SliceCount: s.slices, Epoch: s.epoch}
		s.begun = true
	}
	s.send.push(&sendJob{kind: sendSlice, info: s.info, slice: index, data: slice})
	return nil
}

// frameSent runs on the sender goroutine after each frame.
func (st *stream) frameSent(j *sendJob, err error) {
	if err != nil {
		if !errors.Is(err, transport.ErrClosed) && st.ctx.Err() == nil {
			st.h.fail("video send: %v", err)
		}
		return
	}
	if !j.sent {
		return
	}
	st.frames.Add(1)
	st.sent.add(transport.Mono(), j.bytes)
	if j.traced {
		st.trace.add(j.row)
	}
}

// maxInvalidSpan is how many frames back an invalidation may reach: NVENC
// keeps refDPB (5) pictures, and invalidating more than four of them may
// leave it nothing the client still holds to predict from.
const maxInvalidSpan = 4

// takeInvalidations is applyInvalidations, logging a fallback to a
// keyframe.
func (st *stream) takeInvalidations(pipe VideoPipe, next uint32) (key, recovery bool) {
	key, recovery, why := st.applyInvalidations(pipe, next)
	if why != "" {
		st.h.logf("planck: reference invalidation fell back to a keyframe: %s", why)
	}
	return key, recovery
}

// applyInvalidations tells the encoder to stop predicting from each frame
// a client lost and every frame encoded since, up to next, the frame about
// to be encoded. key says only a keyframe recovers, and why says why;
// recovery that the next frame recovers without one, and must say so
// (wire.VideoInfo.Recovery). A frame the sender dropped is invalidated at
// once; the client's report of the gap it left changes nothing, unless the
// gap runs on past the frames invalidated with it.
func (st *stream) applyInvalidations(pipe VideoPipe, next uint32) (key, recovery bool, why string) {
	for {
		select {
		case g := <-st.invalid:
			id, handled := st.unhandled(g)
			span := int32(next - id)
			switch {
			case key || span <= 0 || handled:
			case span > maxInvalidSpan:
				key, why = true, fmt.Sprintf("frame %d is %d frames back, past %d", id, span, maxInvalidSpan)
			default:
				for f := id; f != next; f++ {
					slot := &st.pts[f%ptsRingSize]
					if slot.id != f {
						key, why = true, fmt.Sprintf("frame %d is no longer in the PTS ring", f)
						break
					}
					if err := pipe.InvalidateRefs(slot.pts); err != nil {
						key, why = true, fmt.Sprintf("frame %d: %v", f, err)
						break
					}
					slot.invalidated = true
				}
				recovery = true
			}
		default:
			return key, recovery && !key, why
		}
	}
}

// unhandled is the first frame of g that still needs invalidating. A gap
// that starts at a frame the sender dropped skips the frames invalidated
// since; handled says that covered all of g, and the Recovery frame after
// them is still the client's to receive.
func (st *stream) unhandled(g invalidGap) (id uint32, handled bool) {
	id = g.from
	if slot := &st.pts[id%ptsRingSize]; slot.id != id || !slot.dropped || !slot.invalidated {
		return id, false
	}
	for ; int32(g.through-id) >= 0; id++ {
		if slot := &st.pts[id%ptsRingSize]; slot.id != id || !slot.invalidated {
			return id, false
		}
	}
	return id, true
}

func (st *stream) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-st.ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// audioLoop sends loopback audio as 10 ms Opus packets. Audio is optional:
// without a device the video keeps streaming.
func (st *stream) audioLoop() {
	open := st.h.cfg.OpenAudio
	if open == nil {
		open = func() (audio.Capturer, error) {
			return newAudioCapturer(audio.CaptureOptions{ExcludePIDs: st.h.audioExcludePIDs(), DeviceID: st.h.cfg.AudioDevice})
		}
	}
	capt, err := open()
	if err != nil {
		st.h.logf("planck: audio unavailable: %v", err)
		return
	}
	defer capt.Close()
	enc, err := audio.NewEncoder(audio.EncoderConfig{})
	if err != nil {
		st.h.logf("planck: audio unavailable: %v", err)
		return
	}
	frame := make([]int16, enc.FrameSamples()*audio.Channels)
	packet := make([]byte, audio.MaxPacketSize)
	fill := 0
	var seq uint32
	err = capt.Start(st.ctx, func(pcm []int16, _ int64) {
		for len(pcm) > 0 {
			n := copy(frame[fill:], pcm)
			fill += n
			pcm = pcm[n:]
			if fill < len(frame) {
				return
			}
			fill = 0
			size, err := enc.Encode(frame, packet)
			if err != nil {
				continue
			}
			if st.sess.SendAudio(seq, transport.Mono(), packet[:size]) == nil {
				st.audioPkts.Add(1)
			}
			seq++
		}
	})
	if err != nil {
		st.h.logf("planck: audio unavailable: %v", err)
		return
	}
	if m, ok := capt.(interface{ Mode() string }); ok {
		st.h.logf("planck: desktop audio captured by %s", m.Mode())
	}
	<-st.ctx.Done()
}

func (st *stream) fill(out *HostStats) {
	out.Connected = true
	out.Client = st.sess.Identity()
	out.ClientAddr = st.addr
	out.Path = st.path.String()
	out.TargetKbps = st.sess.TargetBitrateKbps()
	out.RTTMs = ms(int64(st.sess.RTT()))
	out.FECLossPct = 100 * st.sess.FECLoss()
	out.Frames = st.frames.Load()
	out.Keyframes = st.keyframes.Load()
	out.Dropped = st.dropped.Load()
	out.PLIs = st.sess.Stats().PLIsReceived
	out.AudioPackets = st.audioPkts.Load()
	out.Mic = MicStats{Device: st.micDevice, Hint: st.micHint}
	if st.mic != nil {
		out.Mic.Packets, out.Mic.Concealed = st.mic.stats()
	}
	out.FPS, out.SentKbps = st.sent.per(transport.Mono())
	st.mu.Lock()
	out.Video = st.info
	out.EncodeMs = st.encodeMs.summary()
	out.FECParityPct = st.parityPct
	out.VirtualDisplay = st.vdName
	out.Profile = st.profile
	st.mu.Unlock()
}

// hostStats is what the client cannot read in the video headers.
func (st *stream) hostStats() *wire.HostStats {
	st.mu.Lock()
	defer st.mu.Unlock()
	return &wire.HostStats{CaptureMicros: micros(st.captureW.summary().P50), EncodeMicros: micros(st.encodeW.summary().P50)}
}

func micros(ms float64) uint32 { return uint32(max(ms, 0) * 1000) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
