//go:build windows

package cursor

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32                   = syscall.NewLazyDLL("user32.dll")
	procGetCursorInfo        = user32.NewProc("GetCursorInfo")
	procGetClipCursor        = user32.NewProc("GetClipCursor")
	procGetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	procGetPhysicalCursorPos = user32.NewProc("GetPhysicalCursorPos")
)

const (
	cursorShowing    = 0x1
	cursorSuppressed = 0x2

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

// cursorInfo is CURSORINFO.
type cursorInfo struct {
	size    uint32
	flags   uint32
	hCursor uintptr
	pos     point
}

// showing reports whether CURSORINFO's flags and cursor draw a cursor: a
// game hides it with ShowCursor, or with SetCursor(NULL), which leaves
// CURSOR_SHOWING set and no cursor.
func showing(flags uint32, hCursor uintptr) bool {
	return flags&cursorShowing != 0 && flags&cursorSuppressed == 0 && hCursor != 0
}

// read compares the clip rectangle with the virtual screen in the caller's
// DPI context, which both share; the position is physical, as the capture
// lays the displays out.
func read() (State, error) {
	ci := cursorInfo{size: uint32(unsafe.Sizeof(cursorInfo{}))}
	if r, _, err := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		return State{}, fmt.Errorf("cursor: GetCursorInfo: %w", err)
	}
	var clip rect
	if r, _, err := procGetClipCursor.Call(uintptr(unsafe.Pointer(&clip))); r == 0 {
		return State{}, fmt.Errorf("cursor: GetClipCursor: %w", err)
	}
	metric := func(i uintptr) int {
		r, _, _ := procGetSystemMetrics.Call(i)
		return int(int32(r))
	}
	desktop := Rect{metric(smXVirtualScreen), metric(smYVirtualScreen), metric(smCXVirtualScreen), metric(smCYVirtualScreen)}
	s := classify(showing(ci.flags, ci.hCursor), Rect{int(clip.Left), int(clip.Top), int(clip.Right - clip.Left), int(clip.Bottom - clip.Top)}, desktop)
	p := ci.pos
	if r, _, _ := procGetPhysicalCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		p = ci.pos
	}
	s.X, s.Y = int(p.X), int(p.Y)
	if s.Visible {
		s.Handle = ci.hCursor
	}
	return s, nil
}
