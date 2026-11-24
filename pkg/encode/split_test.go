package encode

import "testing"

func TestSplitFrame(t *testing.T) {
	cases := []struct {
		split     Split
		w, h, fps int
		slices    int
		want      bool
	}{
		{SplitAuto, 1920, 1080, 120, 1, false},
		{SplitAuto, 1920, 1080, 240, 1, true},
		{SplitAuto, 1920, 1080, 200, 0, true},
		{SplitAuto, 1920, 1080, 199, 1, false},
		{SplitAuto, 2560, 1440, 60, 1, true},
		{SplitAuto, 3840, 2160, 120, 1, true},
		{SplitAuto, 2560, 1440, 120, 4, false},
		{SplitAuto, 1280, 720, 360, 1, false},
		{SplitOn, 1280, 720, 60, 4, true},
		{SplitOff, 3840, 2160, 240, 1, false},
	}
	for _, c := range cases {
		cfg := Config{Split: c.split, Width: c.w, Height: c.h, FPS: c.fps, Slices: c.slices}
		if got := cfg.SplitFrame(); got != c.want {
			t.Errorf("%s %dx%d@%d slices %d: SplitFrame %v, want %v", c.split, c.w, c.h, c.fps, c.slices, got, c.want)
		}
	}
}
