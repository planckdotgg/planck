package main

import (
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// TestDragSendsMovesBetweenPressAndRelease: a drag on the picture, as to
// draw a selection box on the host's desktop, sends the move to where it
// starts, the press, a move for every frame the pointer moved, and the
// release where it ends.
func TestDragSendsMovesBetweenPressAndRelease(t *testing.T) {
	// A 2x display: an 800 x 400 point view over a 1600 x 800 drawable,
	// the 32:9 picture letterboxed to rows 175..625.
	var got []input.Event
	f := &inputForwarder{send: func(ev input.Event) { got = append(got, ev) },
		video: func() (int, int, int, int) { return 5120, 1440, 1600, 800 }}
	tt := mui.NewTester(f.view, 800, 400)
	tt.Move(100, 150)
	tt.Frame()
	got = nil
	tt.PressButton(0, 100, 150)
	tt.Frame()
	for _, p := range [][2]float32{{150, 170}, {200, 190}, {300, 220}} {
		tt.Move(p[0], p[1])
		tt.Frame()
	}
	tt.ReleaseButton(0, 300, 220)
	tt.Frame()

	var kinds []string
	var xs []uint16
	for _, ev := range got {
		switch {
		case ev.Type == input.TypeMouseMoveAbs:
			kinds, xs = append(kinds, "move"), append(xs, ev.MouseAbs.X)
		case ev.Type == input.TypeMouseButton && ev.Button.Button == input.ButtonLeft && ev.Button.Pressed:
			kinds = append(kinds, "down")
		case ev.Type == input.TypeMouseButton && ev.Button.Button == input.ButtonLeft:
			kinds = append(kinds, "up")
		default:
			kinds = append(kinds, "other")
		}
	}
	want := []string{"down", "move", "move", "move", "up"}
	if len(kinds) != len(want) {
		t.Fatalf("drag sent %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("drag sent %v, want %v", kinds, want)
		}
	}
	for i := 1; i < len(xs); i++ {
		if xs[i] <= xs[i-1] {
			t.Fatalf("drag moves %v do not follow the pointer rightwards", xs)
		}
	}
	// The press frame sends the move first when the pointer moved since.
	got = nil
	tt.Move(400, 200)
	tt.PressButton(0, 400, 200)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	tt.Frame()
	if len(got) != 3 || got[0].Type != input.TypeMouseMoveAbs || got[1].Type != input.TypeMouseButton || !got[1].Button.Pressed {
		t.Fatalf("press after a move: %+v, want the move, then the press", got)
	}
}

// TestPointerMapsFromAnOffsetView: a stream view that does not start at
// the window's corner, as under a 28 point title bar, maps the pointer
// from its own corner: on a 2x display, with the picture letterboxed.
func TestPointerMapsFromAnOffsetView(t *testing.T) {
	var got []input.Event
	// The view is 800 x 400 points under a 28 point bar, over a 1600 x 800
	// drawable; 32:9 letterboxed is rows 175..625, points 87.5..312.5.
	f := &inputForwarder{send: func(ev input.Event) { got = append(got, ev) },
		video: func() (int, int, int, int) { return 5120, 1440, 1600, 800 }}
	tt := mui.NewTester(func(c *mui.Context) {
		mui.Column(c).Fill().Children(func() {
			mui.Box(c).Height(28).FillWidth()
			mui.Box(c).Grow(1).FillWidth().Children(func() { f.view(c) })
		})
	}, 800, 428)
	for _, c := range []struct {
		x, y float32
		want [2]int
		on   bool
	}{
		{0, 28 + 88, [2]int{0, 0}, true},
		{400, 28 + 200, [2]int{32767, 32767}, true},
		{799.99, 28 + 312, [2]int{65535, 65535}, true},
		{400, 28 + 80, [2]int{}, false},
		{400, 20, [2]int{}, false},
	} {
		got = nil
		tt.Move(c.x, c.y)
		tt.Frame()
		if !c.on {
			if len(got) != 0 {
				t.Fatalf("window point %v, %v, off the picture, sent %+v", c.x, c.y, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("window point %v, %v: %+v", c.x, c.y, got)
		}
		m := got[0].MouseAbs
		if dx, dy := int(m.X)-c.want[0], int(m.Y)-c.want[1]; dx < -300 || dx > 300 || dy < -300 || dy > 300 {
			t.Fatalf("window point %v, %v maps to %d, %d, want %v", c.x, c.y, m.X, m.Y, c.want)
		}
	}
}
