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
