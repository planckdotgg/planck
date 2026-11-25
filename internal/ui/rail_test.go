package ui

import (
	"image"
	"testing"
)

// inkSpan is the first and last row inside r that differ from bg.
func inkSpan(img *image.RGBA, r image.Rectangle, bg [4]uint8) (top, bot int, ok bool) {
	top, bot = -1, -1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := img.RGBAAt(x, y)
			if [4]uint8{p.R, p.G, p.B, p.A} != bg {
				if top < 0 {
					top = y
				}
				bot = y
				break
			}
		}
	}
	return top, bot, top >= 0
}

func TestRailLogoAlignsWithPageTitle(t *testing.T) {
	a, tt := newTester(t, newFake())
	screens := []struct {
		screen Screen
		sub    string
		name   string
	}{
		{ScreenHosts, "Searching your network and tailnet", "hosts"},
		{ScreenStatistics, "Nothing is streaming right now.", "statistics"},
		{ScreenSettings, "How this " + machineWord(a.goos) + " opens streams and what it shares with them.", "settings"},
	}
	for _, s := range screens {
		a.screen = s.screen
		a.Notify()
		tt.Frame()
		snapshot(t, tt, "rail-"+s.name)
		sub, ok := tt.Find(s.sub)
		if !ok {
			t.Fatalf("%s: subtitle %q not found", s.name, s.sub)
		}
		img := tt.Image()
		pg := img.RGBAAt(int(sub.X)-8, 2)
		bg := [4]uint8{pg.R, pg.G, pg.B, pg.A}
		x0 := int(sub.X)
		tTop, tBot, ok := inkSpan(img, image.Rect(x0, 0, x0+14, int(sub.Y)), bg)
		if !ok {
			t.Fatalf("%s: no title ink above the subtitle", s.name)
		}
		rl := img.RGBAAt(4, 2)
		lTop, lBot, ok := inkSpan(img, image.Rect(20, 0, 68, 110), [4]uint8{rl.R, rl.G, rl.B, rl.A})
		if !ok {
			t.Fatalf("%s: no logo ink", s.name)
		}
		tc, lc := float64(tTop+tBot)/2, float64(lTop+lBot)/2
		t.Logf("%s: title ink %d..%d (centre %.1f), logo ink %d..%d (centre %.1f)", s.name, tTop, tBot, tc, lTop, lBot, lc)
		if d := tc - lc; d > 1 || d < -1 {
			t.Errorf("%s: logo centre %.1f is %.1f px from title centre %.1f", s.name, lc, d, tc)
		}
	}
}
