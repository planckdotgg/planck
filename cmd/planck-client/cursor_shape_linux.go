package main

import (
	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/pkg/wire"
)

// gdkHostCursor sets GdkCursors made of the host's shapes on MyGo's event
// window, sized as the host's DPI sizes them at GTK's scale. MyGo sets its
// own cursor there as the pointer enters and as its elements ask for
// another, so set puts the host's back whenever it finds another one.
type gdkHostCursor struct {
	win   func() *mygo.Window
	cache cursorCache[uint64, uintptr]
	ours  uintptr
}

func newHostCursor(win func() *mygo.Window) hostCursorSys {
	return &gdkHostCursor{win: win, cache: cursorCache[uint64, uintptr]{free: func(c uintptr) { ccall(gdk.objectUnref, c) }}}
}

// eventWindow is MyGo's event window of the current window, 0 without.
func (c *gdkHostCursor) eventWindow() uintptr {
	if loadGDK() != nil {
		return 0
	}
	w := c.win()
	if w == nil || w.IsDestroyed() || w.NativeHandle() == 0 {
		return 0
	}
	s, err := seatFor(w.NativeHandle())
	if err != nil {
		return 0
	}
	return s.native.EventWindow
}

func (c *gdkHostCursor) set(shape *wire.CursorShape) bool {
	win := c.eventWindow()
	if win == 0 {
		return false
	}
	want := blankCursor()
	if shape != nil {
		cur, ok := c.cache.get(shape.ID, func() (uintptr, bool) {
			cur := newGdkCursor(shape)
			return cur, cur != 0
		})
		if !ok {
			return false
		}
		want = cur
	}
	if ccall(gdk.windowGetCursor, win) == want {
		return false
	}
	ccall(gdk.windowSetCursor, win, want)
	c.ours = want
	return true
}

func (c *gdkHostCursor) unset() {
	if win := c.eventWindow(); win != 0 && c.ours != 0 && ccall(gdk.windowGetCursor, win) == c.ours {
		ccall(gdk.windowSetCursor, win, 0)
	}
	c.ours = 0
}

func (c *gdkHostCursor) close() {
	c.unset()
	c.cache.close()
}

// newGdkCursor makes a cursor of shape at 100% (96 DPI): GDK scales it
// to the window's scale on its own. The pixbuf is copied so that it owns
// its pixels.
func newGdkCursor(shape *wire.CursorShape) uintptr {
	w, h := int(shape.Width), int(shape.Height)
	if w == 0 || h == 0 || len(shape.RGBA) < w*h*4 {
		return 0
	}
	px, hx, hy := shape.RGBA, int(shape.HotX), int(shape.HotY)
	if k := cursorScale(shape, 96); k != 1 {
		nw, nh := max(int(float64(w)*k+0.5), 1), max(int(float64(h)*k+0.5), 1)
		px = scaleRGBA(px, w, h, nw, nh)
		hx, hy = int(float64(hx)*k), int(float64(hy)*k)
		w, h = nw, nh
	}
	// GDK_COLORSPACE_RGB, with alpha, 8 bits a sample.
	tmp := ccall(gdk.pixbufNewFromData, uintptr(unsafePointer(px)), 0, 1, 8, uintptr(w), uintptr(h), uintptr(w*4), 0, 0)
	if tmp == 0 {
		return 0
	}
	pix := ccall(gdk.pixbufCopy, tmp)
	ccall(gdk.objectUnref, tmp)
	if pix == 0 {
		return 0
	}
	defer ccall(gdk.objectUnref, pix)
	return ccall(gdk.cursorNewFromPixbuf, ccall(gdk.displayGetDefault), pix, uintptr(min(hx, w-1)), uintptr(min(hy, h-1)))
}
