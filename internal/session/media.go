package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// VideoParams is what the host asks a video pipeline for once a client's
// session is negotiated.
type VideoParams struct {
	Display string
	// Adapter is a case-insensitive substring of the encoding GPU's name
	// ("NVIDIA", "Radeon", "UHD"). Empty encodes on the GPU that owns the
	// frames.
	Adapter     string
	Codec       encode.Codec
	Chroma      encode.Chroma
	Width       int
	Height      int
	FPS         int
	BitrateKbps int
	Slices      int
	SubFrame    bool
	Split       encode.Split
	// Synth replaces display capture with generated frames on Adapter's GPU.
	Synth bool
	// VirtualDisplay asks for a SudoVDA display of Width x Height at FPS
	// on Adapter's GPU, captured in place of Display.
	VirtualDisplay bool
	// ScalePct is the Windows display scale a virtual display takes, in
	// percent: the client display's (100 for a 1x display).
	ScalePct int
	// ChunkLen is the shard size PyroWave frames its chunks in.
	ChunkLen int
	// HDR10 streams an HDR desktop as BT.2020 PQ, when capture delivers
	// it 10-bit; an 8-bit capture encodes SDR.
	HDR10 bool
}

// VideoInfo describes an open pipeline.
type VideoInfo struct {
	Width, Height, FPS int
	Codec              encode.Codec
	Chroma             encode.Chroma
	Adapter            gpu.Adapter
	Power              gpu.PowerMode
	// GPUPriority is the encoding device's GPU thread priority, -7 to 7.
	GPUPriority int
	Source      string
	// Display is where the captured display sits on the host's desktop,
	// and Desktop the bounds of every display, for absolute pointer moves;
	// zero where the source is no display or the platform does not say.
	Display, Desktop input.Rect
	// DisplayID is the captured display's capture.Display ID, empty for
	// any other source.
	DisplayID string
}

// inputArea is d's place on the desktop and the bounds of all the
// displays together, as Windows lays out its virtual desktop. d stands in
// for its own entry of all, which a list taken before a mode switch
// settled still has at the old size.
func inputArea(d capture.Display, all []capture.Display) (display, desktop input.Rect) {
	display = input.Rect{X: d.X, Y: d.Y, W: d.Width, H: d.Height}
	x0, y0, x1, y1 := d.X, d.Y, d.X+d.Width, d.Y+d.Height
	for _, o := range all {
		if o.ID == d.ID {
			continue
		}
		x0, y0 = min(x0, o.X), min(y0, o.Y)
		x1, y1 = max(x1, o.X+o.Width), max(y1, o.Y+o.Height)
	}
	return display, input.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// VideoPipe is one open capture-to-bitstream pipeline.
type VideoPipe interface {
	// Next waits up to timeout for the next picture and encodes it into dst.
	// captureTS is when the picture was presented, on transport.Mono.
	// capture.ErrTimeout means the screen did not change; capture.ErrAccessLost
	// means the pipe must be closed and opened again; encode.ErrDropped means
	// the encoder skipped the picture.
	Next(timeout time.Duration, forceKey bool, dst []byte) (pkt encode.Packet, captureTS int64, err error)
	SetBitrate(kbps int) error
	// InvalidateRefs stops prediction from the picture with this PTS and
	// later ones; encode.ErrUnsupported means only a keyframe recovers.
	InvalidateRefs(pts int64) error
	Info() VideoInfo
	Close() error
}

// VideoOpener opens a pipeline. The host calls it once per client session
// and again after capture.ErrAccessLost.
type VideoOpener func(VideoParams) (VideoPipe, error)

// sourcePipe runs a capture.Source into an encoder opened on the first
// frame, when the surface format and size are known.
type sourcePipe struct {
	src     capture.Source
	devices []gpu.Device
	params  VideoParams
	info    VideoInfo
	enc     encode.Encoder
	wrap    func(encode.Encoder) encode.Encoder
	cfg     encode.Config // the encoder's, after fallbacks
	// openEnc opens the encoder, encode.Open when nil.
	openEnc func(encode.Config, ...gpu.Device) (encode.Encoder, error)
	// opened hears of each encoder before it encodes its first picture.
	opened func(encode.Config)
	// gate runs between a picture's capture and its encode; true forces a
	// keyframe.
	gate func() bool
	// emit and emitTS are the current NextSlices call's; encSlice, made
	// once, forwards the encoder's slices to them.
	emit     sliceEmit
	emitTS   int64
	encSlice func(int, []byte, bool) error
	srcFmt   capture.Format
	w, h     int
	power    *gpu.PowerGuard
	closers  []func()
	st       pipeStages
}

// sliceEmit receives a slice of a picture as soon as the encoder finished
// it, with the picture's capture time on transport.Mono.
type sliceEmit func(captureTS int64, index int, slice []byte, keyframe bool) error

func (p *sourcePipe) Next(timeout time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	pkt, captureTS, _, err := p.next(timeout, forceKey, dst, nil)
	return pkt, captureTS, err
}

// NextSlices is Next that hands each slice to emit as the encoder finishes
// it, when the encoder can (encode.SliceEncoder); streamed reports whether
// it did.
func (p *sourcePipe) NextSlices(timeout time.Duration, forceKey bool, dst []byte, emit sliceEmit) (pkt encode.Packet, captureTS int64, streamed bool, err error) {
	return p.next(timeout, forceKey, dst, emit)
}

func (p *sourcePipe) next(timeout time.Duration, forceKey bool, dst []byte, emit sliceEmit) (encode.Packet, int64, bool, error) {
	p.st = pipeStages{WaitStart: transport.Mono()}
	f, err := p.src.Next(timeout)
	if errors.Is(err, capture.ErrAccessLost) {
		err = p.recover()
	}
	if err != nil {
		return encode.Packet{}, 0, false, err
	}
	defer p.src.Release()
	captureTS := transport.Mono()
	p.st.Acquired = captureTS
	if f.Presented > 0 && f.Acquired >= f.Presented && f.Acquired-f.Presented < time.Second {
		captureTS -= int64(f.Acquired - f.Presented)
	}
	if a, ok := p.src.(interface{ AccumulatedFrames() int }); ok {
		p.st.Accum = a.AccumulatedFrames()
	}
	if p.enc == nil || f.Format != p.srcFmt || f.Width != p.w || f.Height != p.h {
		if err := p.openEncoder(f); err != nil {
			return encode.Packet{}, 0, false, err
		}
		forceKey = true
	}
	if p.gate != nil && p.gate() {
		forceKey = true
	}
	p.st.EncStart = transport.Mono()
	var pkt encode.Packet
	se, streamed := p.enc.(encode.SliceEncoder)
	streamed = streamed && emit != nil && p.cfg.SubFrame
	if streamed {
		if p.encSlice == nil {
			p.encSlice = p.forwardSlice
		}
		p.emit, p.emitTS = emit, captureTS
		pkt, err = se.EncodeSlices(f, forceKey, dst, p.encSlice)
		p.emit = nil
	} else {
		pkt, err = p.enc.Encode(f, forceKey, dst)
	}
	p.st.EncEnd = transport.Mono()
	if fs, ok := p.enc.(interface{ FirstSlice() time.Duration }); ok && fs.FirstSlice() > 0 {
		p.st.FirstSlice = p.st.EncStart + int64(fs.FirstSlice())
	}
	if a, ok := p.enc.(interface {
		Stages() (time.Duration, time.Duration, int)
	}); ok {
		s, q, n := a.Stages()
		p.st.SubmitNs, p.st.QueryNs, p.st.Tries = int64(s), int64(q), n
	}
	return pkt, captureTS, streamed, err
}

// recoverer is a capture.Source that can duplicate its display again in
// place after capture.ErrAccessLost (dxgi.Source).
type recoverer interface {
	Recover(timeout, interval time.Duration) error
}

const (
	recoverFor   = 2 * time.Second
	recoverEvery = 20 * time.Millisecond
)

// recover takes a lost capture back on the same device and display, as a
// full-screen game's mode switch needs, without reopening the pipeline.
// The encoder goes with the old desktop images it may hold, and opens
// again, with a keyframe, on the next picture. capture.ErrTimeout says
// the caller may go on; capture.ErrAccessLost that the pipe must reopen.
func (p *sourcePipe) recover() error {
	r, ok := p.src.(recoverer)
	if !ok {
		return capture.ErrAccessLost
	}
	if p.enc != nil {
		p.enc.Close()
		p.enc = nil
	}
	if err := r.Recover(recoverFor, recoverEvery); err != nil {
		return err
	}
	return capture.ErrTimeout
}

func (p *sourcePipe) forwardSlice(i int, s []byte, key bool) error {
	return p.emit(p.emitTS, i, s, key)
}

// pipeStages are the transport.Mono times of the last Next.
type pipeStages struct {
	WaitStart, Acquired, EncStart, EncEnd, FirstSlice int64
	Accum                                             int
	SubmitNs, QueryNs                                 int64
	Tries                                             int
}

func (p *sourcePipe) stages() pipeStages { return p.st }

// hdrSource reports whether frames of format f carry the HDR10 the
// params ask for: only an RGB10A2 capture of an HDR desktop holds PQ.
func hdrSource(p VideoParams, f capture.Format) bool {
	return p.HDR10 && p.Chroma.TenBit() && f == capture.FormatRGB10A2
}

func (p *sourcePipe) openEncoder(f capture.Frame) error {
	if p.enc != nil {
		p.enc.Close()
		p.enc = nil
	}
	cfg := encode.Config{
		Codec: p.params.Codec, Chroma: p.params.Chroma, Source: f.Format,
		Width: f.Width &^ 1, Height: f.Height &^ 1, FPS: p.params.FPS, BitrateKbps: p.params.BitrateKbps,
		Slices: p.params.Slices, SubFrame: p.params.SubFrame, Split: p.params.Split, ChunkLen: p.params.ChunkLen,
		HDR10: hdrSource(p.params, f.Format),
	}
	open := p.openEnc
	if open == nil {
		open = encode.Open
	}
	enc, err := open(cfg, p.devices...)
	if errors.Is(err, encode.ErrUnsupported) && (cfg.Slices > 1 || cfg.SubFrame) {
		// Transport slices are independent of codec slices; a backend
		// without slice support (VideoToolbox) encodes whole pictures.
		cfg.Slices, cfg.SubFrame = 0, false
		enc, err = open(cfg, p.devices...)
	}
	if err != nil {
		return err
	}
	if p.wrap != nil {
		enc = p.wrap(enc)
	}
	p.enc, p.cfg, p.srcFmt, p.w, p.h = enc, cfg, f.Format, f.Width, f.Height
	p.info.Width, p.info.Height = cfg.Width, cfg.Height
	if p.opened != nil {
		p.opened(cfg)
	}
	return nil
}

func (p *sourcePipe) watchEncoder(fn func(encode.Config)) { p.opened = fn }

func (p *sourcePipe) gateEncode(fn func() bool) { p.gate = fn }

func (p *sourcePipe) SetBitrate(kbps int) error {
	p.params.BitrateKbps = kbps
	if p.enc == nil {
		return nil
	}
	return p.enc.SetBitrate(kbps)
}

func (p *sourcePipe) InvalidateRefs(pts int64) error {
	if ri, ok := p.enc.(encode.RefInvalidator); ok {
		return ri.InvalidateRefs(pts)
	}
	return encode.ErrUnsupported
}

func (p *sourcePipe) Info() VideoInfo { return p.info }

func (p *sourcePipe) Close() error {
	if p.enc != nil {
		p.enc.Close()
		p.enc = nil
	}
	err := p.src.Close()
	for i := len(p.closers) - 1; i >= 0; i-- {
		p.closers[i]()
	}
	p.power.Close()
	return err
}

// synthPattern paces pre-rendered GPU surfaces at a fixed rate on
// transport.Mono, standing in for a display that changes every refresh.
type synthPattern struct {
	frames   []capture.Frame
	interval int64
	next     int64
	i        int
	release  func()
}

func newSynthPattern(frames []capture.Frame, fps int, release func()) *synthPattern {
	return &synthPattern{frames: frames, interval: int64(time.Second) / int64(fps), release: release}
}

func (s *synthPattern) Next(timeout time.Duration) (capture.Frame, error) {
	now := transport.Mono()
	if s.next == 0 {
		s.next = now
	}
	if s.next-now > int64(timeout) {
		transport.SleepUntilMono(now + int64(timeout))
		return capture.Frame{}, capture.ErrTimeout
	}
	transport.SleepUntilMono(s.next)
	at := transport.Mono()
	if at-s.next > 4*s.interval {
		s.next = at
	}
	s.next += s.interval
	f := s.frames[s.i%len(s.frames)]
	s.i++
	f.Presented, f.Acquired = 0, 0
	return f, nil
}

func (s *synthPattern) Release() error { return nil }

func (s *synthPattern) Close() error {
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return nil
}

// synthFrameCount is how many distinct pattern frames are rendered up front:
// enough motion that the encoder cannot skip, few enough to fit in VRAM at
// 4K.
const synthFrameCount = 8

// DefaultVideoOpener captures a display, or the synthetic pattern when
// p.Synth is set, and encodes on the GPU that holds the frames.
func DefaultVideoOpener(p VideoParams) (VideoPipe, error) {
	if p.Synth {
		return openSynth(p)
	}
	return openDisplay(p)
}

func matchesAdapter(a gpu.Adapter, match string) bool {
	return match == "" || strings.Contains(strings.ToLower(a.Name), strings.ToLower(match)) ||
		strings.EqualFold(a.Vendor.String(), match)
}

var errCrossAdapter = errors.New("session: encoding on another GPU than the display's needs a cross-adapter copy, which Phase 0 measured slower than same-GPU encode; use -synth to benchmark that GPU")

func codecFromWire(c wire.Codec) encode.Codec {
	switch c {
	case wire.CodecH264:
		return encode.H264
	case wire.CodecAV1:
		return encode.AV1
	case wire.CodecPyroWave:
		return encode.PyroWave
	}
	return encode.HEVC
}

// codecToWire maps a legacy request's codec. PyroWave never goes in one:
// only the capability exchange may choose it.
func codecToWire(c encode.Codec) wire.Codec {
	switch c {
	case encode.H264:
		return wire.CodecH264
	case encode.AV1:
		return wire.CodecAV1
	}
	return wire.CodecHEVC
}

// chromaToWire maps the coded chroma onto the wire's subsampling; bit depth
// is in the bitstream.
func chromaToWire(c encode.Chroma) wire.Chroma {
	if c.Full() {
		return wire.Chroma444
	}
	return wire.Chroma420
}

// ParseCodec reads a codec flag value: h264, hevc, av1 or pyrowave.
func ParseCodec(s string) (encode.Codec, error) {
	switch strings.ToLower(s) {
	case "h264", "avc":
		return encode.H264, nil
	case "hevc", "h265", "":
		return encode.HEVC, nil
	case "av1":
		return encode.AV1, nil
	case "pyrowave":
		return encode.PyroWave, nil
	}
	return 0, fmt.Errorf("session: unknown codec %q (want h264, hevc, av1 or pyrowave)", s)
}

// ParseChroma reads a chroma flag value: nv12, p010, yuv444 or yuv444p10.
func ParseChroma(s string) (encode.Chroma, error) {
	switch strings.ToLower(s) {
	case "nv12", "420", "":
		return encode.NV12, nil
	case "p010":
		return encode.P010, nil
	case "yuv444", "444":
		return encode.YUV444, nil
	case "yuv444p10":
		return encode.YUV444P10, nil
	}
	return 0, fmt.Errorf("session: unknown chroma %q (want nv12, p010, yuv444 or yuv444p10)", s)
}
