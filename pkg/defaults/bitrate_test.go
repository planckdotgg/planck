package defaults

import "testing"

// TestHEVCTargetCurve: the auto LAN target keeps 0.20 bits a pixel up to
// 1440p60 and grows as pixel rate^0.75 past it, so 4K and 5120x1440 at
// 120 fps stay near 125-140 Mbps instead of 177-199.
func TestHEVCTargetCurve(t *testing.T) {
	for _, tc := range []struct {
		w, h, fps   int
		old, lo, hi int // Mbps: the flat 0.20 bpp target, and the range wanted
	}{
		{1920, 1080, 60, 24, 24, 25},
		{2560, 1440, 60, 44, 44, 45},
		{2560, 1440, 120, 88, 72, 77},
		{3840, 2160, 120, 199, 130, 140},
		{5120, 1440, 120, 176, 120, 130},
	} {
		rate := int64(tc.w) * int64(tc.h) * int64(tc.fps)
		got := hevcTargetKbps(rate)
		t.Logf("%dx%d@%d: %.1f Mbps (%.3f bpp; flat 0.20 bpp: %d Mbps)", tc.w, tc.h, tc.fps, float64(got)/1000, float64(got)*1000/float64(rate), tc.old)
		if got < tc.lo*1000 || got > tc.hi*1000 {
			t.Errorf("%dx%d@%d: %d kbps, want %d to %d Mbps", tc.w, tc.h, tc.fps, got, tc.lo, tc.hi)
		}
	}
}

// TestBitrateOverrideKeepsLANCeiling: a set bitrate still reaches the LAN
// ceiling of 300 Mbps, whatever the auto target.
func TestBitrateOverrideKeepsLANCeiling(t *testing.T) {
	for _, mbps := range []int{50, 300} {
		p := Resolve(bigboi(), m5Pro(), lan, UserOverrides{Codec: "hevc", BitrateMbps: mbps})
		if p.BitrateKbps != mbps*1000 || p.MaxBitrateKbps != mbps*1000 {
			t.Fatalf("override %d Mbps: bitrate %d, max %d", mbps, p.BitrateKbps, p.MaxBitrateKbps)
		}
	}
	p := Resolve(bigboi(), m5Pro(), lan, UserOverrides{Codec: "hevc"})
	if p.MaxBitrateKbps != lanCeilingKbps {
		t.Fatalf("auto: max %d, want the %d LAN ceiling for the rate controller", p.MaxBitrateKbps, lanCeilingKbps)
	}
}
