package main

import (
	"fmt"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// pointerLog is the absolute moves and the buttons in evs, as "@x" and
// "L+".
func pointerLog(evs []input.Event) []string {
	var out []string
	for _, ev := range evs {
		switch ev.Type {
		case input.TypeMouseMoveAbs:
			out = append(out, fmt.Sprintf("@%d", ev.MouseAbs.X))
		case input.TypeMouseButton:
			out = append(out, buttonLog([]input.Event{ev})...)
		}
	}
	return out
}

// TestCancelLetsGoOfEveryButton: the system taking a chord's press, which
// MyGo reports as one canceled release, lets go of every button held on
// the host, not the last pressed alone.
func TestCancelLetsGoOfEveryButton(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakeButtons{held: map[input.Button]bool{}}
	f.btnSys = sys
	tt.Move(400, 200)
	tt.Frame()
	sys.held[input.ButtonRight], sys.held[input.ButtonLeft] = true, true
	tt.PressButton(1, 400, 200)
	tt.PressButton(0, 400, 200)
	tt.CancelPress()
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"R+", "L+", "L-", "R-"}) {
		t.Fatalf("sent %v, want both let go", b)
	}
}

// TestReleaseMissedOffTheWindowFoundOnReentry: a button let go off the
// window, where the view hears nothing of it, goes up as the pointer comes
// back, though no frame ran while it was away.
func TestReleaseMissedOffTheWindowFoundOnReentry(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakeButtons{held: map[input.Button]bool{}}
	f.btnSys = sys
	tt.Move(400, 200)
	tt.Frame()
	sys.held[input.ButtonRight], sys.held[input.ButtonLeft] = true, true
	tt.PressButton(1, 400, 200)
	tt.PressButton(0, 400, 200)
	tt.Move(900, 200)
	sys.held[input.ButtonLeft] = false
	tt.ReleaseButton(0, 900, 200)
	sys.held[input.ButtonRight] = false
	tt.Move(400, 200)
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"R+", "L+", "L-", "R-"}) {
		t.Fatalf("sent %v, want the right let go on reentry", b)
	}
}

// TestButtonLandsWhereItWasPressed: a press queued before the pointer
// moves on, both in one frame, goes to the host at the press's place, and
// the move after it.
func TestButtonLandsWhereItWasPressed(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	tt.Move(300, 200)
	tt.Frame()
	tt.PressButton(1, 300, 200)
	tt.Frame()
	*got = nil
	f.observe(mui.InputEvent{Kind: mui.InputPointerDown, Button: 0, X: 400, Y: 200})
	tt.Move(600, 200)
	tt.Frame()
	at := func(x float32) string {
		nx, _ := mapPointer(x, 200, 800, 400, 1920, 1080, 1600, 800)
		return fmt.Sprintf("@%d", nx)
	}
	if l, want := pointerLog(*got), []string{at(400), "L+", at(600)}; !slices.Equal(l, want) {
		t.Fatalf("sent %v, want %v", l, want)
	}
}

// hudForwarder is barsForwarder's view with a control of the overlay over
// the picture, from 300,100 to 400,150.
func hudForwarder() (*inputForwarder, *mui.Tester, *[]input.Event) {
	got := &[]input.Event{}
	f := &inputForwarder{send: func(ev input.Event) { *got = append(*got, ev) },
		video: func() (int, int, int, int) { return 1920, 1080, 1600, 800 }}
	tt := mui.NewTester(func(c *mui.Context) {
		mui.Box(c).Fill().Children(func() {
			f.view(c)
			mui.Box(c).Absolute().Left(300).Top(100).Size(100, 50).Label("hud").HandleInput(func(mui.InputEvent) bool { return true })
		})
	}, 800, 400)
	return f, tt, got
}

// TestSideButtonOnEnteringHUDStaysLocal: a side button clicked from raw
// input as the pointer comes onto the overlay, before a frame finds it
// there, does not reach the host; one clicked on the picture does.
func TestSideButtonOnEnteringHUDStaysLocal(t *testing.T) {
	f, tt, got := hudForwarder()
	tt.Move(350, 200)
	tt.Frame()
	f.sideButton(input.ButtonX1, true)
	f.sideButton(input.ButtonX1, false)
	tt.Move(350, 120)
	tt.Frame()
	if b := buttonLog(*got); len(b) != 0 {
		t.Fatalf("a side click on the overlay sent %v", b)
	}
	tt.Move(350, 200)
	tt.Frame()
	f.sideButton(input.ButtonX1, true)
	tt.Frame()
	tt.Move(350, 120)
	f.sideButton(input.ButtonX1, false)
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"X1+", "X1-"}) {
		t.Fatalf("sent %v, want the click on the picture, let go over the overlay", b)
	}
}
