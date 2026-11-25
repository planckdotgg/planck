package ui

import (
	"image"
	"testing"
	"time"
)

// TestHostingTooltipShowsOnce: the hosting button's status card is its
// only tooltip; the rail's plain name tooltip drew under it too.
func TestHostingTooltipShowsOnce(t *testing.T) {
	f := newFake()
	f.hosting = HostingState{Encoder: "Apple GPU VideoToolbox", Err: "Another app is using UDP port 41000."}
	a, tt := newTester(t, f)
	name := "Host this " + machineWord(a.goos)
	r, ok := tt.Find(name)
	if !ok {
		t.Fatalf("no %q button", name)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	snapshot(t, tt, "hosting-tooltip")
	n := 0
	for _, s := range tt.Texts() {
		if s == name {
			n++
		}
	}
	// The button's own label, and the card's title.
	if n != 2 {
		t.Fatalf("%q shows %d times, want 2 (button and one tooltip): %q", name, n, tt.Texts())
	}
	for _, s := range []string{"off", "Apple GPU VideoToolbox", "Another app is using UDP port 41000."} {
		if !tt.HasText(s) {
			t.Errorf("tooltip misses %q", s)
		}
	}
}

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
