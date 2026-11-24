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
