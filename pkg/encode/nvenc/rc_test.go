package nvenc

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

func TestSinglePassAtLANRates(t *testing.T) {
	for _, tc := range []struct {
		w, h, fps, kbps int
		want            bool
	}{
		{5120, 1440, 120, 125_000, true},
		{5120, 1440, 120, 212_000, true},
		{1920, 1080, 60, 25_000, true},
		{3024, 1964, 120, 100_000, true},
		{2216, 1440, 120, 20_000, false},
		{0, 0, 0, 20_000, false},
	} {
		cfg := encode.Config{Width: tc.w, Height: tc.h, FPS: tc.fps, BitrateKbps: tc.kbps}
		if got := singlePass(cfg); got != tc.want {
			t.Errorf("%dx%d@%d %d kbps: single pass %v, want %v", tc.w, tc.h, tc.fps, tc.kbps, got, tc.want)
		}
	}
}
