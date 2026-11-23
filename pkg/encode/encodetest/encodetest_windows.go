//go:build windows

// Package encodetest runs hardware encoders on synthetic GPU frames for the
// encode, decode and player tests.
package encodetest

import (
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/internal/clock"
	"github.com/watzon/planck/internal/synth"
	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/dxgi"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/gpu"
)

// Device opens a D3D11 device on the first adapter whose name contains
// match, or skips the test when there is none.
func Device(t testing.TB, match string) *dxgi.Device {
	t.Helper()
	a, err := dxgi.FindAdapter(match)
	if err != nil {
		t.Skip(err)
	}
	d, err := dxgi.OpenDevice(a.LUID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// GPU is d as a gpu.Device.
func GPU(d *dxgi.Device) gpu.Device {
	return gpu.Device{Adapter: d.Info, Handle: uintptr(unsafe.Pointer(d.D3D.Object))}
}

type layout struct {
	dxgi  uint32
	bytes func(w, h int) int
	pitch func(w int) uint32
	fill  func(dst []byte, w, h, i int)
}

var layouts = map[capture.Format]layout{
	capture.FormatBGRA8: {win.DXGI_FORMAT_B8G8R8A8_UNORM,
		func(w, h int) int { return w * h * 4 }, func(w int) uint32 { return uint32(w * 4) }, synth.BGRA},
	capture.FormatRGB10A2: {win.DXGI_FORMAT_R10G10B10A2_UNORM,
		func(w, h int) int { return w * h * 4 }, func(w int) uint32 { return uint32(w * 4) }, synth.RGB10A2},
	capture.FormatNV12: {win.DXGI_FORMAT_NV12,
		func(w, h int) int { return w * h * 3 / 2 }, func(w int) uint32 { return uint32(w) }, synth.NV12},
	capture.FormatP010: {win.DXGI_FORMAT_P010,
		func(w, h int) int { return w * h * 3 }, func(w int) uint32 { return uint32(w * 2) }, synth.P010},
}

// Frames uploads n distinct synthetic frames to d as textures that live
// until the test ends.
func Frames(t testing.TB, d *dxgi.Device, f capture.Format, w, h, n int) []capture.Frame {
	t.Helper()
	l, ok := layouts[f]
	if !ok {
		t.Fatalf("no synthetic %s frames", f)
	}
	buf := make([]byte, l.bytes(w, h))
	out := make([]capture.Frame, 0, n)
	for i := 0; i < n; i++ {
		l.fill(buf, w, h, i)
		desc := win.Texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: l.dxgi,
			SampleCount: 1, Usage: win.D3D11_USAGE_DEFAULT, BindFlags: win.D3D11_BIND_RENDER_TARGET | win.D3D11_BIND_SHADER_RESOURCE}
		tex, err := d.D3D.CreateTexture2DInit(&desc, buf, l.pitch(w))
		if err != nil {
			t.Fatalf("%s %dx%d texture: %v", f, w, h, err)
		}
		t.Cleanup(tex.Release)
		out = append(out, capture.Frame{Surface: uintptr(unsafe.Pointer(tex.Object)), Adapter: d.Info.LUID, Format: f, Width: w, Height: h})
	}
	return out
}

// Result summarizes an encode run.
type Result struct {
	Latency   bench.Summary // ms from Encode's call to its return
	Kbps      float64       // output bit rate over the measured frames
	Keyframes int
	// AllocsPerFrame is Go heap allocations per measured frame, process wide.
	AllocsPerFrame float64
	Packets        [][]byte // every packet, warm-up included, when Keep is set
}

// Run options.
type Run struct {
	Frames int  // measured frames, after a 30-frame warm-up
	Paced  bool // submit at cfg.FPS instead of back to back
	FPS    int
	// ForceKey lists frame indexes (warm-up included) to force as keyframes;
	// frame 0 always is.
	ForceKey map[int]bool
	Keep     bool
	// Each, when set, runs after each frame with its index and packet.
	Each func(i int, pkt encode.Packet)
}

const warmup = 30

// Encode feeds frames round robin to enc and measures each call.
func (r Run) Encode(t testing.TB, enc encode.Encoder, frames []capture.Frame) Result {
	t.Helper()
	var res Result
	lat := bench.NewSamples(r.Frames)
	dst := make([]byte, 0, 8<<20)
	var bytes int
	var ms0, ms1 runtime.MemStats
	interval := time.Second / time.Duration(max(r.FPS, 1))
	next := clock.Now()
	for i := 0; i < warmup+r.Frames; i++ {
		if i == warmup {
			runtime.ReadMemStats(&ms0)
		}
		if r.Paced {
			for clock.Now() < next {
				if next-clock.Now() > 1500*time.Microsecond {
					time.Sleep(500 * time.Microsecond)
				}
			}
			next += interval
		}
		start := clock.Now()
		pkt, err := enc.Encode(frames[i%len(frames)], i == 0 || r.ForceKey[i], dst)
		end := clock.Now()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		dst = pkt.Data
		if len(pkt.Data) == 0 {
			t.Fatalf("frame %d: empty packet", i)
		}
		if r.Keep {
			res.Packets = append(res.Packets, append([]byte(nil), pkt.Data...))
		}
		if r.Each != nil {
			r.Each(i, pkt)
		}
		if pkt.Keyframe {
			res.Keyframes++
		}
		if i >= warmup {
			lat.Add(clock.Ms(start, end))
			bytes += len(pkt.Data)
		}
	}
	runtime.ReadMemStats(&ms1)
	res.Latency = lat.Summarize()
	res.Kbps = float64(bytes) * 8 / 1000 / (float64(r.Frames) / float64(max(r.FPS, 1)))
	res.AllocsPerFrame = float64(ms1.Mallocs-ms0.Mallocs) / float64(r.Frames)
	return res
}
