package main

import (
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// barsForwarder shows a fw x fh picture in an 800 x 400 view over a
// 1600 x 800 drawable, as on a 2x display, and records what it sends.
func barsForwarder(fw, fh int) (*inputForwarder, *mui.Tester, *[]input.Event) {
	got := &[]input.Event{}
	f := &inputForwarder{send: func(ev input.Event) { *got = append(*got, ev) },
		video: func() (int, int, int, int) { return fw, fh, 1600, 800 }}
	return f, mui.NewTester(f.view, 800, 400), got
}

// TestPointerOverBarsSendsNothing: hovering, clicking and scrolling on
// the bars beside, above or below the picture reach the host not at all.
func TestPointerOverBarsSendsNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		fw, fh int
		x, y   float32
	}{
		// 16:9 in 2:1: the picture is points 44.4..755.6 across.
		{"left pillar", 1920, 1080, 20, 200},
		{"right pillar", 1920, 1080, 780, 200},
		// 4:1 in 2:1: the picture is points 100..300 down.
		{"top bar", 3840, 960, 400, 50},
		{"bottom bar", 3840, 960, 400, 350},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, tt, got := barsForwarder(c.fw, c.fh)
			tt.Move(c.x, c.y)
			tt.Frame()
			tt.Move(c.x+2, c.y+2)
			tt.Frame()
			tt.PressButton(0, c.x, c.y)
			tt.Frame()
			tt.ReleaseButton(0, c.x, c.y)
			tt.Frame()
			tt.PressButton(1, c.x, c.y)
			tt.Frame()
			tt.ReleaseButton(1, c.x, c.y)
			tt.Frame()
			tt.Scroll(c.x, c.y, 0, 120)
			tt.Frame()
			if len(*got) != 0 {
				t.Fatalf("events sent from the bar: %+v", *got)
			}
		})
	}
}

// TestPointerOnPictureMapsEdgeToEdge: on the picture the pointer maps
// across 0..65535, edge to edge.
func TestPointerOnPictureMapsEdgeToEdge(t *testing.T) {
	_, tt, got := barsForwarder(1920, 1080)
	// The picture is 1422 x 800 pixels from x 89: points 44.5..755.5, so
	// the first and last whole points on it are a pixel in from its edges.
	for _, c := range []struct {
		x, y float32
		want input.MouseMoveAbs
	}{
		{45, 0, input.MouseMoveAbs{X: 0, Y: 0}},
		{400, 200, input.MouseMoveAbs{X: 32767, Y: 32767}},
		{755, 399.99, input.MouseMoveAbs{X: 65535, Y: 65535}},
	} {
		*got = nil
		tt.Move(c.x, c.y)
		tt.Frame()
		if len(*got) != 1 || (*got)[0].Type != input.TypeMouseMoveAbs {
			t.Fatalf("move to %v, %v: %+v", c.x, c.y, *got)
		}
		m := (*got)[0].MouseAbs
		if dx, dy := int(m.X)-int(c.want.X), int(m.Y)-int(c.want.Y); dx < -100 || dx > 100 || dy < -100 || dy > 100 {
			t.Fatalf("move to %v, %v: %+v, want %+v", c.x, c.y, m, c.want)
		}
	}
}

// TestDragOntoBarReleases: a press made on the picture and dragged onto a
// bar follows the picture's edge and lets go there; a press made on a bar
// and dragged onto the picture presses nothing.
func TestDragOntoBarReleases(t *testing.T) {
	_, tt, got := barsForwarder(1920, 1080)
	tt.Move(400, 200)
	tt.PressButton(0, 400, 200)
	tt.Frame()
	tt.Move(10, 200)
	tt.Frame()
	tt.ReleaseButton(0, 10, 200)
	tt.Frame()
	want := []input.Event{
		{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: 32767, Y: 32767}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft, Pressed: true}},
		{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: 0, Y: 32767}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft}},
	}
	if len(*got) != len(want) {
		t.Fatalf("drag onto the bar: %+v", *got)
	}
	for i := range want {
		if d := int((*got)[i].MouseAbs.X) - int(want[i].MouseAbs.X); (*got)[i].Type != want[i].Type || (*got)[i].Button != want[i].Button || d < -1 || d > 1 {
			t.Fatalf("drag onto the bar, event %d: %+v, want %+v", i, (*got)[i], want[i])
		}
	}

	*got = nil
	tt.Move(10, 200)
	tt.PressButton(0, 10, 200)
	tt.Frame()
	tt.Move(400, 200)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	tt.Frame()
	for _, ev := range *got {
		if ev.Type == input.TypeMouseButton {
			t.Fatalf("a press begun on the bar reached the host: %+v", *got)
		}
	}
}

// TestOnPicture: the picture's edges, in view points, at the scales the
// client runs at.
func TestOnPicture(t *testing.T) {
	for _, c := range []struct {
		name           string
		x, y, bw, bh   float32
		fw, fh, dw, dh int
		want           bool
	}{
		// 16:9 in a 32:9 Retina window, 2560 x 720 points: the picture is
		// points 640..1920.
		{"retina pillar", 639, 360, 2560, 720, 1920, 1080, 5120, 1440, false},
		{"retina left edge", 640, 360, 2560, 720, 1920, 1080, 5120, 1440, true},
		{"retina right edge", 1920, 360, 2560, 720, 1920, 1080, 5120, 1440, true},
		{"retina right pillar", 1921, 360, 2560, 720, 1920, 1080, 5120, 1440, false},
		// 150% on Windows, 1280 x 720 DIPs over 1920 x 1080 pixels, a 4:3
		// picture: pixels 240..1680, DIPs 160..1120.
		{"windows 150% pillar", 159, 360, 1280, 720, 1440, 1080, 1920, 1080, false},
		{"windows 150% on", 161, 360, 1280, 720, 1440, 1080, 1920, 1080, true},
		{"windows 150% right pillar", 1121, 360, 1280, 720, 1440, 1080, 1920, 1080, false},
		// 32:9 in 16:9: rows 270..810.
		{"top bar", 960, 269, 1920, 1080, 5120, 1440, 1920, 1080, false},
		{"top edge", 960, 270, 1920, 1080, 5120, 1440, 1920, 1080, true},
		{"bottom bar", 960, 811, 1920, 1080, 5120, 1440, 1920, 1080, false},
		{"no picture yet", 5, 5, 800, 600, 0, 0, 0, 0, true},
	} {
		if got := onPicture(c.x, c.y, c.bw, c.bh, c.fw, c.fh, c.dw, c.dh); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
