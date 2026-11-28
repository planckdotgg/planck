package main

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"

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

// TestStreamEndForgetsUnseenWarp: a warp the view never saw, the stream
// ending before the next frame, is the old stream's too.
func TestStreamEndForgetsUnseenWarp(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	l := newPointerLock(&fakePointer{t: t}, f.send)
	f.lock = l
	tt.Move(300, 100)
	tt.Frame()
	l.setLocked(true)
	if n, _, _ := l.lastWarp(); n != 1 {
		t.Fatalf("%d warps, want the lock's to the centre", n)
	}

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

// TestUnconfirmedWarpSendsLivePlace: a system that does not move the
// cursor where asked, as a Wayland lock's hint may not, leaves the pointer
// where MyGo has it, so a still click goes there and only there.
func TestUnconfirmedWarpSendsLivePlace(t *testing.T) {
	l, sys, tt, take := lockedForwarder(t)
	sys.noWarp = true
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.Move(300, 100)
	tt.Frame()
	l.setHost(wire.CursorState{Visible: true, Captured: true, HasPos: true, X: 65535, Y: 65535})
	tt.Frame()
	if n, _, _ := l.lastWarp(); n != 0 {
		t.Fatalf("%d warps counted, none made", n)
	}
	tt.PressButton(0, 300, 100)
	tt.Frame()
	tt.ReleaseButton(0, 300, 100)
	tt.Frame()
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) == 0 {
		t.Fatal("sent no place")
	}
	for _, ev := range abs {
		if ev.MouseAbs.X < 22000 || ev.MouseAbs.X > 25000 || ev.MouseAbs.Y < 15000 || ev.MouseAbs.Y > 18000 {
			t.Fatalf("sent %+v, want only where MyGo has the pointer", abs)
		}
	}
}

// TestReleaseWarpIgnoresStaleJitter: the traced release. Relative mode
// held the cursor at the picture's centre; the host let go with its cursor
// a quarter of the way in, and the lock warped there. MyGo, which hears of
// no warp, then reported an event queued before it, a fraction of a point
// off the centre: that is not the mouse moving the cursor back to the
// centre, so no move goes there. The mouse moving from the warp's place
// goes as usual.
func TestReleaseWarpIgnoresStaleJitter(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Move(600, 100)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.Move(400, 200)
	tt.Frame()
	take()

	l.setHost(wire.CursorState{HasPos: true, X: 16384, Y: 16384})
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 16000 || abs[0].MouseAbs.X > 16800 || abs[0].MouseAbs.Y < 16000 || abs[0].MouseAbs.Y > 16800 {
		t.Fatalf("on the release sent %+v, want the host's place", abs)
	}
	tt.Move(399.75, 200.25)
	tt.Frame()
	tt.Frame()
	for _, ev := range ofType(take(), input.TypeMouseMoveAbs) {
		if ev.MouseAbs.X > 20000 || ev.MouseAbs.Y > 20000 {
			t.Fatalf("a stale event sent %+v, the picture's centre", ev.MouseAbs)
		}
	}
	tt.Move(230, 110)
	tt.Frame()
	abs = ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X > 17500 || abs[0].MouseAbs.Y > 18500 {
		t.Fatalf("after a move from the warp sent %+v", abs)
	}
}

// clockedForwarder is lockedForwarder with the forwarder's clock the
// test's, which starts at *now.
func clockedForwarder(t *testing.T, now *time.Time) (*pointerLock, *mui.Tester, func() []input.Event) {
	f, tt, got := barsForwarder(1920, 1080)
	f.lock = newPointerLock(&fakePointer{t: t}, f.send)
	f.now = func() time.Time { return *now }
	return f.lock, tt, func() []input.Event {
		out := *got
		*got = nil
		return out
	}
}

// relativeAtCentre takes the pointer relative and leaves MyGo with it at
// the picture's centre, as relative mode holds it.
func relativeAtCentre(l *pointerLock, tt *mui.Tester, take func() []input.Event) {
	tt.Move(600, 100)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.Move(400, 200)
	tt.Frame()
	take()
}

// TestReleaseAtCentreSendsSmallMove: a release that warps the cursor to
// about where relative mode held it leaves nothing for MyGo's stale place
// to get wrong, so the mouse's next 1 DIP goes to the host at once.
func TestReleaseAtCentreSendsSmallMove(t *testing.T) {
	now := time.Unix(1000, 0)
	l, tt, take := clockedForwarder(t, &now)
	relativeAtCentre(l, tt, take)
	l.setHost(wire.CursorState{HasPos: true, X: 32768, Y: 32768})
	tt.Frame()
	take()
	now = now.Add(5 * time.Millisecond)
	tt.Move(401, 200)
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 32820 || abs[0].MouseAbs.X > 32920 {
		t.Fatalf("after a 1 DIP move sent %+v, want it", abs)
	}
}

// TestReleaseWarpPinEnds: away from the release's warp, MyGo's events
// from before it, within warpPinSlop of where it had the pointer, go
// nowhere for warpPinWindow; after it any move MyGo reports is the
// mouse's, however small, while a still mouse still sends nothing.
func TestReleaseWarpPinEnds(t *testing.T) {
	now := time.Unix(1000, 0)
	l, tt, take := clockedForwarder(t, &now)
	relativeAtCentre(l, tt, take)
	l.setHost(wire.CursorState{HasPos: true, X: 16384, Y: 16384})
	tt.Frame()
	take()

	now = now.Add(warpPinWindow / 2)
	tt.Move(399.75, 200.25)
	tt.Frame()
	if abs := ofType(take(), input.TypeMouseMoveAbs); len(abs) != 0 {
		t.Fatalf("a stale event inside the window sent %+v", abs)
	}
	now = now.Add(warpPinWindow)
	tt.Frame()
	tt.Frame()
	if abs := ofType(take(), input.TypeMouseMoveAbs); len(abs) != 0 {
		t.Fatalf("a still mouse after the window sent %+v", abs)
	}
	tt.Move(400.75, 200.25)
	tt.Frame()
	abs := ofType(take(), input.TypeMouseMoveAbs)
	if len(abs) != 1 || abs[0].MouseAbs.X < 32700 || abs[0].MouseAbs.X > 33000 {
		t.Fatalf("a 1 DIP move after the window sent %+v, want it", abs)
	}
}
