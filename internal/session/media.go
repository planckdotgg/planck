package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
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

func (p *sourcePipe) openEncoder(f capture.Frame) error {
	if p.enc != nil {
		p.enc.Close()
		p.enc = nil
	}
	cfg := encode.Config{
		Codec: p.params.Codec, Chroma: p.params.Chroma, Source: f.Format,
		Width: f.Width &^ 1, Height: f.Height &^ 1, FPS: p.params.FPS, BitrateKbps: p.params.BitrateKbps,
		Slices: p.params.Slices, SubFrame: p.params.SubFrame, Split: p.params.Split,
	}
	enc, err := encode.Open(cfg, p.devices...)
	if errors.Is(err, encode.ErrUnsupported) && (cfg.Slices > 1 || cfg.SubFrame) {
		// Transport slices are independent of codec slices; a backend
		// without slice support (VideoToolbox) encodes whole pictures.
		cfg.Slices, cfg.SubFrame = 0, false
		enc, err = encode.Open(cfg, p.devices...)
	}
	if err != nil {
		return err
	}
	if p.wrap != nil {
		enc = p.wrap(enc)
	}
	p.enc, p.cfg, p.srcFmt, p.w, p.h = enc, cfg, f.Format, f.Width, f.Height
	p.info.Width, p.info.Height = cfg.Width, cfg.Height
	return nil
}

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
	}
	return encode.HEVC
}

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

// ParseCodec reads a codec flag value: h264, hevc or av1.
func ParseCodec(s string) (encode.Codec, error) {
	switch strings.ToLower(s) {
	case "h264", "avc":
		return encode.H264, nil
	case "hevc", "h265", "":
		return encode.HEVC, nil
	case "av1":
		return encode.AV1, nil
	}
	return 0, fmt.Errorf("session: unknown codec %q (want h264, hevc or av1)", s)
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
