package main

import (
	"math"
	"strconv"
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
	cgEventCreate               func(source uintptr) uintptr
)

// NSEvent types of the mouse's motion: moved, dragged with the left, right
// or another button, and leaving a tracking area, as the window's view.
const motionEvents = 1<<5 | 1<<6 | 1<<7 | 1<<27 | 1<<9

// macPointer holds the macOS pointer as SDL's Cocoa backend does: relative,
// the cursor hidden and dissociated from the mouse, which reports its
// motion as NSEvent deltas; confined, the cursor moving as usual and warped
// back onto the picture whenever an event finds it off it. MyGo's view
// hears the mouse's moves over it alone, so while held the window takes
// them wherever the cursor is. A warp leaves the mouse's events
// suppressed for a moment unless the cursor is associated again at once,
// which warp does outside relative mode.
//
// A warp shows in the next mouse event's delta, as though the mouse had
// moved the cursor there: relative mode dissociates before it warps, as
// GLFW does, and takes the warp's distance off the first event at the
// warp's target stamped at or after the warp, so that it is never sent as
// motion.
type macPointer struct {
	win    func() *mygo.Window
	motion func(dx, dy float64)
	// cursorAt is where the cursor is now, in the screen's points.
	cursorAt func() cgPoint
	// uptime is the seconds since boot on the clock of NSEvent's timestamp.
	uptime func() float64

	monitor objc.ID
	mode    pointerMode
	// area is the picture in the screen's points, from its top-left
	// corner, as CoreGraphics places the cursor.
	area rect
	// unwarp is the distance relative mode's warps moved the cursor since
	// a mouse event last carried one, the last to warpTo at uptime warpAt.
	unwarp       cgPoint
	warpTo       cgPoint
	warpAt       float64
	unwarpQueued bool
}

// warpEventWait is how long after a warp, in seconds, relative mode waits
// for the event that carries it; the trace saw it 6 to 10 ms after.
const warpEventWait = 0.100

func newSystemPointer(win func() *mygo.Window, motion func(dx, dy float64)) pointerSys {
	loadPointerCG.Do(func() {
		cg := apple.CoreGraphics
		apple.Register(&cgMainDisplayID, cg, "CGMainDisplayID")
		apple.Register(&cgDisplayHideCursor, cg, "CGDisplayHideCursor")
		apple.Register(&cgDisplayShowCursor, cg, "CGDisplayShowCursor")
		apple.Register(&cgAssociateMouseAndPosition, cg, "CGAssociateMouseAndMouseCursorPosition")
		apple.Register(&cgWarpMouseCursorPosition, cg, "CGWarpMouseCursorPosition")
		apple.Register(&cgEventGetLocation, cg, "CGEventGetLocation")
		apple.Register(&cgEventCreate, cg, "CGEventCreate")
	})
	return &macPointer{win: win, motion: motion, cursorAt: cursorLocation, uptime: systemUptime}
}

func systemUptime() float64 {
	info := objc.ID(objc.GetClass("NSProcessInfo")).Send(objc.RegisterName("processInfo"))
	return objc.Send[float64](info, objc.RegisterName("systemUptime"))
}

func cursorLocation() cgPoint {
	now := cgEventCreate(0)
	at := cgEventGetLocation(now)
	apple.CFRelease(now)
	return at
}

func (p *macPointer) hide() {
	traceCG(pointerSysRec{Call: "hide_cursor"}, cgDisplayHideCursor(cgMainDisplayID()))
}
func (p *macPointer) show() {
	traceCG(pointerSysRec{Call: "show_cursor"}, cgDisplayShowCursor(cgMainDisplayID()))
}

// traceCG traces a CoreGraphics call with the CGError it returned.
func traceCG(rec pointerSysRec, cgErr int32) {
	if itrace.On() {
		rec.Ret = int64(cgErr)
		if cgErr != 0 {
			rec.Err = "CGError " + strconv.Itoa(int(cgErr))
		}
		traceSys(rec)
	}
}

func associate(connected uint32) {
	traceCG(pointerSysRec{Call: "associate", Mode: [...]string{"dissociate", "associate"}[connected]}, cgAssociateMouseAndPosition(connected))
}

// hold takes the mode before it warps: relative mode dissociates first,
// and its warp is not associated again; free associates before its warp.
func (p *macPointer) hold(m pointerMode, area rect, at spot) bool {
	p.watch()
	x, y := p.origin()
	p.area = rect{X: x + area.X, Y: y + area.Y, W: area.W, H: area.H}
	if p.mode != pointerRelative {
		p.unwarpQueued, p.unwarp = false, cgPoint{}
	}
	p.mode = m
	if m == pointerRelative {
		associate(0)
	}
	warped := at.set && p.warpGlobal(float64(x+at.x), float64(y+at.y))
	if m != pointerRelative && !at.set {
		associate(1)
	}
	p.allMoves(true)
	return warped
}

func (p *macPointer) free(at spot) bool {
	p.mode = pointerFree
	associate(1)
	p.allMoves(false)
	if !at.set {
		return false
	}
	ox, oy := p.origin()
	return p.warpGlobal(float64(ox+at.x), float64(oy+at.y))
}

// allMoves has the window take the mouse's moves off its view too, or
// leaves them to the view's tracking area.
func (p *macPointer) allMoves(on bool) {
	if w := p.win(); w != nil {
		objc.ID(w.NativeHandle()).Send(objc.RegisterName("setAcceptsMouseMovedEvents:"), on)
	}
}

// warpGlobal warps the cursor to x, y on the screen and reports whether
// CoreGraphics moved it.
func (p *macPointer) warpGlobal(x, y float64) bool {
	from := p.cursorAt()
	at := p.uptime()
	ret := cgWarpMouseCursorPosition(cgPoint{x, y})
	if itrace.On() {
		traceCG(pointerSysRec{Call: "warp", ScreenX: f64p(x), ScreenY: f64p(y)}, ret)
	}
	if p.mode != pointerRelative {
		p.unwarpQueued, p.unwarp = false, cgPoint{}
		associate(1)
		return ret == 0
	}
	if ret != 0 {
		return false
	}
	p.unwarp = cgPoint{p.unwarp.X + x - from.X, p.unwarp.Y + y - from.Y}
	p.warpTo, p.warpAt, p.unwarpQueued = cgPoint{x, y}, at, true
	return true
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
		dx, dy := objc.Send[float64](ev, objc.RegisterName("deltaX")), objc.Send[float64](ev, objc.RegisterName("deltaY"))
		at, known := cgPoint{}, false
		if cg := objc.Send[uintptr](ev, objc.RegisterName("CGEvent")); cg != 0 {
			at, known = cgEventGetLocation(cg), true
		}
		p.relativeEvent(dx, dy, at, known, objc.Send[float64](ev, objc.RegisterName("timestamp")))
	case pointerConfined:
		at := p.cursorAt()
		x, y := clampInto(at.X, p.area.X, p.area.W), clampInto(at.Y, p.area.Y, p.area.H)
		if x != at.X || y != at.Y {
			p.warpGlobal(x, y)
		}
	}
}

// relativeEvent sends a relative mouse event's delta, less the warps it
// carries: the first event at the warps' target stamped at or after the
// last warp, and within warpEventWait of it, carries them. An event stamped
// before the warp was queued before it and is the mouse's alone, wherever
// it lies. An event with no stamp is taken as stamped now.
func (p *macPointer) relativeEvent(dx, dy float64, at cgPoint, known bool, stamp float64) {
	if p.unwarpQueued {
		if stamp <= 0 {
			stamp = p.uptime()
		}
		u := p.unwarp
		switch {
		case stamp < p.warpAt:
		case stamp-p.warpAt > warpEventWait || u == (cgPoint{}):
			p.unwarpQueued, p.unwarp = false, cgPoint{}
		case !known || math.Abs(at.X-p.warpTo.X) <= 1 && math.Abs(at.Y-p.warpTo.Y) <= 1:
			dx, dy = dx-u.X, dy-u.Y
			p.unwarpQueued, p.unwarp = false, cgPoint{}
		}
	}
	p.motion(dx, dy)
}

// clampInto keeps v on the span of n points from lo.
func clampInto(v float64, lo, n float32) float64 {
	return min(max(v, float64(lo)), float64(lo+n)-1)
}
