//go:build linux && !android && (amd64 || arm64)

package main

import (
	"unsafe"

	"github.com/egoist/mygo"
)

// linuxPointer holds the pointer through the window's seat: Wayland's
// pointer constraints with relative pointer motion, or on X11 XFixes
// barriers with XInput 2 raw motion. The cursor hides with GDK's blank
// cursor on MyGo's event window, which MyGo sets its own on again.
type linuxPointer struct {
	win    func() *mygo.Window
	motion func(dx, dy float64)
}

func newSystemPointer(win func() *mygo.Window, motion func(dx, dy float64)) pointerSys {
	return &linuxPointer{win: win, motion: motion}
}

// seat is the current window's seat, nil without one. Main thread.
func (p *linuxPointer) seat() *seat {
	w := p.win()
	if w == nil || w.IsDestroyed() || w.NativeHandle() == 0 {
		return nil
	}
	s, err := seatFor(w.NativeHandle())
	if err != nil {
		return nil
	}
	f := p.motion
	s.motion.Store(&f)
	return s
}

func (p *linuxPointer) hide() {
	if s := p.seat(); s != nil && loadGDK() == nil && s.native.EventWindow != 0 {
		ccall(gdk.windowSetCursor, s.native.EventWindow, blankCursor())
	}
}

func (p *linuxPointer) show() {
	if s := p.seat(); s != nil && loadGDK() == nil && s.native.EventWindow != 0 {
		ccall(gdk.windowSetCursor, s.native.EventWindow, 0)
	}
}

func (p *linuxPointer) hold(m pointerMode, area rect, at spot) {
	s := p.seat()
	if s == nil {
		return
	}
	s.relOn.Store(m == pointerRelative)
	if s.x != nil {
		s.x.hold(m, area, at)
	} else {
		s.waylandHold(m, area, at)
	}
}

func (p *linuxPointer) free(at spot) {
	s := p.seat()
	if s == nil {
		return
	}
	s.relOn.Store(false)
	if s.x != nil {
		s.x.free()
		s.x.warp(at)
	} else {
		s.waylandHint(at)
		s.waylandFree()
	}
}

var blank uintptr

// blankCursor is GDK's cursor that shows nothing, made once.
func blankCursor() uintptr {
	if blank == 0 {
		name := cstr("none")
		blank = ccall(gdk.cursorNewFromName, ccall(gdk.displayGetDefault), uintptr(unsafe.Pointer(&name[0])))
	}
	return blank
}
