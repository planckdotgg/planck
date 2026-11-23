//go:build darwin

package session

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/apple"
	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/decode"
	decvt "github.com/planckdotgg/planck/pkg/decode/vt"
	"github.com/planckdotgg/planck/pkg/encode"
	_ "github.com/planckdotgg/planck/pkg/encode/all"
	encvt "github.com/planckdotgg/planck/pkg/encode/vt"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/input"
)

var (
	cmSampleBufferGetFormatDescription func(sbuf uintptr) uintptr
	cmHEVCParameterSetAtIndex          func(f uintptr, i uint, set *uintptr, size *uint, count *uint, nalLen *int32) int32
)

func init() {
	apple.Register(&cmSampleBufferGetFormatDescription, apple.CoreMedia, "CMSampleBufferGetFormatDescription")
	apple.Register(&cmHEVCParameterSetAtIndex, apple.CoreMedia, "CMVideoFormatDescriptionGetHEVCParameterSetAtIndex")
}

var appleGPU = gpu.Adapter{Vendor: gpu.VendorApple, Name: "Apple GPU"}

func openDisplay(p VideoParams) (VideoPipe, error) {
	all, err := display.List()
	if err != nil {
		return nil, err
	}
	var d capture.Display
	for _, x := range all {
		if x.ID == p.Display || p.Display == "" && x.Primary {
			d = x
		}
	}
	if d.ID == "" {
		return nil, fmt.Errorf("session: no display %q", p.Display)
	}
	if !matchesAdapter(d.Adapter, p.Adapter) && !matchesAdapter(appleGPU, p.Adapter) {
		return nil, errCrossAdapter
	}
	if p.FPS == 0 {
		p.FPS = max(int(d.Refresh+0.5), 60)
	}
	power := gpu.AcquireHighPerf(d.Adapter)
	src, err := display.Open(d.ID, display.Options{Width: p.Width, Height: p.Height, FPS: p.FPS, NV12: true})
	if err != nil {
		power.Close()
		return nil, err
	}
	return &sourcePipe{
		src: src, devices: []gpu.Device{src.Device}, params: p, power: power, wrap: annexB,
		info: VideoInfo{Width: src.Display.Width, Height: src.Display.Height, FPS: p.FPS, Codec: p.Codec, Chroma: p.Chroma,
			Adapter: src.Device.Adapter, Power: power.Mode, Source: "display " + d.ID},
	}, nil
}

func openSynth(p VideoParams) (VideoPipe, error) {
	if !matchesAdapter(appleGPU, p.Adapter) {
		return nil, fmt.Errorf("session: no GPU matches %q on this Mac", p.Adapter)
	}
	w, h := p.Width&^1, p.Height&^1
	buf := make([]byte, w*h*3/2)
	var frames []capture.Frame
	release := func() {
		for _, f := range frames {
			apple.CFRelease(f.Surface)
		}
	}
	for i := 0; i < synthFrameCount; i++ {
		pb, err := encvt.NewPixelBuffer(w, h, encvt.Pixel420v)
		if err != nil {
			release()
			return nil, err
		}
		synth.NV12(buf, w, h, i*8)
		encvt.FillPixelBuffer(pb, [][]byte{buf[:w*h], buf[w*h:]}, []int{w, w})
		frames = append(frames, capture.Frame{Surface: pb, Adapter: appleGPU.LUID, Format: capture.FormatNV12, Width: w, Height: h})
	}
	power := gpu.AcquireHighPerf(appleGPU)
	return &sourcePipe{
		src: newSynthPattern(frames, p.FPS, release), devices: []gpu.Device{{Adapter: appleGPU}}, params: p, power: power, wrap: annexB,
		info: VideoInfo{Width: w, Height: h, FPS: p.FPS, Codec: p.Codec, Chroma: p.Chroma, Adapter: appleGPU, Power: power.Mode, Source: "synthetic"},
	}, nil
}

// annexB makes VideoToolbox output match the other backends: Annex B start
// codes, with the parameter sets in front of every keyframe.
func annexB(e encode.Encoder) encode.Encoder {
	v, ok := e.(*encvt.Encoder)
	if !ok {
		return e
	}
	v.Keep = true
	return &annexBEncoder{Encoder: v}
}

type annexBEncoder struct {
	*encvt.Encoder
	raw []byte
}

func (e *annexBEncoder) Encode(f capture.Frame, forceKey bool, dst []byte) (encode.Packet, error) {
	pkt, err := e.Encoder.Encode(f, forceKey, e.raw[:0])
	if err != nil {
		return pkt, err
	}
	e.raw = pkt.Data
	sbuf := e.Encoder.LastSample
	e.Encoder.LastSample = 0
	defer apple.CFRelease(sbuf)
	out := dst[:0]
	if pkt.Keyframe {
		if out, err = appendParameterSets(out, sbuf); err != nil {
			return encode.Packet{}, err
		}
	}
	raw := pkt.Data
	for len(raw) >= 4 {
		n := int(binary.BigEndian.Uint32(raw))
		if n > len(raw)-4 {
			return encode.Packet{}, errors.New("session: truncated VideoToolbox NAL unit")
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, raw[4:4+n]...)
		raw = raw[4+n:]
	}
	pkt.Data = out
	return pkt, nil
}

func appendParameterSets(out []byte, sbuf uintptr) ([]byte, error) {
	f := cmSampleBufferGetFormatDescription(sbuf)
	count := uint(1)
	for i := uint(0); i < count; i++ {
		var p uintptr
		var size uint
		var nalLen int32
		if st := cmHEVCParameterSetAtIndex(f, i, &p, &size, &count, &nalLen); st != 0 {
			return out, fmt.Errorf("session: HEVC parameter set %d: %d", i, st)
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, unsafe.Slice((*byte)(cPtr(p)), size)...)
	}
	return out, nil
}

// cPtr views memory owned by CoreMedia; Go's GC never moves it.
func cPtr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func newAudioCapturer() (audio.Capturer, error) { return audio.NewCapturer() }

func newInjector() (input.Injector, error) { return input.NewInjector() }

func newAudioPlayer() (audio.Player, error) { return audio.NewPlayer(audio.PlayerConfig{}) }

func playerQueued(p audio.Player) time.Duration {
	if d, ok := p.(*audio.DevicePlayer); ok {
		return d.Stats().Queued
	}
	return 0
}

// OpenDecodeSink decodes with VideoToolbox and hands each picture straight
// back: the bitstream is decoded on the GPU and no pixel is copied.
func OpenDecodeSink(cfg decode.Config) (Sink, error) {
	d, err := decvt.Open(cfg)
	if err != nil {
		return nil, err
	}
	return &decodeSink{dec: d}, nil
}

func boostVideoThread() (func(), error) { return func() {}, nil }
