package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
)

// grab presses the floating button at its centre, so that the button's
// centre then follows the pointer.
func grab(t *testing.T, tt *mui.Tester) (float32, float32) {
	t.Helper()
	bx, by := centre(bounds(t, tt, "Stream controls"))
	tt.Move(bx, by)
	tt.Press(bx, by)
	tt.Frame()
	return bx, by
}

// moveTo moves the held pointer from (x0, y0) to (x1, y1) in steps, a 16 ms
// frame each, and calls each after every frame.
func moveTo(tt *mui.Tester, x0, y0, x1, y1 float32, steps int, each func()) {
	for i := 1; i <= steps; i++ {
		k := float32(i) / float32(steps)
		tt.Move(x0+(x1-x0)*k, y0+(y1-y0)*k)
		tt.Advance(16 * time.Millisecond)
		if each != nil {
			each()
		}
	}
}

// targetEdge is the edge the lit dock target sits on, from its tab's shape
// and place, or DockNone when none shows.
func targetEdge(tt *mui.Tester) DockEdge {
	info, ok := tt.Access("Dock target")
	if !ok {
		return DockNone
	}
	r := info.Bounds
	switch {
	case r.W == tabShort && r.X == 0:
		return DockLeft
	case r.W == tabShort:
		return DockRight
	case r.Y == 0:
		return DockTop
	}
	return DockBottom
}

// TestDockTargetStaysWhileHeldStill: a button held still in the dock zone
// keeps its target lit, frame after frame.
func TestDockTargetStaysWhileHeldStill(t *testing.T) {
	_, tt, _ := newStreamTester(t, streamFake())
	bx, by := grab(t, tt)
	moveTo(tt, bx, by, testWidth-40, 300, 20, nil)
	for i := range 20 {
		tt.Advance(50 * time.Millisecond)
		if !shows(tt, "Dock target") {
			t.Fatalf("held still %d ms in the dock zone: no dock target", (i+1)*50)
		}
	}
	tt.Release(testWidth-40, 300)
	tt.Frame()
}

// TestDockTargetSameMovingOrStill: the target lights from where the button
// is, so a button that stops short of the dock zone does not light one as
// it moves and lose it as it stops.
func TestDockTargetSameMovingOrStill(t *testing.T) {
	_, tt, _ := newStreamTester(t, streamFake())
	bx, by := grab(t, tt)
	x := float32(testWidth - dockReach - 30)
	moveTo(tt, bx, by, x, 300, 8, nil)
	moving := shows(tt, "Dock target")
	tt.Advance(500 * time.Millisecond)
	still := shows(tt, "Dock target")
	tt.Release(x, 300)
	tt.Frame()
	if moving != still || still {
		t.Fatalf("button %v px from the edge: target lit %v moving, %v still; want unlit both", testWidth-x, moving, still)
	}
}

// TestDockTargetFollowsTheButton: while the button moves fast along an
// edge, its target stays centred on it, not ahead of it.
func TestDockTargetFollowsTheButton(t *testing.T) {
	_, tt, _ := newStreamTester(t, streamFake())
	bx, by := grab(t, tt)
	moveTo(tt, bx, by, testWidth-40, 200, 10, nil)
	frames := 0
	moveTo(tt, testWidth-40, 200, testWidth-40, 700, 15, func() {
		_, ty := centre(bounds(t, tt, "Dock target"))
		_, cy := centre(bounds(t, tt, "Stream controls"))
		if d := ty - cy; d < -1 || d > 1 {
			t.Fatalf("dock target centred at y %v, button at %v", ty, cy)
		}
		frames++
	})
	tt.Release(testWidth-40, 700)
	tt.Frame()
	if frames != 15 {
		t.Fatalf("checked %d frames", frames)
	}
}

// TestDockTargetHoldsItsEdgeInACorner: a pointer wobbling where the left
// and top dock zones meet lights one edge, and keeps it.
func TestDockTargetHoldsItsEdgeInACorner(t *testing.T) {
	_, tt, _ := newStreamTester(t, streamFake())
	bx, by := grab(t, tt)
	moveTo(tt, bx, by, 52, 40, 20, nil)
	var seen []DockEdge
	for i := range 24 {
		x := float32(52)
		if i%2 == 0 {
			x = 60
		}
		tt.Move(x, 40)
		tt.Advance(16 * time.Millisecond)
		if e := targetEdge(tt); len(seen) == 0 || seen[len(seen)-1] != e {
			seen = append(seen, e)
		}
	}
	tt.Release(52, 40)
	tt.Frame()
	if len(seen) > 2 || seen[len(seen)-1] == DockNone {
		t.Fatalf("lit edges as the pointer wobbled in the corner: %v", seen)
	}
}

// TestDockLandsWhereTargetShows: let go mid-throw, the button docks in the
// tab its target showed, not where the throw would have carried it.
func TestDockLandsWhereTargetShows(t *testing.T) {
	a, tt, _ := newStreamTester(t, streamFake())
	bx, by := grab(t, tt)
	moveTo(tt, bx, by, testWidth-40, 250, 20, nil)
	moveTo(tt, testWidth-40, 250, testWidth-40, 330, 5, nil)
	want := bounds(t, tt, "Dock target")
	tt.Release(testWidth-40, 330)
	tt.Frame()
	tt.Advance(time.Second)
	tab, _, _ := a.ovl.dockGeom()
	if a.ovl.dock != DockRight || tab != want {
		t.Fatalf("docked %v in %+v, the target showed right at %+v", a.ovl.dock, tab, want)
	}
}

// TestDockCandidateHysteresis: the edge lit keeps the lead until another
// is clearly nearer or it falls well out of reach.
func TestDockCandidateHysteresis(t *testing.T) {
	const vw, vh = 1440, 900
	for _, c := range []struct {
		name   string
		cx, cy float32
		prev   DockEdge
		want   DockEdge
	}{
		{"fresh corner: left first", 40, 40, DockNone, DockLeft},
		{"top held against a slightly nearer left", 45, 50, DockTop, DockTop},
		{"left clearly nearer than top", 30, 50, DockTop, DockLeft},
		{"left held past its reach", dockReach + dockHysteresis - 1, 450, DockLeft, DockLeft},
		{"left let go well past its reach", dockReach + dockHysteresis + 1, 450, DockLeft, DockNone},
		{"right, fresh", vw - 30, 450, DockNone, DockRight},
		{"bottom, fresh", 700, vh - 30, DockNone, DockBottom},
	} {
		if got, _ := dockCandidate(c.cx, c.cy, vw, vh, c.prev); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
