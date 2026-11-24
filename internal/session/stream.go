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

	invalid chan uint32
	// epoch counts the streams a resize switched to; every frame carries
	// it. Only the video loop touches it.
	epoch uint8
	// resize holds the client window's latest size, in pixels, for the
	// video loop.
	resize chan [2]int
	pts    [ptsRingSize]struct {
		id  uint32
		pts int64
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
	st := &stream{h: h, sess: sess, params: p, done: make(chan struct{}), invalid: make(chan uint32, 16), resize: make(chan [2]int, 1),
		encodeMs: newWindow(encodeWindow), captureW: newWindow(encodeWindow), encodeW: newWindow(encodeWindow)}
	if ua, ok := sess.RemoteControlAddr().(*net.UDPAddr); ok {
		st.addr = ua.String()
		st.path = ratectl.ClassifyAddr(ua.AddrPort().Addr())
	}
	st.startMic()
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
	err := vd.Close()
	st.mu.Lock()
	st.vdName = ""
	st.mu.Unlock()
	st.h.logf("planck: virtual display %s removed (err %v)", name, err)
}

// requestResize leaves the client window's size for the video loop,
// replacing one it has not taken yet.
func (st *stream) requestResize(w, h int) {
	for {
		select {
		case st.resize <- [2]int{w, h}:
			return
		default:
		}
		select {
		case <-st.resize:
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
// one. It reports whether the stream changed.
func (st *stream) resizeTo(w, h int, pipe *VideoPipe, vd *VirtualDisplay) bool {
	switches := st.sess.PeerFeatures()&wire.FeatureStreamSwitch != 0
	if st.profile == nil || !st.params.VirtualDisplay || (st.params.Codec == encode.PyroWave && !switches) {
		return false
	}
	var cc clientCaps
	if err := json.Unmarshal(st.sess.PeerCapabilities(), &cc); err != nil || cc.V != clientCapsVersion {
		return false
	}
	cc.Client.Display.ViewWidthPx, cc.Client.Display.ViewHeightPx = w, h
	var remote netip.Addr
	if ua, ok := st.sess.RemoteControlAddr().(*net.UDPAddr); ok {
		remote = ua.AddrPort().Addr()
	}
	cc.Route.Kind = st.profile.Route
	p := st.h.resolveProfile(cc, remote, st.path, st.sess.MaxDatagram() > wire.MaxDatagram)
	if !p.VirtualDisplay {
		return false
	}
	if !switches {
		return st.resizeLegacy(w, h, &p, pipe, vd)
	}
	if sameStream(st.profile, &p) {
		return false
	}
	if *pipe != nil {
		(*pipe).Close()
		*pipe = nil
	}
	from := st.profile
	next := paramsFromProfile(st.params, &p, st.sess.ShardSize())
	if *vd != nil {
		switch {
		case next.Adapter != st.params.Adapter || next.FPS != st.params.FPS:
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
	st.sess.SetStream(st.h.profileConfig(&p, st.sess.Config()), start, p.MaxBitrateKbps, transport.ParityProfile{
		MinPercent: p.FEC.MinParityPercent, KeyframePercent: p.FEC.KeyframeParityPercent, CriticalPercent: p.FEC.CriticalParityPercent})
	st.mu.Lock()
	st.profile = &p
	st.mu.Unlock()
	if js, err := json.Marshal(&p); err == nil {
		_ = st.sess.SendControl(&wire.Stream{Epoch: st.epoch, Profile: js})
	}
	st.h.logf("planck: stream %d moved from %s %dx%d to %s %dx%d@%d for a %dx%d window", st.epoch, from.Codec, from.Width, from.Height,
		p.Codec, p.Width, p.Height, p.FPS, w, h)
	return true
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
	st.sendPeerState()
	var wg sync.WaitGroup
	wg.Go(st.videoLoop)
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

// stop ends the session and waits for its goroutines.
func (st *stream) stop() {
	st.once.Do(func() {
		st.sess.Close()
		<-st.done
	})
}

func (st *stream) invalidate(frameID uint32) {
	select {
	case st.invalid <- frameID:
	default:
		st.sess.SendControl(&wire.PLI{})
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
	buf := make([]byte, 0, 2<<20)
	kbps := st.params.BitrateKbps
	var frameID uint32
	forceKey := true
	mirror := fec.NewPolicy()
	mirror.MinLoss = st.h.cfg.FECMinLoss
	shard := st.sess.ShardSize()
	slices := max(1, int(st.sess.Config().Slices))
	out := &sliceSender{sess: st.sess, slices: uint8(slices)}
	emit := out.emit
	for st.ctx.Err() == nil {
		select {
		case sz := <-st.resize:
			if st.resizeTo(sz[0], sz[1], &pipe, &vd) {
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
			info := pipe.Info()
			st.mu.Lock()
			st.info = info
			st.mu.Unlock()
			st.h.targetInput(info)
			st.h.logf("planck: video %s %dx%d@%d on %s (%s), power %s, GPU priority %d", info.Codec, info.Width, info.Height, info.FPS,
				info.Adapter.Name, info.Source, info.Power, info.GPUPriority)
			forceKey = true
		}
		if st.sess.KeyframeRequested() {
			forceKey = true
		}
		forceKey = st.applyInvalidations(pipe) || forceKey
		var setRate int64
		if t := st.sess.TargetBitrateKbps(); t > 0 && abs(t-kbps)*20 > kbps {
			t0 := transport.Mono()
			if err := pipe.SetBitrate(t); err == nil {
				kbps = t
			}
			setRate = transport.Mono() - t0
		}
		var pkt encode.Packet
		var captureTS int64
		var err error
		streamed := false
		if sp, ok := pipe.(slicePipe); ok && st.params.SubFrame {
			out.start(frameID)
			pkt, captureTS, streamed, err = sp.NextSlices(captureWait, forceKey, buf, emit)
			if err != nil && out.begun {
				// Some slices of this frame went out: its ID is taken.
				frameID++
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
		info := wire.VideoInfo{FrameID: frameID, Keyframe: pkt.Keyframe, CaptureTS: captureTS, EncodeTS: transport.Mono(), Epoch: st.epoch}
		slot := &st.pts[frameID%ptsRingSize]
		slot.id, slot.pts = frameID, pkt.PTS
		switch {
		case streamed:
			info = out.info
		case st.params.Codec == encode.PyroWave && len(pkt.SliceEnds) > 0:
			err = st.sess.SendChunkedFrame(info, pkt.Data, pkt.SliceEnds, pkt.Critical)
		default:
			err = st.sess.SendVideoFrame(info, pkt.Data)
		}
		frameID++
		buf = pkt.Data[:0]
		if err != nil {
			if errors.Is(err, transport.ErrClosed) || st.ctx.Err() != nil {
				return
			}
			st.h.fail("video send: %v", err)
			continue
		}
		st.frames.Add(1)
		acquired := captureTS
		sp, staged := pipe.(interface{ stages() pipeStages })
		if staged {
			acquired = sp.stages().Acquired
		}
		if st.trace != nil {
			row := hostTraceRow{Frame: info.FrameID, Key: pkt.Keyframe, Bytes: len(pkt.Data), Present: captureTS, SendEnd: transport.Mono(), Kbps: kbps, SetRateNs: setRate}
			if staged {
				g := sp.stages()
				row.WaitStart, row.Acquired, row.EncStart, row.EncEnd, row.FirstSlice, row.Accum = g.WaitStart, g.Acquired, g.EncStart, g.EncEnd, g.FirstSlice, g.Accum
				row.SubmitNs, row.QueryNs, row.Tries = g.SubmitNs, g.QueryNs, g.Tries
			}
			st.trace.add(row)
		}
		mirror.SetLoss(st.sess.FECLoss())
		data := 0
		for s := 0; s < slices; s++ {
			n := len(pkt.Data)*(s+1)/slices - len(pkt.Data)*s/slices
			data += (n + shard - 1) / shard
		}
		st.mu.Lock()
		st.encodeMs.add(ms(info.EncodeTS - captureTS))
		st.captureW.add(ms(acquired - captureTS))
		st.encodeW.add(ms(info.EncodeTS - acquired))
		st.parityPct = 100 * float64(mirror.Parity(data)) / float64(max(data, 1))
		st.mu.Unlock()
		st.sent.add(info.EncodeTS, len(pkt.Data))
	}
}

// slicePipe is a VideoPipe that can send slices as the encoder finishes
// them (sourcePipe).
type slicePipe interface {
	NextSlices(timeout time.Duration, forceKey bool, dst []byte, emit sliceEmit) (encode.Packet, int64, bool, error)
}

// sliceSender sends one frame's slices as they come. Every slice of a
// frame carries the same VideoInfo, stamped when the first one was ready:
// EncodeTS is then when sending began.
type sliceSender struct {
	sess   *transport.Session
	slices uint8
	epoch  uint8
	frame  uint32
	info   wire.VideoInfo
	begun  bool
}

func (s *sliceSender) start(frame uint32) { s.frame, s.begun = frame, false }

func (s *sliceSender) emit(captureTS int64, index int, slice []byte, key bool) error {
	if !s.begun {
		s.info = wire.VideoInfo{FrameID: s.frame, Keyframe: key, CaptureTS: captureTS, EncodeTS: transport.Mono(), SliceCount: s.slices, Epoch: s.epoch}
		s.begun = true
	}
	return s.sess.SendVideoSlice(s.info, index, slice)
}

// applyInvalidations forwards lost reference frames to the encoder and
// reports whether only a keyframe can recover.
func (st *stream) applyInvalidations(pipe VideoPipe) bool {
	key := false
	for {
		select {
		case id := <-st.invalid:
			slot := st.pts[id%ptsRingSize]
			if slot.id != id || pipe.InvalidateRefs(slot.pts) != nil {
				key = true
			}
		default:
			return key
		}
	}
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
			return newAudioCapturer(audio.CaptureOptions{ExcludePIDs: st.h.cfg.AudioExcludePIDs})
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
