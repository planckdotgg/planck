package session

import (
	"context"
	"errors"
	"net"
	"runtime"
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
	pts     [ptsRingSize]struct {
		id  uint32
		pts int64
	}

	mu        sync.Mutex
	info      VideoInfo
	vdName    string
	encodeMs  *window
	sent      rate
	parityPct float64

	frames, keyframes, dropped, audioPkts atomic.Uint64

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
	st := &stream{h: h, sess: sess, params: p, done: make(chan struct{}), invalid: make(chan uint32, 16), encodeMs: newWindow(encodeWindow)}
	if ua, ok := sess.RemoteControlAddr().(*net.UDPAddr); ok {
		st.addr = ua.String()
		st.path = ratectl.ClassifyAddr(ua.AddrPort().Addr())
	}
	prof, err := ParseProfile(sess.Profile())
	if err != nil {
		h.logf("planck: %v; using the legacy config", err)
	}
	if prof != nil {
		st.profile = prof
		st.params = paramsFromProfile(st.params, prof, sess.ShardSize())
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

// openVirtualDisplay makes the session's virtual display and points the
// pipeline at it. Without one the stream falls back to the physical
// display on its own GPU.
func (st *stream) openVirtualDisplay() (func() error, error) {
	fp := st.sess.PeerFingerprint()
	gdi, closeVD, err := st.h.vdd.create(st.params, fp[:], st.h.cfg.VDDPrimary)
	if err != nil {
		st.params.VirtualDisplay, st.params.Adapter = false, ""
		return nil, err
	}
	st.params.Display = gdi
	st.mu.Lock()
	st.vdName = gdi
	st.mu.Unlock()
	st.h.logf("planck: virtual display %s %dx%d@%d on %s", gdi, st.params.Width, st.params.Height, st.params.FPS, st.params.Adapter)
	return func() error {
		err := closeVD()
		st.mu.Lock()
		st.vdName = ""
		st.mu.Unlock()
		st.h.logf("planck: virtual display %s removed (err %v)", gdi, err)
		return err
	}, nil
}

func (st *stream) run(parent context.Context) {
	defer close(st.done)
	st.ctx, st.cancel = context.WithCancel(parent)
	defer st.cancel()
	var wg sync.WaitGroup
	wg.Go(st.videoLoop)
	if st.sess.Config().AudioChannels > 0 && !st.h.cfg.NoAudio {
		wg.Go(st.audioLoop)
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
	st.sess.Close()
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
	var closeVD func() error
	st.trace = newTracer[hostTraceRow](traceHostEnv)
	// Deferred calls run while a panic unwinds this goroutine, so the
	// virtual display goes with the session however it ends. A crash
	// elsewhere stops the driver pings and its watchdog removes it.
	defer func() {
		if pipe != nil {
			pipe.Close()
		}
		if closeVD != nil {
			closeVD()
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
		if pipe == nil && st.params.VirtualDisplay && closeVD == nil {
			var err error
			if closeVD, err = st.openVirtualDisplay(); err != nil {
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
		info := wire.VideoInfo{FrameID: frameID, Keyframe: pkt.Keyframe, CaptureTS: captureTS, EncodeTS: transport.Mono()}
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
		if st.trace != nil {
			row := hostTraceRow{Frame: info.FrameID, Key: pkt.Keyframe, Bytes: len(pkt.Data), Present: captureTS, SendEnd: transport.Mono(), Kbps: kbps, SetRateNs: setRate}
			if sp, ok := pipe.(interface{ stages() pipeStages }); ok {
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
	frame  uint32
	info   wire.VideoInfo
	begun  bool
}

func (s *sliceSender) start(frame uint32) { s.frame, s.begun = frame, false }

func (s *sliceSender) emit(captureTS int64, index int, slice []byte, key bool) error {
	if !s.begun {
		s.info = wire.VideoInfo{FrameID: s.frame, Keyframe: key, CaptureTS: captureTS, EncodeTS: transport.Mono(), SliceCount: s.slices}
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
		open = newAudioCapturer
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
	out.FPS, out.SentKbps = st.sent.per(transport.Mono())
	st.mu.Lock()
	out.Video = st.info
	out.EncodeMs = st.encodeMs.summary()
	out.FECParityPct = st.parityPct
	out.VirtualDisplay = st.vdName
	st.mu.Unlock()
	out.Profile = st.profile
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
