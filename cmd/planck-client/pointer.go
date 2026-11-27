package main

import (
	"math"
	"sync"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
	"github.com/planckdotgg/planck/pkg/wire"
)

// pointerMode is how the local pointer is held over the stream.
type pointerMode uint8

const (
	pointerFree pointerMode = iota
	// pointerRelative hides the cursor, holds it still on the picture and
	// sends the mouse's motion: the host's cursor is hidden, as a game
	// turning its camera, or drawing a cursor of its own, has it.
	pointerRelative
	// pointerConfined keeps the cursor, which stands in for the host's
	// visible one, on the picture.
	pointerConfined
)

// rect is a rectangle of the window's content, in DIPs.
type rect struct{ X, Y, W, H float32 }

func (r rect) valid() bool { return r.W > 0 && r.H > 0 }

// pointerSys is the system's pointer, addressed in the window's DIPs. Its
// methods run on the main thread; pointerLock keeps hide and show
// balanced. In pointerRelative the system reports the mouse's motion to
// pointerLock.motion, from any goroutine.
type pointerSys interface {
	hide()
	show()
	// hold keeps the cursor in area as m has it, until free; free lets
	// it go. Each puts the cursor at at, where set, ordered against the
	// hold changing as the system needs for the cursor to end up there:
	// a Windows clip clamps a move to the old clip, and a Wayland lock
	// takes its place only as a hint before it goes. Each reports whether
	// it moved the cursor there: a Wayland compositor may ignore the hint.
	hold(m pointerMode, area rect, at spot) (warped bool)
	free(at spot) (warped bool)
}

// spot is where to put the cursor, in the window's DIPs, where set.
type spot struct {
	x, y float32
	set  bool
}

// pictureCursor is what shows over the picture while the pointer is free
// or confined on it.
type pictureCursor uint8

const (
	// cursorLocal is the window's own cursor, as the UI sets it.
	cursorLocal pictureCursor = iota
	// cursorHidden is no cursor, as the host hides its own without
	// capturing the mouse: over a playing video, or while typing.
	cursorHidden
	// cursorHost is the host's cursor, drawn as it last sent it.
	cursorHost
)

// hostCursorSys shows the host's cursor over the picture in place of the
// window's own. Its methods run on the main thread.
type hostCursorSys interface {
	// set keeps shape, or no cursor for nil, as the cursor over the
	// window, against the UI setting its own, until unset. It reports
	// whether it found another cursor set and set its own again.
	set(shape *wire.CursorShape) bool
	// unset leaves the cursor to the UI.
	unset()
	// close unsets the cursor and frees the cursors made.
	close()
}

// pointerLock decides when the stream holds the local pointer, and holds
// it. The host's cursor state says when a game captured the mouse; the
// user locks and releases it too. The lock engages only while the window
// has the focus, the stream's menu is closed and the picture is known, and
// a release by the user, or the focus leaving, waits for a click on the
// picture or the shortcut to engage again.
type pointerLock struct {
	sys  pointerSys
	send func(input.Event)
	// changed asks for a frame, from any goroutine.
	changed func()

	// cursor shows the host's cursor over the picture; nil leaves the
	// window's own there.
	cursor hostCursorSys

	mu        sync.Mutex
	host      wire.CursorState
	hostKnown bool
	// fresh is whether host came since the pointer last went relative.
	fresh bool
	// shape is the host's cursor image; look what cursor shows over the
	// picture.
	shape *wire.CursorShape
	look  pictureCursor
	// manual is the user's lock while the host has not captured the mouse;
	// paused a lock the user released or the focus left.
	manual, paused bool
	menuOpen       bool
	active         bool
	area           rect
	// mode, heldArea and hidden are what the system was told; dx and dy
	// the motion not sent yet, under a whole unit.
	mode     pointerMode
	heldArea rect
	hidden   bool
	dx, dy   float64
	// warps counts the warps the system made, the last to warpX, warpY;
	// relMoves the relative moves sent.
	warps        uint32
	warpX, warpY float32
	relMoves     uint32
	// hostSeq counts the host's cursor states, and why is what the next
	// apply that changes the hold does it for, for the input trace.
	hostSeq uint32
	why     string
	// lastWarped is what the system said of the last hold or free.
	lastWarped bool
}

func newPointerLock(sys pointerSys, send func(input.Event)) *pointerLock {
	return &pointerLock{sys: sys, send: send}
}

func (l *pointerLock) wanted() bool { return l.host.Captured || l.manual }

// target is the mode the lock should be in now.
func (l *pointerLock) target() pointerMode {
	if !l.wanted() || l.paused || !l.active || l.menuOpen || !l.area.valid() {
		return pointerFree
	}
	if l.hostKnown && l.host.Visible {
		return pointerConfined
	}
	return pointerRelative
}

// setHost takes the host's cursor state. Safe from any goroutine.
func (l *pointerLock) setHost(s wire.CursorState) {
	l.mu.Lock()
	if s.Captured && !l.host.Captured {
		l.paused = false
	}
	l.host, l.hostKnown, l.fresh = s, true, true
	if !l.wanted() {
		l.paused = false
	}
	l.hostSeq++
	l.why = "host_state"
	if itrace.On() {
		r := hostCursorRec{Seq: l.hostSeq, Visible: s.Visible, Captured: s.Captured, HasPos: s.HasPos, X: s.X, Y: s.Y}
		if l.shape != nil {
			r.ShapeID = l.shape.ID
		}
		inputtrace.Emit(itrace, "host_cursor_state", r)
	}
	changed := l.changed
	l.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// setShape takes the host's cursor image. Safe from any goroutine.
func (l *pointerLock) setShape(s wire.CursorShape) {
	l.mu.Lock()
	l.shape = &s
	changed := l.changed
	l.mu.Unlock()
	if changed != nil {
		changed()
	}
}

// pictureCursor shows the cursor the picture has now, with the pointer
// over it, on the picture rather than a bar beside it or the overlay above
// it, or not, and reports it: anything but cursorLocal the UI must not
// draw a cursor of its own over. A free pointer shows the host's cursor
// as it shows, or none as it hides; a confined one the host's; a relative
// one, which the lock hides, and one the user let go of while a game
// holds the mouse, the window's own. It runs as the stream view builds,
// and asks for another frame when the UI set a cursor over its own.
func (l *pointerLock) pictureCursor(over bool) pictureCursor {
	l.mu.Lock()
	want := l.pictureTarget(over)
	again := false
	switch {
	case l.cursor == nil:
		want = cursorLocal
	case want == cursorHidden:
		again = l.cursor.set(nil)
	case want == cursorHost:
		again = l.cursor.set(l.shape)
	case l.look != cursorLocal:
		l.cursor.unset()
	}
	l.look = want
	changed := l.changed
	l.mu.Unlock()
	if again && changed != nil {
		changed()
	}
	return want
}

// pictureTarget is the cursor the picture should have. Called with mu
// held.
func (l *pointerLock) pictureTarget(over bool) pictureCursor {
	if !over || !l.hostKnown || !l.active || l.menuOpen {
		return cursorLocal
	}
	switch {
	case l.mode == pointerRelative:
		return cursorLocal
	case l.mode == pointerConfined:
	case l.host.Captured:
		return cursorLocal
	case !l.host.Visible:
		return cursorHidden
	}
	if l.host.Visible && l.shape != nil {
		return cursorHost
	}
	return cursorLocal
}

// locked reports whether the mouse is to be locked to the stream, though
// the focus or the menu may hold it off for now.
func (l *pointerLock) locked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.wanted() && !l.paused
}

// setLocked locks the mouse to the stream, or releases it until a click on
// the picture or the shortcut.
func (l *pointerLock) setLocked(on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if on {
		l.paused = false
		l.manual = l.manual || !l.host.Captured
		l.why = "user_lock"
	} else {
		l.manual = false
		l.paused = l.wanted()
		l.why = "user_release"
	}
	l.apply()
}

// setMenuOpen holds the lock off while the stream's menu is open.
func (l *pointerLock) setMenuOpen(open bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.menuOpen != open {
		l.menuOpen = open
		l.why = "menu_open"
		if !open {
			l.why = "menu_closed"
		}
		l.apply()
	}
}

// frame takes the window's focus and the picture's place, once a frame.
// The focus leaving releases the lock until the user takes it again.
func (l *pointerLock) frame(active bool, picture rect) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active && !active && l.wanted() {
		l.paused = true
	}
	switch {
	case l.active && !active:
		l.why = "focus_lost"
	case !l.active && active:
		l.why = "focus_gained"
	case l.why == "":
		l.why = "frame"
	}
	l.active, l.area = active, picture
	l.apply()
}

// clickPicture takes a press on the picture that engages a released lock,
// and reports whether it did: that press is the lock's, not the host's.
func (l *pointerLock) clickPicture() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.wanted() || !l.paused || !l.active || l.menuOpen {
		return false
	}
	l.paused = false
	l.why = "click_picture"
	l.apply()
	return true
}

// held is the mode the system holds the pointer in.
func (l *pointerLock) held() pointerMode {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mode
}

// reset lets go of the pointer and forgets the stream's lock, as it ends.
func (l *pointerLock) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.host, l.hostKnown, l.manual, l.paused, l.shape = wire.CursorState{}, false, false, false, nil
	l.why = "stream_end"
	l.apply()
	l.unsetCursor()
}

// close gives the pointer back, whatever state the lock is in: on exit,
// and on a panic on the main thread.
func (l *pointerLock) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release(spot{})
	if l.cursor != nil {
		l.cursor.close()
		l.look = cursorLocal
	}
}

// unsetCursor gives the cursor back to the UI. Called with mu held.
func (l *pointerLock) unsetCursor() {
	if l.look != cursorLocal {
		l.cursor.unset()
		l.look = cursorLocal
	}
}

// release frees the pointer and shows it, and reports whether it warped
// to at.
func (l *pointerLock) release(at spot) bool {
	warped := false
	if l.mode != pointerFree {
		warped = l.sys.free(at)
		if itrace.On() {
			w := warped
			traceSys(pointerSysRec{Call: "free", At: spotOf(at), Warped: &w})
		}
		l.counted(warped, at)
		l.mode = pointerFree
	}
	if l.hidden {
		l.sys.show()
		if itrace.On() {
			traceSys(pointerSysRec{Call: "show"})
		}
		l.hidden = false
	}
	return warped
}

// apply brings the system's pointer to target. Called with mu held.
func (l *pointerLock) apply() {
	reason := l.why
	l.why = ""
	want := l.target()
	if want == l.mode && (want == pointerFree || l.area == l.heldArea) {
		return
	}
	from, fresh := l.mode, l.fresh
	// Free or confined, the local cursor is what places the host's, whose
	// reported place lags it by a round trip, so only coming back from
	// relative, with a place sampled since, does the cursor go there.
	back := from == pointerRelative && l.fresh && l.host.HasPos && l.area.valid()
	var at spot
	if itrace.On() {
		defer func() { l.traceMode(from, want, reason, fresh, at) }()
	}
	switch {
	case want == pointerRelative:
		l.dx, l.dy = 0, 0
		if from != pointerRelative {
			l.fresh = false
		}
		at = spot{x: l.area.X + l.area.W/2, y: l.area.Y + l.area.H/2, set: true}
	case back && (want == pointerConfined || !l.host.Captured):
		at.x, at.y = l.hostPoint()
		at.set = true
	}
	if want == pointerFree {
		l.lastWarped = l.release(at)
		return
	}
	l.lastWarped = l.sys.hold(want, l.area, at)
	if itrace.On() {
		area := areaOf(l.area)
		warped := l.lastWarped
		traceSys(pointerSysRec{Call: "hold", Mode: want.String(), Area: &area, At: spotOf(at), Warped: &warped})
	}
	l.counted(l.lastWarped, at)
	l.mode, l.heldArea = want, l.area
	if hide := want == pointerRelative; hide != l.hidden {
		call := "show"
		if hide {
			l.sys.hide()
			call = "hide"
		} else {
			l.sys.show()
		}
		if itrace.On() {
			traceSys(pointerSysRec{Call: call})
		}
		l.hidden = hide
	}
}

// traceMode writes the change apply made. Called with mu held.
func (l *pointerLock) traceMode(from, to pointerMode, reason string, fresh bool, at spot) {
	inputtrace.Emit(itrace, "pointer_mode", pointerModeRec{
		From: from.String(), To: to.String(), Reason: reason, Want: l.wanted(), Area: areaOf(l.area),
		HostX: l.host.X, HostY: l.host.Y, HostHasPos: l.host.HasPos, HostVisible: l.host.Visible, HostCapt: l.host.Captured,
		WarpTarget: spotOf(at), WarpConfirm: l.lastWarped && at.set, WarpSeq: l.warps, FreshHost: fresh,
		Manual: l.manual, ManualRel: l.paused, Focused: l.active, MenuOpen: l.menuOpen,
	})
}

// counted counts the warp to at the system reports it made.
func (l *pointerLock) counted(warped bool, at spot) {
	if warped && at.set {
		l.warps++
		l.warpX, l.warpY = at.x, at.y
	}
}

// lastWarp is the count of warps made and where the last went, in the
// window's DIPs.
func (l *pointerLock) lastWarp() (n uint32, x, y float32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warps, l.warpX, l.warpY
}

// relativeMoves is the count of relative moves sent, each of which moves
// the host's cursor from the last place sent.
func (l *pointerLock) relativeMoves() uint32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.relMoves
}

// hostPoint is the host's cursor on the picture.
func (l *pointerLock) hostPoint() (float32, float32) {
	return l.area.X + float32(l.host.X)/65535*l.area.W, l.area.Y + float32(l.host.Y)/65535*l.area.H
}

// motion sends the mouse's motion while the lock holds the pointer
// relative, in whole units, keeping the fractions for the next. Safe from
// any goroutine.
func (l *pointerLock) motion(dx, dy float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mode != pointerRelative {
		if itrace.On() {
			inputtrace.Emit(itrace, "client_input", clientInputRec{Event: "motion", RawDX: dx, RawDY: dy, WarpSeq: l.warps,
				Mode: l.mode.String(), SentKind: "suppressed", Suppress: "not_relative"})
		}
		return
	}
	l.dx += dx
	l.dy += dy
	traced := itrace.On()
	var sx, sy int32
	n := 0
	for {
		x, y := whole(l.dx), whole(l.dy)
		if x == 0 && y == 0 {
			break
		}
		l.dx -= float64(x)
		l.dy -= float64(y)
		l.relMoves++
		l.send(input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: x, DY: y}})
		if traced {
			sx, sy, n = sx+int32(x), sy+int32(y), n+1
		}
	}
	if traced {
		r := clientInputRec{Event: "motion", RawDX: dx, RawDY: dy, WarpSeq: l.warps, Mode: l.mode.String(), SentKind: "rel",
			SentDX: sx, SentDY: sy, SentCount: n}
		if n == 0 {
			r.SentKind, r.Suppress = "suppressed", "under_one_unit"
		}
		inputtrace.Emit(itrace, "client_input", r)
	}
}

// whole is the whole part of v toward zero, as one event carries it.
func whole(v float64) int16 {
	return int16(max(min(math.Trunc(v), math.MaxInt16), math.MinInt16))
}
