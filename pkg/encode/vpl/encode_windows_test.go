//go:build windows

package vpl

import (
	"fmt"
	"testing"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
)

func hevc(chroma encode.Chroma, src capture.Format) encode.Config {
	return encode.Config{Codec: encode.HEVC, Chroma: chroma, Source: src, Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 30000}
}

func TestEncode(t *testing.T) {
	d := encodetest.Device(t, "UHD")
	for _, cfg := range []encode.Config{
		hevc(encode.NV12, capture.FormatNV12),
		hevc(encode.NV12, capture.FormatBGRA8),
		hevc(encode.P010, capture.FormatP010),
		hevc(encode.P010, capture.FormatRGB10A2),
		hevc(encode.YUV444, capture.FormatBGRA8),
		hevc(encode.YUV444P10, capture.FormatRGB10A2),
	} {
		t.Run(fmt.Sprintf("%s_from_%s", cfg.Chroma, cfg.Source), func(t *testing.T) {
			e, err := Open(d.D3D, d.Info, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 8)
			var keys []int
			r := encodetest.Run{Frames: 240, FPS: cfg.FPS, Paced: true, ForceKey: map[int]bool{100: true}, Each: func(i int, pkt encode.Packet) {
				if pkt.Keyframe {
					keys = append(keys, i)
				}
				if pkt.PTS != int64(i) {
					t.Fatalf("frame %d has PTS %d", i, pkt.PTS)
				}
			}}.Encode(t, e, frames)
			t.Logf("paced: %s; %.0f kbit/s; %.2f allocs/frame; keyframes at %v", r.Latency, r.Kbps, r.AllocsPerFrame, keys)
			if fmt.Sprint(keys) != "[0 100]" {
				t.Fatalf("keyframes at %v, want [0 100]", keys)
			}
			if r.Kbps > float64(cfg.BitrateKbps)*1.25 {
				t.Fatalf("output %.0f kbit/s over the %d CBR target", r.Kbps, cfg.BitrateKbps)
			}
			if r.AllocsPerFrame >= 0.5 {
				t.Fatalf("%.2f allocations per frame", r.AllocsPerFrame)
			}
		})
	}
}

func TestSetBitrate(t *testing.T) {
	d := encodetest.Device(t, "UHD")
	cfg := hevc(encode.NV12, capture.FormatNV12)
	cfg.BitrateKbps = 40000
	e, err := Open(d.D3D, d.Info, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
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
