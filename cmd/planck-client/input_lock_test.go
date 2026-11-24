package main

import (
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// lockedForwarder is barsForwarder, 16:9 in an 800 x 400 view, with a
// lock on a fake pointer.
func lockedForwarder(t *testing.T) (*pointerLock, *fakePointer, *mui.Tester, func() []input.Event) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakePointer{t: t}
	f.lock = newPointerLock(sys, f.send)
	take := func() []input.Event {
		out := *got
		*got = nil
		return out
	}
	return f.lock, sys, tt, take
}

func ofType(evs []input.Event, typ input.Type) []input.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(e input.Event) bool { return e.Type != typ })
}

// TestLockedPointerSendsMotionNotPlaces: held relative, the pointer's
// place goes nowhere, its motion does, and presses and the wheel go on
// everywhere, bars included.
func TestLockedPointerSendsMotionNotPlaces(t *testing.T) {
	l, sys, tt, take := lockedForwarder(t)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	if sys.mode != pointerRelative || sys.hides != 1 {
		t.Fatalf("held %d, hidden %d", sys.mode, sys.hides)
	}
	if sys.area.X < 44 || sys.area.X > 45 || sys.area.W < 711 || sys.area.W > 712 {
		t.Fatalf("held over %+v, want the picture, not its bars", sys.area)
	}
	take()
	tt.Move(300, 100)
	tt.Frame()
	l.motion(3, -4)
	tt.PressButton(0, 20, 200)
	tt.Frame()
	tt.ReleaseButton(0, 20, 200)
	tt.Frame()
	tt.Scroll(20, 200, 0, -wheelDIPs())
	tt.Frame()
	got := take()
	if abs := ofType(got, input.TypeMouseMoveAbs); len(abs) != 0 {
		t.Fatalf("places sent while locked: %+v", abs)
	}
	want := []input.Event{
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 3, DY: -4}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft, Pressed: true}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft}},
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DY: input.WheelNotch}},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sent %+v\nwant %+v", got, want)
	}
}

// TestClickOnPictureRelocks: after a release, a click on the bars does
// nothing; a click on the picture takes the lock and stays with the
// client.
func TestClickOnPictureRelocks(t *testing.T) {
	l, sys, tt, take := lockedForwarder(t)
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	l.setLocked(false)
	tt.Frame()
	take()
	tt.PressButton(0, 20, 200)
	tt.Frame()
	tt.ReleaseButton(0, 20, 200)
	tt.Frame()
	if sys.mode != pointerFree || l.locked() {
		t.Fatal("a click on a bar took the lock")
	}
	tt.PressButton(0, 400, 200)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	tt.Frame()
	if sys.mode != pointerRelative {
		t.Fatal("a click on the picture did not take the lock")
	}
	if b := ofType(take(), input.TypeMouseButton); len(b) != 0 {
		t.Fatalf("the click that took the lock reached the host: %+v", b)
	}
}

// TestFocusLossFreesPointer: the window losing the focus gives the
// pointer back at once.
func TestFocusLossFreesPointer(t *testing.T) {
	l, sys, tt, _ := lockedForwarder(t)
	l.setHost(wire.CursorState{Captured: true})
	tt.Frame()
	tt.SetWindowActive(false)
	tt.Frame()
	if sys.mode != pointerFree || sys.hides != 0 {
		t.Fatalf("focus left: held %d, hidden %d", sys.mode, sys.hides)
	}
	tt.SetWindowActive(true)
	tt.Frame()
	if sys.mode != pointerFree {
		t.Fatal("the focus coming back took the lock without a click")
	}
}

// TestWheelOnPicture: unlocked, a notch over the picture scrolls the host
// a notch, down for down, and a touchpad's small moves add up.
func TestWheelOnPicture(t *testing.T) {
	_, tt, got := barsForwarder(1920, 1080)
	tt.Frame()
	tt.Scroll(400, 200, 0, wheelDIPs())
	tt.Frame()
	for range 3 {
		tt.Scroll(400, 200, wheelDIPs()/3, 0)
		tt.Frame()
	}
	want := []input.Event{
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DY: -input.WheelNotch}},
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DX: 40}},
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DX: 40}},
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DX: 40}},
	}
	if w := ofType(*got, input.TypeMouseWheel); !slices.Equal(w, want) {
		t.Fatalf("wheel %+v\nwant %+v", w, want)
	}
}

// TestLockKeyStaysWithClient: Ctrl+Alt+G locks and releases the cursor,
// and never reaches the host.
func TestLockKeyStaysWithClient(t *testing.T) {
	var got []input.Event
	f := &inputForwarder{send: func(ev input.Event) { got = append(got, ev) }}
	f.keyboard([]mui.Key{mui.KeyG}, mui.Ctrl|mui.Alt)
	f.keyboard(nil, mui.Ctrl|mui.Alt)
	f.keyboard(nil, 0)
	for _, ev := range got {
		if ev.Key.Usage == input.KeyG {
			t.Fatalf("Ctrl+Alt+G forwarded: %+v", ev)
		}
	}
}
