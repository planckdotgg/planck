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

// TestStreamEndForgetsWarp: a warp's pin is the last stream's; the next
// stream, with MyGo's pointer where it was, takes the pointer from there,
// not from where the old stream's warp put it.
func TestStreamEndForgetsWarp(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	l := newPointerLock(&fakePointer{t: t}, f.send)
	f.lock = l
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.Move(300, 100)
	tt.Frame()
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 65535})
	tt.Frame()

	f.streamEnded()
	l.reset()
	*got = nil
	l.setHost(wire.CursorState{Visible: true, HasPos: true, X: 65535, Y: 65535})
	tt.Frame()
	abs := ofType(*got, input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 22000 || abs[0].MouseAbs.X > 25000 || abs[0].MouseAbs.Y < 15000 || abs[0].MouseAbs.Y > 18000 {
		t.Fatalf("the new stream sent %+v, want where MyGo has the pointer", abs)
	}
}

// TestRelativeBetweenFramesSendsPlace: the pointer going relative, moving
// the host's cursor and coming back all between two frames, the next
// frame sends its place, though it matches the last sent.
func TestRelativeBetweenFramesSendsPlace(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Move(400, 200)
	tt.Frame()
	if abs := ofType(take(), input.TypeMouseMoveAbs); len(abs) != 1 {
		t.Fatalf("sent %+v, want the first place", abs)
	}
	l.setLocked(true)
	l.motion(40, 40)
	l.setLocked(false)
	tt.Frame()
	if abs := ofType(take(), input.TypeMouseMoveAbs); len(abs) != 1 {
		t.Fatalf("after relative motion between frames sent %+v, want the place again", abs)
	}
}
