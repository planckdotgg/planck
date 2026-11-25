package input

import "testing"

func TestKeyNamesRoundTrip(t *testing.T) {
	for u, name := range keyNames {
		got, ok := KeyByName(name)
		if !ok || got != u {
			t.Errorf("KeyByName(%q) = %#x, %v; want %#x", name, got, ok, u)
		}
	}
	for name, want := range map[string]uint16{"k": KeyK, "BACKSPACE": KeyBackspace, "f13": KeyF13, "]": KeyRightBracket, "esc": KeyEscape} {
		if got, ok := KeyByName(name); !ok || got != want {
			t.Errorf("KeyByName(%q) = %#x, %v; want %#x", name, got, ok, want)
		}
	}
	if KeyName(KeyLeftControl) != "" {
		t.Error("a modifier has a chord name")
	}
}

func TestSwapCmdCtrl(t *testing.T) {
	key := func(u uint16, m uint8) Event {
		return Event{Type: TypeKey, Key: KeyEvent{Usage: u, Pressed: true, Modifiers: m}}
	}
	cases := []struct{ in, want Event }{
		{key(KeyLeftGUI, ModLeftGUI), key(KeyLeftControl, ModLeftControl)},
		{key(KeyLeftControl, ModLeftControl), key(KeyLeftGUI, ModLeftGUI)},
		{key(KeyRightGUI, ModRightGUI), key(KeyRightControl, ModRightControl)},
		{key(KeyC, ModLeftGUI|ModLeftShift), key(KeyC, ModLeftControl|ModLeftShift)},
		{key(KeyC, ModLeftControl|ModLeftGUI|ModLeftAlt), key(KeyC, ModLeftControl|ModLeftGUI|ModLeftAlt)},
		{key(KeyC, ModRightControl), key(KeyC, ModRightGUI)},
	}
	for _, c := range cases {
		if got := SwapCmdCtrl(c.in); got != c.want {
			t.Errorf("SwapCmdCtrl(%+v) = %+v, want %+v", c.in.Key, got.Key, c.want.Key)
		}
		if back := SwapCmdCtrl(SwapCmdCtrl(c.in)); back != c.in {
			t.Errorf("swapping %+v twice gave %+v", c.in.Key, back.Key)
		}
	}
	move := Event{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: 3}}
	if SwapCmdCtrl(move) != move {
		t.Error("a mouse event changed")
	}
	for _, c := range []struct {
		client, host string
		want         bool
	}{{"darwin", "windows", true}, {"windows", "darwin", true}, {"darwin", "darwin", false}, {"windows", "linux", false}, {"darwin", "", false}} {
		if got := SwapsCmdCtrl(c.client, c.host); got != c.want {
			t.Errorf("SwapsCmdCtrl(%q, %q) = %v", c.client, c.host, got)
		}
	}
}
