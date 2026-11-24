package main

import (
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// TestInputForwarder drives the stream view through MyGo's tester, with
// no window and no OS events: pointer moves and buttons, keys with their
// modifiers, and the HUD hotkey staying with the client.
func TestInputForwarder(t *testing.T) {
	var got []input.Event
	f := &inputForwarder{send: func(ev input.Event) { got = append(got, ev) }}
	tt := mui.NewTester(f.view, 800, 600)
	take := func() []input.Event { out := got; got = nil; return out }

	tt.Move(400, 150)
	tt.Frame()
	if ev := take(); len(ev) != 1 || ev[0].Type != input.TypeMouseMoveAbs || ev[0].MouseAbs.X != 32767 || ev[0].MouseAbs.Y != 16383 {
		t.Fatalf("move: %+v", ev)
	}
	tt.Frame()
	if ev := take(); len(ev) != 0 {
		t.Fatalf("no move sent again: %+v", ev)
	}

	tt.PressButton(1, 400, 150)
	tt.Frame()
	tt.ReleaseButton(1, 400, 150)
	tt.Frame()
	want := []input.Event{
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonRight, Pressed: true}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonRight}},
	}
	if ev := slices.DeleteFunc(take(), func(e input.Event) bool { return e.Type == input.TypeMouseMoveAbs }); !slices.Equal(ev, want) {
		t.Fatalf("right click: %+v", ev)
	}

	f.keyboard(nil, mui.Shift)
	f.keyboard([]mui.Key{mui.KeyA}, mui.Shift)
	f.keyboard(nil, mui.Shift)
	f.keyboard(nil, 0)
	want = []input.Event{
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftShift, Pressed: true, Modifiers: input.ModLeftShift}},
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA, Pressed: true, Modifiers: input.ModLeftShift}},
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA, Modifiers: input.ModLeftShift}},
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftShift}},
	}
	if ev := take(); !slices.Equal(ev, want) {
		t.Fatalf("shift+A:\n got %v\nwant %v", keys(ev), keys(want))
	}

	f.keyboard([]mui.Key{mui.KeyH}, mui.Ctrl|mui.Alt)
	f.keyboard(nil, mui.Ctrl|mui.Alt)
	f.keyboard(nil, 0)
	for _, ev := range take() {
		if ev.Key.Usage == input.KeyH {
			t.Fatalf("HUD hotkey forwarded: %+v", ev)
		}
	}
}

func TestHIDUsageTable(t *testing.T) {
	for k, want := range map[mui.Key]uint16{mui.KeyZ: 0x1d, mui.Key1: 0x1e, mui.Key0: 0x27, mui.KeyF12: 0x45, mui.KeyUp: 0x52, mui.KeyBackquote: 0x35} {
		if got := hidUsage[k]; got != want {
			t.Errorf("key %v: usage %#x, want %#x", k, got, want)
		}
	}
}

func keys(evs []input.Event) []input.KeyEvent {
	var out []input.KeyEvent
	for _, e := range evs {
		out = append(out, e.Key)
	}
	return out
}

func TestMapPointer(t *testing.T) {
	type pt struct{ x, y float32 }
	for _, c := range []struct {
		name           string
		bw, bh         float32
		fw, fh, dw, dh int
		at             pt
		want           [2]int
	}{
		{"1:1 centre", 1920, 1080, 1920, 1080, 1920, 1080, pt{960, 540}, [2]int{32767, 32767}},
		{"1:1 corner", 1920, 1080, 1920, 1080, 1920, 1080, pt{0, 0}, [2]int{0, 0}},
		// 16:9 in a 32:9 Retina window, 2560x720 points: the picture is
		// points 640..1920 wide.
		{"pillarbox left edge", 2560, 720, 1920, 1080, 5120, 1440, pt{640, 360}, [2]int{0, 32767}},
		{"pillarbox centre", 2560, 720, 1920, 1080, 5120, 1440, pt{1280, 0}, [2]int{32767, 0}},
		{"pillarbox right edge", 2560, 720, 1920, 1080, 5120, 1440, pt{1920, 719.99}, [2]int{65535, 65535}},
		{"left pillar clamps", 2560, 720, 1920, 1080, 5120, 1440, pt{100, 360}, [2]int{0, 32767}},
		{"right pillar clamps", 2560, 720, 1920, 1080, 5120, 1440, pt{2500, 360}, [2]int{65535, 32767}},
		// 32:9 in 16:9: the picture is rows 270..810.
		{"letterbox centre", 1920, 1080, 5120, 1440, 1920, 1080, pt{960, 540}, [2]int{32767, 32767}},
		{"top bar clamps", 1920, 1080, 5120, 1440, 1920, 1080, pt{960, 100}, [2]int{32767, 0}},
		{"bottom bar clamps", 1920, 1080, 5120, 1440, 1920, 1080, pt{960, 1000}, [2]int{32767, 65535}},
		{"letterbox quarter", 1920, 1080, 5120, 1440, 1920, 1080, pt{480, 405}, [2]int{16383, 16383}},
		// 150% scaling on Windows: 1280x720 DIPs over 1920x1080 pixels.
		{"scale 1.5", 1280, 720, 1920, 1080, 1920, 1080, pt{640, 180}, [2]int{32767, 16383}},
		// A 14" MacBook Pro, 1512x982 points at 2x: the 16:9 picture is
		// pixel rows 131..1832, points 65.5..916.
		{"retina letterbox top", 1512, 982, 1920, 1080, 3024, 1964, pt{756, 65.5}, [2]int{32767, 0}},
		{"retina letterbox centre", 1512, 982, 1920, 1080, 3024, 1964, pt{756, 490.75}, [2]int{32767, 32767}},
		{"no drawable yet", 800, 600, 0, 0, 0, 0, pt{400, 150}, [2]int{32767, 16383}},
	} {
		x, y := mapPointer(c.at.x, c.at.y, c.bw, c.bh, c.fw, c.fh, c.dw, c.dh)
		if d := int(x) - c.want[0]; d < -1 || d > 1 {
			t.Errorf("%s: x %d, want %d", c.name, x, c.want[0])
		}
		if d := int(y) - c.want[1]; d < -1 || d > 1 {
			t.Errorf("%s: y %d, want %d", c.name, y, c.want[1])
		}
	}
}

// TestInputForwarderFollowsPicture: the pointer maps onto the picture as
// it is now, through a resize to full screen and a new frame size.
func TestInputForwarderFollowsPicture(t *testing.T) {
	var got []input.Event
	fw, fh, dw, dh := 1920, 1080, 1600, 1000
	f := &inputForwarder{send: func(ev input.Event) { got = append(got, ev) },
		video: func() (int, int, int, int) { return fw, fh, dw, dh }}
	tt := mui.NewTester(f.view, 800, 500)
	move := func(x, y float32) input.MouseMoveAbs {
		t.Helper()
		got = nil
		tt.Move(x, y)
		tt.Frame()
		if len(got) != 1 {
			t.Fatalf("move to %v, %v: %+v", x, y, got)
		}
		return got[0].MouseAbs
	}
	// 16:9 in 16:10: rows 50..950 of 1000, points 25..475.
	if m := move(400, 25); m.X != 32767 || m.Y != 0 {
		t.Fatalf("windowed top edge: %+v", m)
	}
	// The window goes full screen on a 32:9 display while the old frames
	// still show: pillarboxed until the host's new size arrives.
	tt.SetSize(2560, 720)
	dw, dh = 5120, 1440
	if m := move(640, 360); m.X != 0 || m.Y != 32767 {
		t.Fatalf("full screen, old frame, pillar edge: %+v", m)
	}
	fw, fh = 5120, 1440
	if m := move(640, 360); m.X != 16383 || m.Y != 32767 {
		t.Fatalf("full screen, new frame: %+v", m)
	}
}
