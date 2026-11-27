package main

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// TestWarpedPointerSendsNoStalePlace: a warp moves the cursor without the
// system telling MyGo, so until the mouse moves, the view must take the
// pointer as where the warp put it, not send the host back to where MyGo
// last saw it.
func TestWarpedPointerSendsNoStalePlace(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.Move(300, 100)
	tt.Frame()
	take()

	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 65535})
	tt.Frame()
	tt.Frame()
	for _, ev := range ofType(take(), input.TypeMouseMoveAbs) {
		if ev.MouseAbs.X < 65000 || ev.MouseAbs.Y < 65000 {
			t.Fatalf("sent %+v after the warp to the corner", ev.MouseAbs)
		}
	}

	tt.Move(400, 200)
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 32000 || abs[0].MouseAbs.X > 33500 {
		t.Fatalf("after a move sent %+v, want the picture's centre", abs)
	}
}

// TestReleaseFromRelativeSendsPlace: the host's cursor moved with the
// motion while the pointer was relative, so the first place after it goes
// even where it matches the last sent before.
func TestReleaseFromRelativeSendsPlace(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Move(300, 100)
	tt.Frame()
	if abs := ofType(take(), input.TypeMouseMoveAbs); len(abs) != 1 {
		t.Fatalf("sent %+v, want the first place", abs)
	}
	l.setLocked(true)
	tt.Frame()
	l.motion(40, 40)
	l.setLocked(false)
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 32000 || abs[0].MouseAbs.X > 33500 || abs[0].MouseAbs.Y < 32000 || abs[0].MouseAbs.Y > 33500 {
		t.Fatalf("after the release sent %+v, want the picture's centre, where the lock held the cursor", abs)
	}
}
