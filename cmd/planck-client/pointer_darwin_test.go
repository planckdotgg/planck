package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// fakeCG stands in for the CoreGraphics calls macPointer makes, which
// must never move the cursor in a test. It records them in order.
type fakeCG struct {
	at    cgPoint
	calls []string
}

func (f *fakeCG) install(t *testing.T) {
	warp, assoc := cgWarpMouseCursorPosition, cgAssociateMouseAndPosition
	t.Cleanup(func() { cgWarpMouseCursorPosition, cgAssociateMouseAndPosition = warp, assoc })
	cgWarpMouseCursorPosition = func(at cgPoint) int32 {
		f.calls = append(f.calls, fmt.Sprintf("warp %g,%g", at.X, at.Y))
		f.at = at
		return 0
	}
	cgAssociateMouseAndPosition = func(connected uint32) int32 {
		f.calls = append(f.calls, [...]string{"dissociate", "associate"}[connected])
		return 0
	}
}

func fakeMacPointer(f *fakeCG, sent *[][2]float64) *macPointer {
	return &macPointer{
		monitor:  1,
		win:      func() *mygo.Window { return nil },
		cursorAt: func() cgPoint { return f.at },
		motion:   func(dx, dy float64) { *sent = append(*sent, [2]float64{dx, dy}) },
	}
}

// TestMacRelativeHoldDissociatesFirst: going relative, the cursor is
// dissociated before the warp to the picture's centre and never
// associated again, so the mouse's motion stays the system's deltas.
func TestMacRelativeHoldDissociatesFirst(t *testing.T) {
	f := &fakeCG{at: cgPoint{3496, 294}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	if want := []string{"dissociate", "warp 2560,720"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls %v, want %v", f.calls, want)
	}
}

// TestMacWarpDeltaNotSent: the traced spike. The cursor at 3496, 294 went
// relative with a warp to 2560, 720, and the next event, at the warp's
// target, carried -938, +426: the warp's -936, +426 and the mouse's -2, 0.
// Only the mouse's goes to the host, and events after it go as they come.
func TestMacWarpDeltaNotSent(t *testing.T) {
	f := &fakeCG{at: cgPoint{3496, 294}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	now := p.warpAt
	centre := cgPoint{2560, 720}
	p.relativeEvent(-1, 0, cgPoint{3497, 294}, true, now.Add(2*time.Millisecond))
	p.relativeEvent(-938, 426, centre, true, now.Add(7*time.Millisecond))
	p.relativeEvent(-1, 0, centre, true, now.Add(12*time.Millisecond))
	if want := [][2]float64{{-1, 0}, {-2, 0}, {-1, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v: the queued event, the mouse's part of the warp's, then the next", sent, want)
	}
}

// TestMacWarpDeltaExpires: an event at the target long after the warp,
// whose own event never came, is the mouse's alone.
func TestMacWarpDeltaExpires(t *testing.T) {
	f := &fakeCG{at: cgPoint{100, 100}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{W: 2560, H: 1440}, spot{x: 1280, y: 720, set: true})
	p.relativeEvent(5, 5, cgPoint{1280, 720}, true, p.warpAt.Add(warpEventWait+time.Millisecond))
	if want := [][2]float64{{5, 5}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacRecentreAddsUp: a second warp before the first's event, as a
// picture resize recentres, is carried by the same event.
func TestMacRecentreAddsUp(t *testing.T) {
	f := &fakeCG{at: cgPoint{0, 0}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{W: 2000, H: 1000}, spot{x: 1000, y: 500, set: true})
	p.hold(pointerRelative, rect{W: 3000, H: 1000}, spot{x: 1500, y: 500, set: true})
	p.relativeEvent(1503, 500, cgPoint{1500, 500}, true, p.warpAt.Add(time.Millisecond))
	if want := [][2]float64{{3, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacFreeAndConfinedAssociate: freeing, and confining with a warp,
// associate the cursor again after the warp, and leave no warp distance
// for a later relative hold to take off the mouse's motion.
func TestMacFreeAndConfinedAssociate(t *testing.T) {
	f := &fakeCG{at: cgPoint{2560, 720}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{W: 5120, H: 1440}, spot{x: 2560, y: 720, set: true})
	f.calls = nil
	p.free(spot{x: 4000, y: 300, set: true})
	if want := []string{"associate", "warp 4000,300", "associate"}; !slices.Equal(f.calls, want) {
		t.Fatalf("free calls %v, want %v", f.calls, want)
	}
	f.calls = nil
	p.hold(pointerConfined, rect{W: 5120, H: 1440}, spot{x: 100, y: 100, set: true})
	if want := []string{"warp 100,100", "associate"}; !slices.Equal(f.calls, want) {
		t.Fatalf("confined calls %v, want %v", f.calls, want)
	}
	f.calls = nil
	p.hold(pointerConfined, rect{W: 5120, H: 1440}, spot{})
	if want := []string{"associate"}; !slices.Equal(f.calls, want) {
		t.Fatalf("confined calls with no warp %v, want %v", f.calls, want)
	}
	f.at = cgPoint{2560, 720}
	p.hold(pointerRelative, rect{W: 5120, H: 1440}, spot{x: 2560, y: 720, set: true})
	p.relativeEvent(4, 0, cgPoint{2560, 720}, true, p.warpAt.Add(time.Millisecond))
	if want := [][2]float64{{4, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}
