package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

type overlayLog struct{ states []OverlayState }

func (l *overlayLog) last(t *testing.T) OverlayState {
	t.Helper()
	if len(l.states) == 0 {
		t.Fatal("no overlay change heard")
	}
	return l.states[len(l.states)-1]
}

func near32(a, b float32) bool { return a-b < 1e-4 && b-a < 1e-4 }

// newRestTester is newStreamTester listening to the overlay from the
// first frame.
func newRestTester(t *testing.T, f *fakeBackend) (*App, *mui.Tester, *overlayLog) {
	t.Helper()
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	a := NewApp(f)
	a.goos = "windows"
	a.SetStreamView((&streamProbe{}).view)
	log := &overlayLog{}
	a.OnOverlayChange(func(s OverlayState) { log.states = append(log.states, s) })
	return a, mui.NewTester(a.View, testWidth, testHeight), log
}

// TestRestingButtonKeepsDirect: the floating button neither hovered nor
// open is all the overlay draws once the first-stream tip goes, so it
// reports its shapes, at full opacity and then dimmed, and is not live;
// hovering it makes it live, and it rests again once the pointer leaves.
func TestRestingButtonKeepsDirect(t *testing.T) {
	_, tt, log := newRestTester(t, streamFake())
	tt.Move(900, 700)
	tt.Frame()
	if s := log.last(t); !s.Live() {
		t.Fatalf("first-stream tip showing: state %+v, want live", s)
	}
	btn := bounds(t, tt, "Stream controls")
	glass := colorOf(theme.HUDGlassIdle, 1)
	check := func(step string, opacity float32) {
		t.Helper()
		s := log.last(t)
		if !s.Shown || s.Live() || s.Rest == nil {
			t.Fatalf("%s: state %+v, want shown at rest", step, s)
		}
		r := s.Rest
		if r.ViewW != testWidth || r.ViewH != testHeight || len(r.Boxes) != 1 {
			t.Fatalf("%s: rest %+v", step, r)
		}
		b := r.Boxes[0]
		if b.RestRect != (RestRect{X: btn.X, Y: btn.Y, W: btnSize, H: btnSize}) || b.Radius != 12 || !near32(b.Color[3], glass[3]*opacity) {
			t.Fatalf("%s: button box %+v, want the button at %+v, radius 12, alpha %v", step, b, btn, glass[3]*opacity)
		}
		if r.Mark != (RestRect{X: btn.X + 15.5, Y: btn.Y + 12, W: 13, H: 20}) || !near32(r.MarkOpacity, markIdleOpacity*opacity) {
			t.Fatalf("%s: mark %+v at %v", step, r.Mark, r.MarkOpacity)
		}
	}
	tt.Advance(tipFirst + 100*time.Millisecond)
	check("tip gone", 1)
	tt.Advance(idleTime + dimTime)
	check("idle, dimmed", 1-0.65)

	tt.Move(btn.X+btn.W+40, btn.Y)
	tt.Frame()
	check("pointer near", 1)
	tt.Move(centre(btn))
	tt.Frame()
	if s := log.last(t); !s.Live() {
		t.Fatalf("button hovered: state %+v, want live", s)
	}
	tt.Move(900, 700)
	tt.Advance(idleTime + dimTime + 100*time.Millisecond)
	check("idle again", 1-0.65)
}

// TestRestingTabKeepsDirect: the docked tab rests from the start; its
// glass runs past the edge so that only its inner corners round, with the
// ember slice centred. The menu and the metrics window are live.
func TestRestingTabKeepsDirect(t *testing.T) {
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, Dock: DockLeft, DockFrac: 0.5}
	a, tt, log := newRestTester(t, f)
	tt.Move(900, 700)
	tt.Frame()
	s := log.last(t)
	if s.Live() || s.Rest == nil || len(s.Rest.Boxes) != 2 || s.Rest.Mark.W != 0 {
		t.Fatalf("docked tab: state %+v, want at rest with two boxes and no mark", s)
	}
	tab, _, _ := a.ovl.dockGeom()
	glass, slice := s.Rest.Boxes[0], s.Rest.Boxes[1]
	if glass.RestRect != (RestRect{X: tab.X - 6, Y: tab.Y, W: tab.W + 6, H: tab.H}) || glass.Radius != 6 || glass.Color != colorOf(theme.HUDGlassTab, 1) {
		t.Fatalf("tab glass %+v for tab %+v", glass, tab)
	}
	if slice.RestRect != (RestRect{X: tab.X + 3, Y: tab.Y + 12, W: 2, H: 16}) || slice.Color != colorOf(theme.RoleAccent, 1) {
		t.Fatalf("ember slice %+v for tab %+v", slice, tab)
	}

	tt.Key(mui.Ctrl|mui.Alt, mui.KeyO)
	tt.Frame()
	tt.Advance(popOutTime + 20*time.Millisecond)
	if s := log.last(t); !s.Live() {
		t.Fatalf("menu open: state %+v, want live", s)
	}
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	tt.Advance(lingerTime + popBack + 100*time.Millisecond)
	if s := log.last(t); s.Live() || s.Rest == nil {
		t.Fatalf("menu closed, tab back: state %+v, want at rest", s)
	}

	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	tt.Frame()
	if s := log.last(t); !s.Live() {
		t.Fatalf("metrics window: state %+v, want live", s)
	}
}
