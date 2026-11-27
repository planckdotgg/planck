package main

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestMenuRecaptureKeepsLocalCursor is the Oblivion inventory: the cursor
// shows, and hovering the menu has the game clip it again and again. The
// host's place comes only with each change, from a round trip ago or
// older, while the local cursor is what moves the host's; confining it
// must leave it where it is, not take it back there.
func TestMenuRecaptureKeepsLocalCursor(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 6553, Y: 6553})
	l.frame(true, testPicture)
	wantCalls(t, sys)

	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 13107, Y: 13107})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 2")
	for range 3 {
		l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 19660, Y: 19660})
		l.frame(true, testPicture)
		l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 26214, Y: 26214})
		l.frame(true, testPicture)
		wantCalls(t, sys, "free", "hold 2")
	}
}

// TestReengageKeepsLocalCursor: with the host's cursor showing, taking the
// lock back by a click, the shortcut or the stream's menu closing confines
// the cursor where it is.
func TestReengageKeepsLocalCursor(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 65535})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 2")

	l.setLocked(false)
	wantCalls(t, sys, "free")
	if !l.clickPicture() {
		t.Fatal("a click did not take the lock back")
	}
	wantCalls(t, sys, "hold 2")

	l.setLocked(false)
	l.setLocked(true)
	wantCalls(t, sys, "free", "hold 2")

	l.setMenuOpen(true)
	l.setMenuOpen(false)
	wantCalls(t, sys, "free", "hold 2")
	if sys.hides != 0 {
		t.Fatal("a confined cursor hid")
	}
}

// TestHideFlickerComesBackAtHostCursor: a game hiding its cursor while it
// holds the mouse takes the pointer relative; as it shows again, the
// cursor comes back where the host's is now, as the motion took it.
func TestHideFlickerComesBackAtHostCursor(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 0, Y: 0})
	l.frame(true, testPicture)
	sys.take()
	l.setHost(wire.CursorState{Captured: true, HasPos: true, X: 0, Y: 0})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 65535})
	l.frame(true, testPicture)
	wantCalls(t, sys, "hold 2 at 760,405", "show")
}

// TestReleaseFromRelativeIgnoresOldHostPlace: the host's place from before
// the pointer went relative is no longer where its cursor is, so letting
// go leaves the cursor where the lock held it.
func TestReleaseFromRelativeIgnoresOldHostPlace(t *testing.T) {
	l, sys, _ := newTestLock(t)
	l.setHost(wire.CursorState{HasPos: true, X: 0, Y: 0})
	l.setLocked(true)
	wantCalls(t, sys, "hold 1 at 400,202.5", "hide")
	l.motion(300, 120)
	l.setLocked(false)
	wantCalls(t, sys, "free", "show")
}

// TestLockReportsWarps: each warp the lock makes is counted, with its
// place, for the view to take in place of where the system last told it
// the pointer was.
func TestLockReportsWarps(t *testing.T) {
	l, _, _ := newTestLock(t)
	if n, _, _ := l.lastWarp(); n != 0 {
		t.Fatalf("%d warps before any", n)
	}
	l.setHost(wire.CursorState{Captured: true})
	l.frame(true, testPicture)
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 0})
	l.frame(true, testPicture)
	if n, x, y := l.lastWarp(); n != 2 || x != 760 || y != 0 {
		t.Fatalf("warps %d, last at %g,%g", n, x, y)
	}
}
