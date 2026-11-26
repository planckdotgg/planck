package main

import (
	"fmt"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// buttonLog is the mouse buttons in evs, as "L+", "R-".
func buttonLog(evs []input.Event) []string {
	names := map[input.Button]string{input.ButtonLeft: "L", input.ButtonRight: "R", input.ButtonMiddle: "M", input.ButtonX1: "X1", input.ButtonX2: "X2"}
	var out []string
	for _, ev := range evs {
		if ev.Type != input.TypeMouseButton {
			continue
		}
		s := names[ev.Button.Button] + "-"
		if ev.Button.Pressed {
			s = names[ev.Button.Button] + "+"
		}
		out = append(out, s)
	}
	return out
}

// chordStep presses (+) or releases (-) MyGo button b, with a frame after
// it unless the step is in the same frame as the next. The Tester runs a
// frame after each event it is given, so a step in the same frame goes to
// the view's observer as MyGo delivers it, with no frame.
type chordStep struct {
	button int
	down   bool
	same   bool
}

func runChord(f *inputForwarder, tt *mui.Tester, x, y float32, steps []chordStep) {
	for _, s := range steps {
		kind := mui.InputPointerUp
		if s.down {
			kind = mui.InputPointerDown
		}
		switch {
		case s.same:
			f.observe(mui.InputEvent{Kind: kind, Button: s.button, X: x, Y: y})
		case s.down:
			tt.PressButton(s.button, x, y)
		default:
			tt.ReleaseButton(s.button, x, y)
		}
	}
	tt.Frame()
}

// TestChordedButtonsReachHostIndependently: a second button pressed while
// another is held, as aiming down sights with the right button and firing
// with the left, goes down beside it, and each goes up as it is let go,
// in either order, unlocked over the picture and held relative alike.
func TestChordedButtonsReachHostIndependently(t *testing.T) {
	cases := []struct {
		name  string
		steps []chordStep
		want  []string
	}{
		{"ads then tap fire", []chordStep{{1, true, false}, {0, true, false}, {0, false, false}, {1, false, false}},
			[]string{"R+", "L+", "L-", "R-"}},
		{"ads then spray", []chordStep{{1, true, false}, {0, true, false}, {0, false, false}, {0, true, false}, {0, false, false}, {1, false, false}},
			[]string{"R+", "L+", "L-", "L+", "L-", "R-"}},
		{"fire then ads", []chordStep{{0, true, false}, {1, true, false}, {1, false, false}, {0, false, false}},
			[]string{"L+", "R+", "R-", "L-"}},
		{"crossed releases", []chordStep{{0, true, false}, {1, true, false}, {0, false, false}, {1, false, false}},
			[]string{"L+", "R+", "L-", "R-"}},
		{"three buttons", []chordStep{{1, true, false}, {0, true, false}, {2, true, false}, {0, false, false}, {2, false, false}, {1, false, false}},
			[]string{"R+", "L+", "M+", "L-", "M-", "R-"}},
		{"chord within one frame", []chordStep{{1, true, true}, {0, true, true}, {0, false, true}, {1, false, true}},
			[]string{"R+", "L+", "L-", "R-"}},
	}
	for _, c := range cases {
		t.Run("free/"+c.name, func(t *testing.T) {
			f, tt, got := barsForwarder(1920, 1080)
			tt.Move(400, 200)
			tt.Frame()
			runChord(f, tt, 400, 200, c.steps)
			if b := buttonLog(*got); !slices.Equal(b, c.want) {
				t.Fatalf("sent %v, want %v", b, c.want)
			}
		})
		t.Run("relative/"+c.name, func(t *testing.T) {
			f, tt, got := barsForwarder(1920, 1080)
			f.lock = newPointerLock(&fakePointer{t: t}, f.send)
			tt.Frame()
			f.lock.setHost(wire.CursorState{Captured: true})
			tt.Move(400, 200)
			tt.Frame()
			*got = nil
			runChord(f, tt, 400, 200, c.steps)
			if b := buttonLog(*got); !slices.Equal(b, c.want) {
				t.Fatalf("sent %v, want %v", b, c.want)
			}
		})
	}
}

// TestChordWithMotion: held relative, the mouse's motion goes on between
// the presses of a chord, in order, and moves no button.
func TestChordWithMotion(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Move(400, 200)
	tt.Frame()
	take()
	tt.PressButton(1, 400, 200)
	tt.Frame()
	l.motion(5, 0)
	tt.Frame()
	tt.PressButton(0, 400, 200)
	tt.Frame()
	l.motion(-2, 3)
	l.motion(0, 4)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	tt.Frame()
	l.motion(1, 1)
	tt.ReleaseButton(1, 400, 200)
	tt.Frame()
	var log []string
	for _, ev := range take() {
		switch ev.Type {
		case input.TypeMouseMoveRel:
			log = append(log, fmt.Sprintf("%d,%d", ev.MouseRel.DX, ev.MouseRel.DY))
		case input.TypeMouseButton:
			log = append(log, buttonLog([]input.Event{ev})...)
		default:
			t.Fatalf("unexpected %+v", ev)
		}
	}
	want := []string{"R+", "5,0", "L+", "-2,3", "0,4", "L-", "1,1", "R-"}
	if !slices.Equal(log, want) {
		t.Fatalf("sent %v, want %v", log, want)
	}
}

// fakeButtons is a buttonSys: held is what the system holds down; next
// the button MyGo's button 2 is.
type fakeButtons struct {
	held    map[input.Button]bool
	next    input.Button
	started int
}

func (s *fakeButtons) start() { s.started++ }

func (s *fakeButtons) other() (input.Button, bool) { return s.next, s.next != 0 }

func (s *fakeButtons) down(b input.Button) bool { return s.held[b] }

// TestChordReleasedOffTheWindow: a chord dragged off the window and let go
// there, last button first, which MyGo reports only for the button
// pressed last, lets go of the other as the system says it is up, not
// before.
func TestChordReleasedOffTheWindow(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakeButtons{held: map[input.Button]bool{}}
	f.btnSys = sys
	tt.Move(400, 200)
	tt.Frame()
	sys.held[input.ButtonRight], sys.held[input.ButtonLeft] = true, true
	tt.PressButton(1, 400, 200)
	tt.Frame()
	tt.PressButton(0, 400, 200)
	tt.Frame()
	tt.Move(900, 200)
	tt.Frame()
	sys.held[input.ButtonLeft] = false
	tt.ReleaseButton(0, 900, 200)
	tt.Frame()
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"R+", "L+", "L-"}) {
		t.Fatalf("right still held off the window: sent %v", b)
	}
	sys.held[input.ButtonRight] = false
	tt.ReleaseButton(1, 900, 200)
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"R+", "L+", "L-", "R-"}) {
		t.Fatalf("sent %v, want the right let go too", b)
	}
	if sys.started != 1 {
		t.Fatalf("button system started %d times", sys.started)
	}
}

// TestFocusLossLetsGoOfButtons: the window losing the focus with buttons
// down lets go of every one on the host, and their releases later send
// nothing more.
func TestFocusLossLetsGoOfButtons(t *testing.T) {
	l, _, tt, take := lockedForwarder(t)
	tt.Frame()
	l.setHost(wire.CursorState{Captured: true})
	tt.Move(400, 200)
	tt.Frame()
	take()
	tt.PressButton(1, 400, 200)
	tt.PressButton(0, 400, 200)
	tt.PressButton(2, 400, 200)
	tt.Frame()
	tt.SetWindowActive(false)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	tt.ReleaseButton(1, 400, 200)
	tt.ReleaseButton(2, 400, 200)
	tt.Frame()
	if b := buttonLog(take()); !slices.Equal(b, []string{"R+", "L+", "M+", "L-", "R-", "M-"}) {
		t.Fatalf("sent %v", b)
	}
	tt.PressButton(0, 400, 200)
	tt.Frame()
	if b := buttonLog(take()); len(b) != 0 {
		t.Fatalf("a press in an inactive window sent %v", b)
	}
}

// TestSideButtonsOnMac: macOS reports every button past the right as
// MyGo's button 2; the system's button number tells the middle and the
// side buttons apart, and they chord with the rest.
func TestSideButtonsOnMac(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakeButtons{held: map[input.Button]bool{}}
	f.btnSys = sys
	tt.Move(400, 200)
	tt.Frame()
	press := func(b input.Button, down bool) {
		sys.next, sys.held[b] = b, down
		if down {
			tt.PressButton(2, 400, 200)
		} else {
			tt.ReleaseButton(2, 400, 200)
		}
		tt.Frame()
	}
	sys.held[input.ButtonRight] = true
	tt.PressButton(1, 400, 200)
	tt.Frame()
	press(input.ButtonX1, true)
	press(input.ButtonMiddle, true)
	press(input.ButtonX2, true)
	sys.held[input.ButtonLeft] = true
	tt.PressButton(0, 400, 200)
	tt.Frame()
	press(input.ButtonX1, false)
	sys.held[input.ButtonLeft] = false
	tt.ReleaseButton(0, 400, 200)
	tt.Frame()
	press(input.ButtonX2, false)
	press(input.ButtonMiddle, false)
	sys.held[input.ButtonRight] = false
	tt.ReleaseButton(1, 400, 200)
	tt.Frame()
	want := []string{"R+", "X1+", "M+", "X2+", "L+", "X1-", "L-", "X2-", "M-", "R-"}
	if b := buttonLog(*got); !slices.Equal(b, want) {
		t.Fatalf("sent %v, want %v", b, want)
	}
}

// TestSideButtonsFromRawInput: Windows's side buttons, which come from raw
// input on another thread, go to the host while the window is active and
// the pointer on the picture or held relative, chorded with the rest, and
// a press elsewhere goes nowhere.
func TestSideButtonsFromRawInput(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	notified := 0
	f.notify = func() { notified++ }
	tt.Move(20, 200)
	tt.Frame()
	f.sideButton(input.ButtonX1, true)
	f.sideButton(input.ButtonX1, false)
	tt.Frame()
	if b := buttonLog(*got); len(b) != 0 {
		t.Fatalf("a side button over a bar sent %v", b)
	}
	tt.Move(400, 200)
	tt.Frame()
	tt.PressButton(1, 400, 200)
	tt.Frame()
	done := make(chan struct{})
	go func() {
		f.sideButton(input.ButtonX2, true)
		close(done)
	}()
	<-done
	tt.Frame()
	tt.PressButton(0, 400, 200)
	tt.Frame()
	tt.ReleaseButton(0, 400, 200)
	f.sideButton(input.ButtonX2, false)
	tt.ReleaseButton(1, 400, 200)
	tt.Frame()
	want := []string{"R+", "X2+", "L+", "L-", "X2-", "R-"}
	if b := buttonLog(*got); !slices.Equal(b, want) {
		t.Fatalf("sent %v, want %v", b, want)
	}
	// The bar's X1 press and release, which its frame drops, and X2's.
	if notified != 4 {
		t.Fatalf("asked for %d frames, want one per side button queued", notified)
	}
	f.sideButton(input.ButtonX1, true)
	tt.SetWindowActive(false)
	tt.Frame()
	f.streamEnded()
	f.sideButton(input.ButtonX2, true)
	tt.Frame()
	if b := buttonLog(*got); len(b) != len(want) {
		t.Fatalf("after focus loss and the stream's end: %v", b[len(want):])
	}
}

// TestReleaseMissedBetweenFrames: the right let go off the window and the
// pointer back over the view before a frame finds it away still lets go
// of the right on the host.
func TestReleaseMissedBetweenFrames(t *testing.T) {
	f, tt, got := barsForwarder(1920, 1080)
	sys := &fakeButtons{held: map[input.Button]bool{input.ButtonRight: true, input.ButtonLeft: true}}
	f.btnSys = sys
	tt.Move(400, 200)
	tt.PressButton(1, 400, 200)
	tt.PressButton(0, 400, 200)
	sys.held[input.ButtonLeft] = false
	tt.ReleaseButton(0, 400, 200)
	sys.held[input.ButtonRight] = false
	tt.Move(410, 200)
	tt.Frame()
	if b := buttonLog(*got); !slices.Equal(b, []string{"R+", "L+", "L-", "R-"}) {
		t.Fatalf("sent %v, want the right let go", b)
	}
}
