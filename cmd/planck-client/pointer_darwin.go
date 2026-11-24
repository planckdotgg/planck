package main

import (
	"sync"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/internal/apple"
)

type cgPoint struct{ X, Y float64 }

var (
	loadPointerCG               sync.Once
	cgMainDisplayID             func() uint32
	cgDisplayHideCursor         func(display uint32) int32
	cgDisplayShowCursor         func(display uint32) int32
	cgAssociateMouseAndPosition func(connected uint32) int32
	cgWarpMouseCursorPosition   func(at cgPoint) int32
	cgEventGetLocation          func(ev uintptr) cgPoint
)

// NSEvent types of the mouse's motion: moved, and dragged with the left,
// right or another button.
const motionEvents = 1<<5 | 1<<6 | 1<<7 | 1<<27

// macPointer holds the macOS pointer as SDL's Cocoa backend does: relative,
// the cursor hidden and dissociated from the mouse, which reports its
// motion as NSEvent deltas; confined, the cursor moving as usual and warped
// back onto the picture whenever an event finds it off it. A warp leaves
// the mouse's events suppressed for a moment unless the cursor is
// associated again at once, which warp does outside relative mode.
type macPointer struct {
	win    func() *mygo.Window
	motion func(dx, dy float64)

	monitor objc.ID
	mode    pointerMode
	// area is the picture in the screen's points, from its top-left
	// corner, as CoreGraphics places the cursor.
	area rect
}

func newSystemPointer(win func() *mygo.Window, motion func(dx, dy float64)) pointerSys {
	loadPointerCG.Do(func() {
		cg := apple.CoreGraphics
		apple.Register(&cgMainDisplayID, cg, "CGMainDisplayID")
		apple.Register(&cgDisplayHideCursor, cg, "CGDisplayHideCursor")
		apple.Register(&cgDisplayShowCursor, cg, "CGDisplayShowCursor")
		apple.Register(&cgAssociateMouseAndPosition, cg, "CGAssociateMouseAndMouseCursorPosition")
		apple.Register(&cgWarpMouseCursorPosition, cg, "CGWarpMouseCursorPosition")
		apple.Register(&cgEventGetLocation, cg, "CGEventGetLocation")
	})
	return &macPointer{win: win, motion: motion}
}

func (p *macPointer) hide() { cgDisplayHideCursor(cgMainDisplayID()) }
func (p *macPointer) show() { cgDisplayShowCursor(cgMainDisplayID()) }

func (p *macPointer) hold(m pointerMode, area rect) {
	p.watch()
	x, y := p.origin()
	p.area = rect{X: x + area.X, Y: y + area.Y, W: area.W, H: area.H}
	p.mode = m
	connected := uint32(1)
	if m == pointerRelative {
		connected = 0
	}
	cgAssociateMouseAndPosition(connected)
}

func (p *macPointer) free() {
	p.mode = pointerFree
	cgAssociateMouseAndPosition(1)
}

func (p *macPointer) warp(x, y float32) {
	ox, oy := p.origin()
	p.warpGlobal(float64(ox+x), float64(oy+y))
}

func (p *macPointer) warpGlobal(x, y float64) {
	cgWarpMouseCursorPosition(cgPoint{x, y})
	if p.mode != pointerRelative {
		cgAssociateMouseAndPosition(1)
	}
}

// origin is the window content's top-left corner on the screen, in points.
func (p *macPointer) origin() (float32, float32) {
	w := p.win()
	if w == nil {
		return 0, 0
	}
	b := w.ContentBounds()
	return float32(b.X), float32(b.Y)
}

// watch sees the mouse's motion before the window does, from the first
// hold on; it lets every event through.
func (p *macPointer) watch() {
	if p.monitor != 0 {
		return
	}
	blk := objc.NewBlock(func(_ objc.Block, ev objc.ID) objc.ID {
		p.event(ev)
		return ev
	})
	p.monitor = objc.ID(objc.GetClass("NSEvent")).Send(objc.RegisterName("addLocalMonitorForEventsMatchingMask:handler:"), uint64(motionEvents), blk).
		Send(objc.RegisterName("retain"))
}

func (p *macPointer) event(ev objc.ID) {
	switch p.mode {
	case pointerRelative:
		p.motion(objc.Send[float64](ev, objc.RegisterName("deltaX")), objc.Send[float64](ev, objc.RegisterName("deltaY")))
	case pointerConfined:
		at := cgEventGetLocation(objc.Send[uintptr](ev, objc.RegisterName("CGEvent")))
		x, y := clampInto(at.X, p.area.X, p.area.W), clampInto(at.Y, p.area.Y, p.area.H)
		if x != at.X || y != at.Y {
			p.warpGlobal(x, y)
		}
	}
}

// clampInto keeps v on the span of n points from lo.
func clampInto(v float64, lo, n float32) float64 {
	return min(max(v, float64(lo)), float64(lo+n)-1)
}
