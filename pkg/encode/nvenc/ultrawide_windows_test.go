package nvenc

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
)

// NV_ENC_RC_PARAMS bit field at rcFlags (nvEncodeAPI.h), for the audit.
const (
	rcFlags         = 36
	rcLookaheadDep  = 82
	rcBitAQ         = 3
	rcBitLookahead  = 5
	rcBitTemporalAQ = 8
	rcBitZeroDelay  = 9
)

// TestUltrawide120 measures the live session's stream, 5120x1440 HEVC
// Main10 from an RGB10A2 desktop at 120 fps paced, encoded whole with and
// without the two-engine split, and as sub-frame slices with and without
// it, and logs the rate control flags the P1 ultra-low-latency preset
// leaves set. PLANCK_LIVE_ULTRAWIDE=1 runs it.
func TestUltrawide120(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_ULTRAWIDE") != "1" {
		t.Skip("set PLANCK_LIVE_ULTRAWIDE=1 to encode 5120x1440 at 120 fps on the NVIDIA GPU")
	}
	d := encodetest.Device(t, "NVIDIA")
	base := encode.Config{Codec: encode.HEVC, Chroma: encode.P010, Source: capture.FormatRGB10A2,
		Width: 5120, Height: 1440, FPS: 120}
	frames := encodetest.Frames(t, d, base.Source, base.Width, base.Height, 8)
	type variant struct {
		name     string
		slices   int
		subFrame bool
		split    uint32
	}
	variants := []variant{
		{"whole, one engine", 1, false, splitDisabled},
		{"whole, two-engine split", 1, false, splitTwoForced},
		{"4 slices sub-frame, one engine", 4, true, splitDisabled},
		{"4 slices sub-frame, two-engine split", 4, true, splitTwoForced},
		{"8 slices sub-frame, two-engine split", 8, true, splitTwoForced},
	}
	for _, kbps := range []int{212_000, 125_000} {
		for _, v := range variants {
			cfg := base
			cfg.BitrateKbps, cfg.Slices, cfg.SubFrame = kbps, v.slices, v.subFrame
			e, err := Open(d.D3D, d.Info, cfg, Options{SplitMode: v.split})
			if err != nil {
				t.Logf("%3d Mbps %-38s cannot open: %v", kbps/1000, v.name, err)
				continue
			}
			if kbps == 212_000 && v.slices == 1 && v.split == splitTwoForced {
				rc := e.config[cfgRC:]
				flags := get32(rc, rcFlags)
				t.Logf("preset P1 + ultra-low latency: rc mode %d, multipass %d, AQ %d, temporal AQ %d, lookahead %d (depth %d), zero reorder delay %d, GOP %d, P interval %d, VBV %d bits (%.2f frames)",
					get32(rc, rcMode), get32(rc, rcMultiPass), flags>>rcBitAQ&1, flags>>rcBitTemporalAQ&1, flags>>rcBitLookahead&1,
					uint16(get32(rc, rcLookaheadDep)), flags>>rcBitZeroDelay&1, get32(e.config, cfgGopLength), get32(e.config, cfgFrameIntervalP),
					get32(rc, rcVBVSize), float64(get32(rc, rcVBVSize))*float64(cfg.FPS)/float64(cfg.BitrateKbps*1000))
			}
			var first []float64
			r := encodetest.Run{Frames: 600, FPS: cfg.FPS, Paced: true, Each: func(int, encode.Packet) {
				if v.subFrame {
					first = append(first, float64(e.FirstSlice())/float64(time.Millisecond))
				}
			}}.Encode(t, e, frames)
			e.Close()
			fs := "-"
			if len(first) > 0 {
				slices.Sort(first)
				fs = time.Duration(first[len(first)/2] * float64(time.Millisecond)).Round(time.Microsecond).String()
			}
			t.Logf("%3d Mbps %-38s encode p50 %.2f p99 %.2f max %.2f ms; first slice p50 %s; %.0f kbit/s; %.2f allocs/frame",
				kbps/1000, v.name, r.Latency.P50, r.Latency.P99, r.Latency.Max, fs, r.Kbps, r.AllocsPerFrame)
		}
	}
}
