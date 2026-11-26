//go:build darwin || windows

package main

import (
	"testing"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/ui"
)

// TestMarkAtlasSplitsFills: the mark's bone and ember fills land in their
// own halves of the atlas, the ember slice in the band the SVG puts it
// (y 186.67 to 238.67 of 48 to 464) and right of the stem, the stem bone.
func TestMarkAtlasSplitsFills(t *testing.T) {
	const w, h = 26, 40
	m := newMarkAtlas(w, h)
	ink := func(left bool, x, y int) int {
		x0 := 1
		if !left {
			x0 = 2 + w
		}
		return int(m.pix[(y+1)*m.aw+x0+x])
	}
	var bone, ember, stray int
	for y := range h {
		for x := range w {
			b, e := ink(true, x, y), ink(false, x, y)
			bone += b
			ember += e
			band := float64(y+1)/h > (186.67-48)/416 && float64(y)/h < (238.67-48)/416
			if e > 64 && (!band || x < w/2) {
				stray++
			}
		}
	}
	if bone == 0 || ember == 0 || stray > 0 {
		t.Fatalf("bone ink %d, ember ink %d, ember outside its slice %d", bone, ember, stray)
	}
	if ink(true, 2, h-2) < 200 || ink(false, 2, h-2) > 0 {
		t.Fatalf("stem foot: bone %d, ember %d", ink(true, 2, h-2), ink(false, 2, h-2))
	}
	if bone < 4*ember {
		t.Fatalf("bone ink %d not well above ember %d", bone, ember)
	}
}

// TestRestRectsScale: the resting shapes move from the view to the
// drawable's pixels, radius too, and the mark lands on whole pixels in
// both fills at its opacity.
func TestRestRectsScale(t *testing.T) {
	rest := &ui.OverlayRest{ViewW: 1440, ViewH: 900,
		Boxes:       []ui.RestBox{{RestRect: ui.RestRect{X: 24, Y: 428, W: 44, H: 44}, Radius: 12, Color: [4]float32{0.1, 0.1, 0.1, 0.2}}},
		Mark:        ui.RestRect{X: 39.5, Y: 440, W: 13, H: 20},
		MarkOpacity: 0.25,
	}
	mw, mh := markSize(rest, 2880, 1800)
	if mw != 26 || mh != 40 {
		t.Fatalf("mark %dx%d at 2x, want 26x40", mw, mh)
	}
	m := newMarkAtlas(mw, mh)
	got := restRects(rest, 2880, 1800, m)
	if len(got) != 3 {
		t.Fatalf("%d rects, want the box and the mark's two fills", len(got))
	}
	if want := (gpusurface.OverlayRect{X: 48, Y: 856, W: 88, H: 88, Radius: 24, Color: [4]float32{0.1, 0.1, 0.1, 0.2}}); got[0] != want {
		t.Fatalf("box %+v, want %+v", got[0], want)
	}
	for i, uv := range [][4]float32{m.bone, m.ember} {
		r := got[1+i]
		if r.X != 79 || r.Y != 880 || r.W != 26 || r.H != 40 || r.UV != uv || r.Color[3] != 0.25 {
			t.Fatalf("mark fill %d: %+v", i, r)
		}
	}
	if restRects(nil, 2880, 1800, m) != nil {
		t.Fatal("no rest drew something")
	}
}

// TestDirectStaysWithRestingOverlay: the overlay resting keeps direct mode
// wanted and drawing more swaps it out; a change of the resting shapes
// alone schedules no swap.
func TestDirectStaysWithRestingOverlay(t *testing.T) {
	v := &videoView{logf: t.Logf}
	v.mode.allowed.Store(true)
	wanted := func() bool { return directWanted(v.mode.allowed.Load(), true, v.mode.overlay.Load()) }
	tab := &ui.OverlayRest{ViewW: 100, ViewH: 100, Boxes: []ui.RestBox{{RestRect: ui.RestRect{W: 8, H: 40}}}}
	v.overlayState(ui.OverlayState{Shown: true})
	if wanted() {
		t.Fatal("menu, toast or hover shown: direct wanted")
	}
	v.overlayState(ui.OverlayState{Shown: true, Rest: tab})
	if !wanted() || v.mode.rest.Load() != tab {
		t.Fatalf("button at rest: direct wanted %v, rest %v", wanted(), v.mode.rest.Load())
	}
	moved := &ui.OverlayRest{ViewW: 100, ViewH: 100, Boxes: []ui.RestBox{{RestRect: ui.RestRect{Y: 10, W: 8, H: 40}}}}
	v.overlayState(ui.OverlayState{Shown: true, Rest: moved})
	if !wanted() || v.mode.rest.Load() != moved {
		t.Fatal("moved tab: direct dropped or shapes kept")
	}
	v.overlayState(ui.OverlayState{Shown: true})
	if wanted() || v.mode.rest.Load() != moved {
		t.Fatal("overlay live again: direct still wanted, or the shapes went before the swap")
	}
	v.overlayState(ui.OverlayState{})
	if !wanted() || v.mode.rest.Load() != nil {
		t.Fatal("overlay hidden: direct not wanted, or shapes left")
	}
	if v.mode.gen != 4 {
		t.Fatalf("%d swaps scheduled, want 4: live, rest, live, hidden, and none for the tab moving", v.mode.gen)
	}
}
