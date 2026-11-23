//go:build windows

package amf

import (
	"errors"
	"fmt"
	"testing"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/dxgi"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
)

func hevc(chroma encode.Chroma, src capture.Format) encode.Config {
	return encode.Config{Codec: encode.HEVC, Chroma: chroma, Source: src, Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 30000}
}

func open(t *testing.T, d *dxgi.Device, cfg encode.Config) *Encoder {
	t.Helper()
	e, err := Open(d.D3D, d.Info, cfg)
	if err != nil {
		t.Fatalf("open %s %s from %s: %v", cfg.Codec, cfg.Chroma, cfg.Source, err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestEncode(t *testing.T) {
	d := encodetest.Device(t, "Radeon")
	for _, cfg := range []encode.Config{
		hevc(encode.NV12, capture.FormatBGRA8),
		hevc(encode.P010, capture.FormatP010),
		func() encode.Config { c := hevc(encode.NV12, capture.FormatBGRA8); c.Slices = 4; return c }(),
	} {
		t.Run(fmt.Sprintf("%s_from_%s_%d_slices", cfg.Chroma, cfg.Source, max(cfg.Slices, 1)), func(t *testing.T) {
			e := open(t, d, cfg)
			frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
			var keys []int
			for _, paced := range []bool{false, true} {
				keys = keys[:0]
				r := encodetest.Run{Frames: 240, FPS: cfg.FPS, Paced: paced, ForceKey: map[int]bool{100: true}, Each: func(i int, pkt encode.Packet) {
					if pkt.Keyframe {
						keys = append(keys, i)
					}
				}}.Encode(t, e, frames)
				t.Logf("paced=%v: %s; %.0f kbit/s; %.2f allocs/frame; keyframes at %v", paced, r.Latency, r.Kbps, r.AllocsPerFrame, keys)
				if r.Kbps > float64(cfg.BitrateKbps)*1.25 {
					t.Fatalf("output %.0f kbit/s over the %d CBR target", r.Kbps, cfg.BitrateKbps)
				}
				if r.AllocsPerFrame >= 0.5 {
					t.Fatalf("%.2f allocations per frame", r.AllocsPerFrame)
				}
			}
			if fmt.Sprint(keys) != "[0 100]" {
				t.Fatalf("keyframes at %v, want [0 100]", keys)
			}
		})
	}
}

func TestSetBitrate(t *testing.T) {
	d := encodetest.Device(t, "Radeon")
	cfg := hevc(encode.NV12, capture.FormatBGRA8)
	cfg.BitrateKbps = 40000
	e := open(t, d, cfg)
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
	high := encodetest.Run{Frames: 240, FPS: cfg.FPS}.Encode(t, e, frames)
	if err := e.SetBitrate(8000); err != nil {
		t.Fatal(err)
	}
	low := encodetest.Run{Frames: 240, FPS: cfg.FPS}.Encode(t, e, frames)
	t.Logf("40000 kbit/s target: %.0f out; 8000 target: %.0f out", high.Kbps, low.Kbps)
	if low.Kbps > 8000*1.25 || low.Kbps > high.Kbps/2 {
		t.Fatalf("SetBitrate did not take: %.0f then %.0f kbit/s", high.Kbps, low.Kbps)
	}
}

func TestRefuses444(t *testing.T) {
	d := encodetest.Device(t, "Radeon")
	if _, err := Open(d.D3D, d.Info, hevc(encode.YUV444, capture.FormatBGRA8)); !errors.Is(err, encode.ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}
