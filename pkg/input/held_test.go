package input

import (
	"slices"
	"testing"
)

func TestHeldRelease(t *testing.T) {
	var h Held
	for _, ev := range []Event{
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftAlt, Pressed: true, Modifiers: ModLeftAlt}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftGUI, Pressed: true, Modifiers: ModLeftAlt | ModLeftGUI}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyTab, Pressed: true, Modifiers: ModLeftAlt | ModLeftGUI, SystemKeyCapture: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyA, Pressed: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyA}},
		{Type: TypeMouseButton, Button: MouseButton{Button: ButtonLeft, Pressed: true}},
		{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: 3}},
	} {
		h.Note(ev)
	}
	want := []Event{
		{Type: TypeMouseButton, Button: MouseButton{Button: ButtonLeft}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyTab, Modifiers: ModLeftAlt | ModLeftGUI}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftAlt, Modifiers: ModLeftGUI}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftGUI}},
	}
	if got := h.Release(); !slices.Equal(got, want) {
		t.Fatalf("release:\n got %+v\nwant %+v", got, want)
	}
	if got := h.Release(); len(got) != 0 {
		t.Fatalf("a second release let go of %+v", got)
	}
	for _, ev := range want {
		if err := ev.validate(); err != nil {
			t.Fatalf("%+v: %v", ev, err)
		}
	}
}
