package ui

import (
	"testing"

	mui "github.com/egoist/mygo/ui"
)

// TestStreamFullscreenEndsWithTheStream: full screen the stream turned on
// ends with it, however it ends; full screen from before the stream, or
// one the user already left, stays as it is.
func TestStreamFullscreenEndsWithTheStream(t *testing.T) {
	disconnectButton := func(t *testing.T, _ *fakeBackend, tt *mui.Tester) {
		openMenu(t, tt)
		click(t, tt, "Disconnect")
	}
	hostDrop := func(_ *testing.T, f *fakeBackend, tt *mui.Tester) {
		f.set(func(f *fakeBackend) { f.tel = Telemetry{} })
		tt.Frame()
	}
	shortcut := func(_ *testing.T, _ *fakeBackend, tt *mui.Tester) {
		tt.Key(mui.Ctrl|mui.Alt|mui.Shift, mui.KeyD)
		tt.Frame()
	}
	menuFullscreen := func(t *testing.T, _ *fakeBackend, tt *mui.Tester, _ *fakeWindow) {
		openMenu(t, tt)
		click(t, tt, "Fullscreen")
		tt.Key(0, mui.KeyEscape)
		tt.Frame()
	}
	keyFullscreen := func(_ *testing.T, _ *fakeBackend, tt *mui.Tester, _ *fakeWindow) {
		tt.Key(mui.Ctrl|mui.Alt, mui.KeyF)
		tt.Frame()
	}
	userLeftAndCameBack := func(t *testing.T, f *fakeBackend, tt *mui.Tester, w *fakeWindow) {
		menuFullscreen(t, f, tt, w)
		w.full = false
		tt.Frame()
		w.full = true
		tt.Frame()
	}
	for _, c := range []struct {
		name     string
		before   bool
		during   func(*testing.T, *fakeBackend, *mui.Tester, *fakeWindow)
		end      func(*testing.T, *fakeBackend, *mui.Tester)
		wantFull bool
	}{
		{"menu, Disconnect button", false, menuFullscreen, disconnectButton, false},
		{"menu, host drop", false, menuFullscreen, hostDrop, false},
		{"Ctrl+Alt+F, Ctrl+Alt+Shift+D", false, keyFullscreen, shortcut, false},
		{"full screen before the stream, Disconnect button", true, nil, disconnectButton, true},
		{"full screen before the stream, host drop", true, nil, hostDrop, true},
		{"user left full screen, then went back on their own", false, userLeftAndCameBack, disconnectButton, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := streamFake()
			f.tel = Telemetry{}
			a, tt, _ := newStreamTester(t, f)
			w := &fakeWindow{full: c.before}
			a.SetWindow(w)
			tt.Frame()
			f.set(func(f *fakeBackend) { f.tel = sampleTelemetry(); f.tel.Profile = sampleProfile() })
			tt.Frame()
			if !a.streaming {
				t.Fatal("stream did not start")
			}
			if c.during != nil {
				c.during(t, f, tt, w)
				if !w.full {
					t.Fatal("not full screen during the stream")
				}
			}
			c.end(t, f, tt)
			if a.streaming {
				t.Fatal("stream did not end")
			}
			if w.full != c.wantFull {
				t.Fatalf("after the stream: full screen %v, want %v", w.full, c.wantFull)
			}
		})
	}
}

// TestStreamFullscreenLeftByUserIsForgotten: full screen the stream turned
// on and the user turned off is not left again when the stream ends, nor
// by the next stream's end.
func TestStreamFullscreenLeftByUserIsForgotten(t *testing.T) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	w := &fakeWindow{}
	a.SetWindow(w)
	openMenu(t, tt)
	click(t, tt, "Fullscreen")
	click(t, tt, "Fullscreen")
	if w.full {
		t.Fatal("the switch did not leave full screen")
	}
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	f.Disconnect()
	tt.Frame()
	w.full = true
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	tt.Frame()
	f.Disconnect()
	tt.Frame()
	if !w.full {
		t.Fatal("a stream left full screen it never turned on")
	}
}

// slowWindow goes full screen, or leaves it, only when the test says the
// animation ended, as a macOS window does.
type slowWindow struct{ full, want bool }

func (w *slowWindow) Fullscreen() bool      { return w.full }
func (w *slowWindow) SetFullscreen(on bool) { w.want = on }

// TestStreamFullscreenWaitsForTheAnimation: frames built while the window
// is still on its way to full screen do not count as the user leaving it.
func TestStreamFullscreenWaitsForTheAnimation(t *testing.T) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	w := &slowWindow{}
	a.SetWindow(w)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyF)
	for range 5 {
		tt.Frame()
	}
	w.full = w.want
	tt.Frame()
	f.Disconnect()
	tt.Frame()
	if w.want {
		t.Fatal("the stream's end did not leave the full screen it turned on")
	}
}
