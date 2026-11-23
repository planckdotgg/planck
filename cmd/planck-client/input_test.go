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
