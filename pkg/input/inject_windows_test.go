//go:build windows

package input

import (
	"testing"
	"unsafe"
)

func TestInputRecordMatchesWin32Layout(t *testing.T) {
	var r inputRecord
	ki := (*keybdInput)(unsafe.Pointer(&r.mi))
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof(INPUT)", unsafe.Sizeof(r), 40},
		{"offsetof(INPUT.mi)", unsafe.Offsetof(r.mi), 8},
		{"sizeof(MOUSEINPUT)", unsafe.Sizeof(r.mi), 32},
		{"MOUSEINPUT.dwFlags", unsafe.Offsetof(r.mi.flags), 12},
		{"MOUSEINPUT.dwExtraInfo", unsafe.Offsetof(r.mi.extraInfo), 24},
		{"sizeof(KEYBDINPUT)", unsafe.Sizeof(*ki), 24},
		{"KEYBDINPUT.wScan", unsafe.Offsetof(ki.scan), 2},
		{"KEYBDINPUT.dwFlags", unsafe.Offsetof(ki.flags), 4},
		{"KEYBDINPUT.dwExtraInfo", unsafe.Offsetof(ki.extraInfo), 16},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestButtonFlags(t *testing.T) {
	cases := []struct {
		b           MouseButton
		flags, data uint32
	}{
		{MouseButton{ButtonLeft, true}, mouseeventfLeftDown, 0},
		{MouseButton{ButtonLeft, false}, mouseeventfLeftUp, 0},
		{MouseButton{ButtonRight, true}, mouseeventfRightDown, 0},
		{MouseButton{ButtonMiddle, false}, mouseeventfMiddleUp, 0},
		{MouseButton{ButtonX1, true}, mouseeventfXDown, xbutton1},
		{MouseButton{ButtonX2, false}, mouseeventfXUp, xbutton2},
	}
	for _, c := range cases {
		if f, d := buttonFlags(c.b); f != c.flags || d != c.data {
			t.Errorf("buttonFlags(%+v) = %#x %d, want %#x %d", c.b, f, d, c.flags, c.data)
		}
	}
}

// TestAbsMoveTargetsTheStreamedDisplay: with a target set, an absolute
// move goes across the virtual desktop onto the streamed display, not the
// primary one; with none it covers the primary, as before.
func TestAbsMoveTargetsTheStreamedDisplay(t *testing.T) {
	s := &SendInputInjector{}
	if dx, dy, f := s.absMove(MouseMoveAbs{X: 100, Y: 200}); dx != 100 || dy != 200 || f != mouseeventfMove|mouseeventfAbsolute {
		t.Fatalf("no target: %d, %d, flags %#x", dx, dy, f)
	}
	// bigboi with a session's virtual display: DISPLAY1 1920 x 1080
	// primary, the 5120 x 1440 virtual display to its right.
	s.SetTarget(Rect{1920, 0, 5120, 1440}, Rect{0, 0, 7040, 1440})
	dx, dy, f := s.absMove(MouseMoveAbs{X: 0, Y: 65535})
	if f != mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk {
		t.Fatalf("flags %#x, want MOVE | ABSOLUTE | VIRTUALDESK", f)
	}
	if x, y := int(dx)*7040/65536, int(dy)*1440/65536; x != 1920 || y != 1439 {
		t.Fatalf("bottom-left of the virtual display lands at %d, %d, want 1920, 1439", x, y)
	}
	s.SetTarget(Rect{}, Rect{})
	if _, _, f := s.absMove(MouseMoveAbs{}); f&mouseeventfVirtualDesk != 0 {
		t.Fatal("a cleared target still moves across the virtual desktop")
	}
}

// TestButtonsDoNotMoveThePointer: a press and release carry no move or
// absolute flag, so a drag's LEFTDOWN, moves and LEFTUP keep the pointer
// where the moves put it.
func TestButtonsDoNotMoveThePointer(t *testing.T) {
	for _, b := range []Button{ButtonLeft, ButtonRight, ButtonMiddle, ButtonX1, ButtonX2} {
		for _, down := range []bool{true, false} {
			if f, _ := buttonFlags(MouseButton{b, down}); f&(mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDesk) != 0 {
				t.Errorf("button %v down %v: flags %#x move the pointer", b, down, f)
			}
		}
	}
}

// TestInjectMouseWhileLocked: a locked client's relative moves go as plain
// MOUSEEVENTF_MOVE with signed deltas, never absolute, and its buttons and
// wheel go as they do unlocked.
func TestInjectMouseWhileLocked(t *testing.T) {
	var got []mouseInput
	s := &SendInputInjector{post: func(r *inputRecord) error {
		if r.typ != inputMouse {
			t.Fatalf("record type %d, want mouse", r.typ)
		}
		got = append(got, r.mi)
		return nil
	}}
	s.SetTarget(Rect{1920, 0, 2560, 1440}, Rect{0, 0, 4480, 1440})
	for _, ev := range []Event{
		{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: -37, DY: 12}},
		{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: 32767, DY: -32768}},
		{Type: TypeMouseButton, Button: MouseButton{Button: ButtonLeft, Pressed: true}},
		{Type: TypeMouseButton, Button: MouseButton{Button: ButtonLeft}},
		{Type: TypeMouseWheel, Wheel: MouseWheel{DY: -WheelNotch, DX: 40}},
	} {
		if err := s.Inject(ev); err != nil {
			t.Fatal(err)
		}
	}
	neg := func(v int32) uint32 { return uint32(v) }
	want := []mouseInput{
		{dx: -37, dy: 12, flags: mouseeventfMove},
		{dx: 32767, dy: -32768, flags: mouseeventfMove},
		{flags: mouseeventfLeftDown},
		{flags: mouseeventfLeftUp},
		{mouseData: neg(-WheelNotch), flags: mouseeventfWheel},
		{mouseData: 40, flags: mouseeventfHWheel},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestInjectedEventsCarryTheMarker: a client capturing system keys on the
// host's own machine tells the host's events by their dwExtraInfo.
func TestInjectedEventsCarryTheMarker(t *testing.T) {
	var got []uintptr
	s := &SendInputInjector{post: func(r *inputRecord) error {
		if r.typ == inputKeyboard {
			got = append(got, (*keybdInput)(unsafe.Pointer(&r.mi)).extraInfo)
		} else {
			got = append(got, r.mi.extraInfo)
		}
		return nil
	}}
	for _, ev := range []Event{
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftGUI, Pressed: true, Modifiers: ModLeftGUI, SystemKeyCapture: true}},
		{Type: TypeMouseButton, Button: MouseButton{Button: ButtonLeft, Pressed: true}},
	} {
		if err := s.Inject(ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != InjectMarker || got[1] != InjectMarker {
		t.Fatalf("dwExtraInfo %#x, want %#x on each", got, InjectMarker)
	}
}

// TestInjectSystemKeys: the Windows keys go as their E0-prefixed scan
// codes, which Windows reads as VK_LWIN and VK_RWIN; Alt and Tab, as
// Alt-Tab needs, by their plain ones.
func TestInjectSystemKeys(t *testing.T) {
	type sent struct {
		scan  uint16
		flags uint32
	}
	var got []sent
	s := &SendInputInjector{post: func(r *inputRecord) error {
		ki := (*keybdInput)(unsafe.Pointer(&r.mi))
		got = append(got, sent{ki.scan, ki.flags})
		return nil
	}}
	for _, ev := range []Event{
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftGUI, Pressed: true, Modifiers: ModLeftGUI, SystemKeyCapture: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftGUI, Modifiers: 0, SystemKeyCapture: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyRightGUI, Pressed: true, Modifiers: ModRightGUI, SystemKeyCapture: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyLeftAlt, Pressed: true, Modifiers: ModLeftAlt, SystemKeyCapture: true}},
		{Type: TypeKey, Key: KeyEvent{Usage: KeyTab, Pressed: true, Modifiers: ModLeftAlt, SystemKeyCapture: true}},
	} {
		if err := s.Inject(ev); err != nil {
			t.Fatal(err)
		}
	}
	want := []sent{
		{0x5B, keyeventfScanCode | keyeventfExtendedKey},
		{0x5B, keyeventfScanCode | keyeventfExtendedKey | keyeventfKeyUp},
		{0x5C, keyeventfScanCode | keyeventfExtendedKey},
		{0x38, keyeventfScanCode},
		{0x0F, keyeventfScanCode},
	}
	if len(got) != len(want) {
		t.Fatalf("sent %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d: scan %#x flags %#x, want scan %#x flags %#x", i, got[i].scan, got[i].flags, want[i].scan, want[i].flags)
		}
	}
}
