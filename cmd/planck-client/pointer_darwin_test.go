package main

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"

	"github.com/egoist/mygo"
)

// fakeCG stands in for the CoreGraphics calls macPointer makes, which
// must never move the cursor in a test. It records them in order.
type fakeCG struct {
	at    cgPoint
	calls []string
	// warpErr is the CGError warps return; one but 0 leaves the cursor.
	warpErr int32
	// uptime is the clock's seconds since boot.
	uptime float64
}

func (f *fakeCG) install(t *testing.T) {
	warp, assoc := cgWarpMouseCursorPosition, cgAssociateMouseAndPosition
	t.Cleanup(func() { cgWarpMouseCursorPosition, cgAssociateMouseAndPosition = warp, assoc })
	cgWarpMouseCursorPosition = func(at cgPoint) int32 {
		f.calls = append(f.calls, fmt.Sprintf("warp %g,%g", at.X, at.Y))
		if f.warpErr != 0 {
			return f.warpErr
		}
		f.at = at
		return 0
	}
	cgAssociateMouseAndPosition = func(connected uint32) int32 {
		f.calls = append(f.calls, [...]string{"dissociate", "associate"}[connected])
		return 0
	}
}

func fakeMacPointer(f *fakeCG, sent *[][2]float64) *macPointer {
	if f.uptime == 0 {
		f.uptime = 1000
	}
	return &macPointer{
		monitor:  1,
		win:      func() *mygo.Window { return nil },
		cursorAt: func() cgPoint { return f.at },
		uptime:   func() float64 { return f.uptime },
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
	warp := p.warpAt
	centre := cgPoint{2560, 720}
	p.relativeEvent(-1, 0, cgPoint{3497, 294}, true, warp-0.002)
	p.relativeEvent(-938, 426, centre, true, warp+0.007)
	p.relativeEvent(-1, 0, centre, true, warp+0.012)
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
	p.relativeEvent(5, 5, cgPoint{1280, 720}, true, p.warpAt+warpEventWait+0.001)
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
	p.relativeEvent(1503, 500, cgPoint{1500, 500}, true, p.warpAt+0.001)
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
	p.relativeEvent(4, 0, cgPoint{2560, 720}, true, p.warpAt+0.001)
	if want := [][2]float64{{4, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacQueuedEventAtCentreKeepsItsDelta: an event stamped before the
// warp that happens to lie at the warp's target is still the mouse's
// alone; the warp's distance comes off the first event stamped after it.
func TestMacQueuedEventAtCentreKeepsItsDelta(t *testing.T) {
	f := &fakeCG{at: cgPoint{3496, 294}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	warp := p.warpAt
	centre := cgPoint{2560, 720}
	p.relativeEvent(3, 1, cgPoint{2560.5, 720}, true, warp-0.001)
	p.relativeEvent(-938, 426, centre, true, warp+0.007)
	if want := [][2]float64{{3, 1}, {-2, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacFailedWarpTakesNothingOff: a warp CoreGraphics refuses moved
// nothing, so going relative reports no warp and the next event's delta
// goes as it came.
func TestMacFailedWarpTakesNothingOff(t *testing.T) {
	f := &fakeCG{at: cgPoint{2860, 820}, warpErr: 1001}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	if p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true}) {
		t.Fatal("hold reported a warp CoreGraphics refused")
	}
	p.relativeEvent(4, 0, cgPoint{2560, 720}, true, f.uptime)
	if want := [][2]float64{{4, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
	if p.free(spot{x: 100, y: 100, set: true}) {
		t.Fatal("free reported a warp CoreGraphics refused")
	}
}

// TestMacPointerWarpWithOpposingDeviceMotion: the mouse moving -6 against a
// +4 warp gives the first event stamped after it a delta of -2. It carries
// the warp, so the mouse's -6 goes, and the next event's +3 goes whole.
func TestMacPointerWarpWithOpposingDeviceMotion(t *testing.T) {
	f := &fakeCG{at: cgPoint{2556, 720}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	warp := p.warpAt
	centre := cgPoint{2560, 720}
	p.relativeEvent(-2, 0, centre, true, warp+0.007)
	p.relativeEvent(3, 0, centre, true, warp+0.012)
	if want := [][2]float64{{-6, 0}, {3, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacQueuedSmallMoveKeepsSmallWarp: QA's round-3 case. A +4 warp, then
// an event at the target stamped before it with the mouse's -1, then the
// warp-bearing +6. The queued event's delta looks like a small warp less
// the mouse's motion, but its stamp says it came first, so -1 goes whole
// and the +4 comes off the +6.
func TestMacQueuedSmallMoveKeepsSmallWarp(t *testing.T) {
	f := &fakeCG{at: cgPoint{2556, 720}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	warp := p.warpAt
	centre := cgPoint{2560, 720}
	p.relativeEvent(-1, 0, centre, true, warp-0.0005)
	p.relativeEvent(6, 0, centre, true, warp+0.006)
	if want := [][2]float64{{-1, 0}, {2, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacUnstampedEventTakesNow: an event with no stamp counts as stamped
// at the clock's now, after the warp.
func TestMacUnstampedEventTakesNow(t *testing.T) {
	f := &fakeCG{at: cgPoint{2556, 720}}
	f.install(t)
	var sent [][2]float64
	p := fakeMacPointer(f, &sent)
	p.hold(pointerRelative, rect{X: 1280, W: 2560, H: 1440}, spot{x: 2560, y: 720, set: true})
	f.uptime += 0.005
	p.relativeEvent(5, 0, cgPoint{2560, 720}, true, 0)
	if want := [][2]float64{{1, 0}}; !slices.Equal(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

// TestMacUptimeIsEventClock reads the real clocks, without moving the
// cursor: systemUptime is positive, advances, and fits inside the time
// since boot, and an NSEvent's timestamp reads back as the float it was
// made with.
func TestMacUptimeIsEventClock(t *testing.T) {
	a := systemUptime()
	time.Sleep(20 * time.Millisecond)
	b := systemUptime()
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		t.Fatal(err)
	}
	sinceBoot := time.Since(time.Unix(boot.Unix())).Seconds()
	if a <= 0 || b-a < 0.015 || b-a > 1 || b > sinceBoot+1 {
		t.Fatalf("uptime %v then %v, %v s since boot", a, b, sinceBoot)
	}
	ev := objc.ID(objc.GetClass("NSEvent")).Send(objc.RegisterName("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
		uint(15), nsPoint{}, uint(0), b, 0, objc.ID(0), int16(0), 0, 0)
	if ev == 0 {
		t.Fatal("no NSEvent")
	}
	if got := objc.Send[float64](ev, objc.RegisterName("timestamp")); got != b {
		t.Fatalf("timestamp %v, want %v", got, b)
	}
	t.Logf("systemUptime %.6f s, %.0f s since boot", b, sinceBoot)
}
