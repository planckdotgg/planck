//go:build windows

package d3d11

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"unsafe"

	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/internal/synth"
	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/dxgi"
	"github.com/watzon/planck/pkg/decode"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
	"github.com/watzon/planck/pkg/encode/nvenc"
)

const w, h = 1920, 1080

// stream encodes n synthetic frames with NVENC and returns the packets.
func stream(t *testing.T, codec encode.Codec, chroma encode.Chroma, src capture.Format, n int) [][]byte {
	t.Helper()
	nv := encodetest.Device(t, "NVIDIA")
	cfg := encode.Config{Codec: codec, Chroma: chroma, Source: src, Width: w, Height: h, FPS: 120, BitrateKbps: 150000}
	enc, err := nvenc.Open(nv.D3D, nv.Info, cfg, nvenc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frames := encodetest.Frames(t, nv, src, w, h, n)
	var out [][]byte
	for i := 0; i < n; i++ {
		pkt, err := enc.Encode(frames[i], i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pkt.Data)
	}
	return out
}

// lumaPSNR reads the decoded picture's luma back through a staging texture
// and compares it with the synthetic source frame i.
func lumaPSNR(t *testing.T, d *dxgi.Device, f decode.Frame, i int) (float64, float64) {
	t.Helper()
	dxgiFormat, bytes := uint32(win.DXGI_FORMAT_NV12), 1
	if f.Format == capture.FormatP010 {
		dxgiFormat, bytes = win.DXGI_FORMAT_P010, 2
	}
	staging, err := d.D3D.CreateTexture2D(&win.Texture2DDesc{Width: uint32(f.Width), Height: uint32(f.Height), MipLevels: 1, ArraySize: 1,
		Format: dxgiFormat, SampleCount: 1, Usage: win.D3D11_USAGE_STAGING, CPUAccessFlags: win.D3D11_CPU_ACCESS_READ})
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Release()
	box := win.Box{Right: uint32(f.Width), Bottom: uint32(f.Height), Back: 1}
	obj(uintptr(unsafe.Pointer(d.Context.Object))).call(46, uintptr(unsafe.Pointer(staging.Object)), 0, 0, 0, 0,
		f.Surface, uintptr(f.Slice), uintptr(unsafe.Pointer(&box)))
	var m win.MappedSubresource
	if err := d.Context.Map(staging.Object, win.D3D11_MAP_READ, &m); err != nil {
		t.Fatal(err)
	}
	defer d.Context.Unmap(staging.Object)
	psnr := func(frame int) float64 {
		ref := make([]byte, w*h*3/2)
		synth.NV12(ref, w, h, frame)
		var se float64
		for y := 0; y < h; y++ {
			row := unsafe.Slice((*byte)(win.Ptr(m.Data+uintptr(y)*uintptr(m.RowPitch))), w*bytes)
			for x := 0; x < w; x++ {
				got := float64(row[x])
				if bytes == 2 {
					got = float64(uint16(row[2*x])|uint16(row[2*x+1])<<8) / 256 // 10 bits in the top of 16, as 8-bit
				}
				diff := got - float64(ref[y*w+x])
				se += diff * diff
			}
		}
		return 10 * math.Log10(255*255/math.Max(se/float64(w*h), 1e-9))
	}
	// The source scrolls 7 rows a frame: the neighbor frame is the control.
	return psnr(i), psnr(i + 1)
}

func TestDecode(t *testing.T) {
	cases := []struct {
		codec  encode.Codec
		chroma encode.Chroma
		src    capture.Format
		out    capture.Format
	}{
		{encode.HEVC, encode.NV12, capture.FormatNV12, capture.FormatNV12},
		{encode.HEVC, encode.P010, capture.FormatP010, capture.FormatP010},
		{encode.AV1, encode.NV12, capture.FormatNV12, capture.FormatNV12},
		{encode.AV1, encode.P010, capture.FormatP010, capture.FormatP010},
	}
	for _, c := range cases {
		packets := stream(t, c.codec, c.chroma, c.src, 120)
		for _, gpuName := range []string{"Radeon", "NVIDIA"} {
			t.Run(fmt.Sprintf("%s_%s_on_%s", c.codec, c.chroma, gpuName), func(t *testing.T) {
				d := encodetest.Device(t, gpuName)
				dec, err := Open(encodetest.GPU(d), decode.Config{Codec: c.codec, Width: w, Height: h, FPS: 120})
				if errors.Is(err, decode.ErrUnsupported) {
					t.Skip(err)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer dec.Close()
				lat := bench.NewSamples(len(packets))
				var psnr, control []float64
				decoded := 0
				for i, p := range packets {
					f, err := dec.Decode(p, int64(i))
					if errors.Is(err, decode.ErrNoFrame) {
						continue
					}
					if err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
					decoded++
					if f.Format != c.out || f.Width != w || f.Height != h || f.Surface == 0 {
						t.Fatalf("frame %d: %s %dx%d surface=%#x, want %s %dx%d", i, f.Format, f.Width, f.Height, f.Surface, c.out, w, h)
					}
					if f.PTS != int64(i) {
						t.Fatalf("frame %d came out with PTS %d: the decoder fell behind", i, f.PTS)
					}
					if i >= 10 {
						lat.Add(float64(f.Latency) / 1e6)
					}
					// Not frame 0: a one-frame VBV starves the opening IDR.
					if i%30 == 0 && i > 0 {
						p, c := lumaPSNR(t, d, f, i)
						psnr, control = append(psnr, p), append(control, c)
					}
					dec.Release(f)
				}
				t.Logf("%s on %s: %d/%d pictures, %d dropped late; decode %s; luma PSNR %.1f dB (next source frame: %.1f dB)",
					dec.Name(), d.Info.Name, decoded, len(packets), dec.Dropped(), lat.Summarize(), psnr, control)
				// A picture later than Decode's wait is released unshown when
				// the next one is ready; allow that for 1 in 100.
				if decoded+dec.Dropped() != len(packets) || dec.Dropped() > len(packets)/100 {
					t.Fatalf("decoded %d of %d, %d dropped late", decoded, len(packets), dec.Dropped())
				}
				for i := range psnr {
					if psnr[i] < 35 || psnr[i] < control[i]+10 {
						t.Fatalf("luma PSNR %.1f dB against its source, %.1f against the next: the picture is not the source", psnr[i], control[i])
					}
				}
			})
		}
	}
}

// TestNoSizeHint opens without a size, so the output type is picked when
// the first access unit reveals the stream (a stream change).
func TestNoSizeHint(t *testing.T) {
	packets := stream(t, encode.HEVC, encode.P010, capture.FormatP010, 10)
	d := encodetest.Device(t, "Radeon")
	dec, err := Open(encodetest.GPU(d), decode.Config{Codec: encode.HEVC})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	for i, p := range packets {
		f, err := dec.Decode(p, int64(i))
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Format != capture.FormatP010 || f.Width != w || f.Height != h {
			t.Fatalf("frame %d: %s %dx%d", i, f.Format, f.Width, f.Height)
		}
		dec.Release(f)
	}
}

func TestRejectsGarbage(t *testing.T) {
	d := encodetest.Device(t, "Radeon")
	dec, err := Open(encodetest.GPU(d), decode.Config{Codec: encode.HEVC})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if _, err := dec.Decode([]byte{0, 0, 0, 1, 0x40, 0x01, 0xff, 0xff}, 0); err == nil {
		t.Fatal("decoded a picture from a truncated VPS")
	}
	if _, err := Open(encodetest.GPU(d), decode.Config{Codec: encode.H264}); !errors.Is(err, decode.ErrUnsupported) {
		t.Fatalf("H.264: got %v, want ErrUnsupported", err)
	}
}
