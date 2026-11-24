package main

import (
	"math"
	"sync"

	"github.com/planckdotgg/planck/pkg/input"
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
	// hold keeps the cursor in area as m has it, until free.
	hold(m pointerMode, area rect)
	free()
	warp(x, y float32)
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

	mu        sync.Mutex
	host      wire.CursorState
	hostKnown bool
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
	l.host, l.hostKnown = s, true
	if !l.wanted() {
		l.paused = false
	}
	changed := l.changed
	l.mu.Unlock()
	if changed != nil {
		changed()
	}
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
	} else {
		l.manual = false
		l.paused = l.wanted()
	}
	l.apply()
}

// setMenuOpen holds the lock off while the stream's menu is open.
func (l *pointerLock) setMenuOpen(open bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.menuOpen != open {
		l.menuOpen = open
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
	l.host, l.hostKnown, l.manual, l.paused = wire.CursorState{}, false, false, false
	l.apply()
}

// close gives the pointer back, whatever state the lock is in: on exit,
// and on a panic on the main thread.
func (l *pointerLock) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release()
}

func (l *pointerLock) release() {
	if l.mode != pointerFree {
		l.sys.free()
		l.mode = pointerFree
	}
	if l.hidden {
		l.sys.show()
		l.hidden = false
	}
}

// apply brings the system's pointer to target. Called with mu held.
func (l *pointerLock) apply() {
	want := l.target()
	if want == l.mode && (want == pointerFree || l.area == l.heldArea) {
		return
	}
	from := l.mode
	switch {
	case want == pointerFree:
		l.release()
		if from == pointerRelative && l.host.HasPos && !l.host.Captured && l.area.valid() {
			// The game let go: the cursor comes back where the host has it.
			l.sys.warp(l.hostPoint())
		}
		return
	case want == pointerRelative:
		l.dx, l.dy = 0, 0
		l.sys.warp(l.area.X+l.area.W/2, l.area.Y+l.area.H/2)
	case from != pointerConfined && l.host.HasPos:
		l.sys.warp(l.hostPoint())
	}
	l.sys.hold(want, l.area)
	l.mode, l.heldArea = want, l.area
	if hide := want == pointerRelative; hide != l.hidden {
		if hide {
			l.sys.hide()
		} else {
			l.sys.show()
		}
		l.hidden = hide
	}
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
		return
	}
	l.dx += dx
	l.dy += dy
	for {
		x, y := whole(l.dx), whole(l.dy)
		if x == 0 && y == 0 {
			return
		}
		l.dx -= float64(x)
		l.dy -= float64(y)
		l.send(input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: x, DY: y}})
	}
}

// whole is the whole part of v toward zero, as one event carries it.
func whole(v float64) int16 {
	return int16(max(min(math.Trunc(v), math.MaxInt16), math.MinInt16))
}
