//go:build linux && !android && (amd64 || arm64)

package nvenc

import (
	"math"
	"runtime"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/codec/hevc"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/nvdec"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func adapter(t *testing.T) gpu.Adapter {
	t.Helper()
	all, err := Adapters()
	if err != nil {
		t.Skip(err)
	}
	t.Logf("CUDA devices: %+v", all)
	return all[0]
}

// memFrames renders n pattern frames in memory as source fourcc.
func memFrames(fourcc uint32, w, h, n int) ([]*capture.DMABuf, []capture.Frame, [][]byte) {
	var bufs []*capture.DMABuf
	var frames []capture.Frame
	var lumas [][]byte
	for i := range n {
		switch fourcc {
		case capture.FourccNV12:
			b := make([]byte, w*h*3/2)
			synth.NV12(b, w, h, i*8)
			d := capture.MemoryDMABuf(fourcc, w, h, [][]byte{b[:w*h], b[w*h:]}, []int{w, w})
			bufs, lumas = append(bufs, d), append(lumas, b[:w*h])
			frames = append(frames, capture.Frame{Surface: d.Handle(), Format: capture.FormatNV12, Width: w, Height: h})
		default:
			b := make([]byte, w*h*4)
			synth.BGRA(b, w, h, i*8)
			d := capture.MemoryDMABuf(fourcc, w, h, [][]byte{b}, []int{w * 4})
			bufs = append(bufs, d)
			frames = append(frames, capture.Frame{Surface: d.Handle(), Format: capture.FormatBGRA8, Width: w, Height: h})
		}
	}
	return bufs, frames, lumas
}

func openLinux(t *testing.T, cfg encode.Config) *Encoder {
	t.Helper()
	e, err := Open(adapter(t), cfg, Options{})
	if err != nil {
		t.Fatalf("open %s %s from %s: %v", cfg.Codec, cfg.Chroma, cfg.Source, err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// TestLinuxKeyframeCarriesParameterSets encodes BGRA and NV12 frames from
// memory and checks the first picture is an IDR with VPS, SPS and PPS in
// front, at 8 and 10 bits.
func TestLinuxKeyframeCarriesParameterSets(t *testing.T) {
	for _, c := range []struct {
		fourcc uint32
		src    capture.Format
		chroma encode.Chroma
	}{
		{capture.FourccXRGB8888, capture.FormatBGRA8, encode.NV12},
		{capture.FourccXRGB8888, capture.FormatBGRA8, encode.P010},
		{capture.FourccNV12, capture.FormatNV12, encode.NV12},
	} {
		t.Run(c.src.String()+"-"+c.chroma.String(), func(t *testing.T) {
			cfg := encode.Config{Codec: encode.HEVC, Chroma: c.chroma, Source: c.src, Width: 1280, Height: 720, FPS: 60, BitrateKbps: 20000}
			e := openLinux(t, cfg)
			bufs, frames, _ := memFrames(c.fourcc, 1280, 720, 4)
			var types []int
			for i, f := range frames {
				pkt, err := e.Encode(f, i == 0, nil)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if i == 0 {
					if !pkt.Keyframe {
						t.Fatal("first picture is no keyframe")
					}
					for _, n := range hevc.NALUnits(pkt.Data) {
						types = append(types, hevc.NALType(n))
					}
				} else if pkt.Keyframe || len(pkt.Data) == 0 {
					t.Fatalf("frame %d: keyframe=%v %d bytes", i, pkt.Keyframe, len(pkt.Data))
				}
			}
			for _, want := range []int{hevc.NalVPS, hevc.NalSPS, hevc.NalPPS, hevc.NalIDRWRADL} {
				if !slices.Contains(types, want) && !(want == hevc.NalIDRWRADL && slices.Contains(types, hevc.NalIDRNLP)) {
					t.Fatalf("keyframe NAL types %v lack %d", types, want)
				}
			}
			runtime.KeepAlive(bufs)
		})
	}
}

// TestLinuxRoundTripThroughNVDEC encodes NV12 frames with NVENC, decodes
// them with NVDEC and compares the luma with the source.
func TestLinuxRoundTripThroughNVDEC(t *testing.T) {
	const w, h = 1280, 720
	cfg := encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: capture.FormatNV12, Width: w, Height: h, FPS: 60, BitrateKbps: 40000}
	e := openLinux(t, cfg)
	if err := nvdec.Available(); err != nil {
		t.Skip(err)
	}
	d, err := nvdec.Open(decode.Config{Codec: encode.HEVC, Width: w, Height: h})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	bufs, frames, lumas := memFrames(capture.FourccNV12, w, h, 8)
	for i, f := range frames {
		pkt, err := e.Encode(f, i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		pic, err := d.Decode(pkt.Data, int64(i))
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		out := capture.DMABufOf(pic.Surface)
		var se float64
		for y := range h {
			row := unsafe.Slice((*byte)(unsafe.Add(out.CPU[0], y*int(out.Pitches[0]))), w)
			for x := range w {
				diff := float64(row[x]) - float64(lumas[i][y*w+x])
				se += diff * diff
			}
		}
		d.Release(pic)
		psnr := 10 * math.Log10(255*255/(se/float64(w*h)))
		if psnr < 30 {
			t.Fatalf("frame %d: luma PSNR %.1f dB", i, psnr)
		}
		if i == len(frames)-1 {
			t.Logf("frame %d: %d bytes, luma PSNR %.1f dB", i, len(pkt.Data), psnr)
		}
	}
	runtime.KeepAlive(bufs)
}

// TestLinuxPaced1080p120 measures upload plus encode at 1080p120, paced,
// from BGRA frames in memory.
func TestLinuxPaced1080p120(t *testing.T) {
	const w, h, fps = 1920, 1080, 120
	cfg := encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: capture.FormatBGRA8, Width: w, Height: h, FPS: fps, BitrateKbps: 50000}
	e := openLinux(t, cfg)
	bufs, frames, _ := memFrames(capture.FourccXRGB8888, w, h, 8)
	var took []time.Duration
	next := time.Now()
	for i := range 240 {
		time.Sleep(time.Until(next))
		next = next.Add(time.Second / fps)
		start := clock.Now()
		if _, err := e.Encode(frames[i%len(frames)], i == 0, nil); err != nil {
			t.Fatal(err)
		}
		took = append(took, clock.Now()-start)
	}
	slices.Sort(took)
	t.Logf("1080p120 BGRA upload+encode: p50 %v p99 %v max %v", took[len(took)/2], took[len(took)*99/100], took[len(took)-1])
	runtime.KeepAlive(bufs)
}
