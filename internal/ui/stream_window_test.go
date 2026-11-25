package ui

import (
	"context"
	"errors"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// fakeStreamWindow records what the app asks of the stream's window.
type fakeStreamWindow struct {
	open   bool
	opens  []string
	closes int
}

func (w *fakeStreamWindow) OpenStreamWindow(host string) {
	w.open = true
	w.opens = append(w.opens, host)
}
func (w *fakeStreamWindow) CloseStreamWindow()     { w.open = false; w.closes++ }
func (w *fakeStreamWindow) StreamWindowOpen() bool { return w.open }

// newSplitTester mounts the launcher and the stream's window of one app.
func newSplitTester(t *testing.T, b Backend) (*App, *fakeStreamWindow, *mui.Tester, *mui.Tester) {
	t.Helper()
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	a := NewApp(b)
	sw := &fakeStreamWindow{}
	a.SetStreamWindow(sw)
	a.SetStreamView(func(c *mui.Context) { mui.Box(c).Fill().Label("Video") })
	return a, sw, mui.NewTester(a.View, testWidth, testHeight), mui.NewTester(a.StreamView, testWidth, testHeight)
}

// streamUntil runs frames of both windows until the app is streaming.
func streamUntil(t *testing.T, a *App, launcher, stream *mui.Tester) {
	t.Helper()
	eventually(t, launcher, func() bool {
		stream.Frame()
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.connecting == "" && a.streaming
	})
	launcher.Frame()
	stream.Frame()
}

// TestStreamWindowOpensOnConnect: connecting opens the stream's own
// window, which shows the connect and then the stream, while the launcher
// stays on Hosts.
func TestStreamWindowOpensOnConnect(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, sw, launcher, stream := newSplitTester(t, f)

	click(t, launcher, "Connect to bigboi")
	if !slices.Equal(sw.opens, []string{"bigboi"}) {
		t.Fatalf("stream windows opened: %q, want bigboi's", sw.opens)
	}
	if launcher.HasText("Connecting to bigboi") || !launcher.HasText("Hosts") {
		t.Fatalf("launcher while connecting: %q", launcher.Texts())
	}
	stream.Frame()
	if !stream.HasText("Connecting to bigboi") {
		t.Fatalf("stream window while connecting: %q", stream.Texts())
	}

	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	f.connectResult <- nil
	streamUntil(t, a, launcher, stream)
	stream.Frame()
	if _, ok := stream.Access("Stream controls"); !ok {
		t.Fatalf("overlay missing from the stream window: %q", stream.Texts())
	}
	if _, ok := stream.Access("Video"); !ok {
		t.Fatal("stream view missing from the stream window")
	}
	if _, ok := launcher.Access("Stream controls"); ok || !launcher.HasText("Hosts") {
		t.Fatalf("launcher while streaming: %q", launcher.Texts())
	}
	if sw.closes != 0 || len(sw.opens) != 1 {
		t.Fatalf("opens %q, closes %d while streaming", sw.opens, sw.closes)
	}
}

// TestStreamWindowClosesWithTheStream: however the stream ends, its
// window closes and the launcher shows Hosts.
func TestStreamWindowClosesWithTheStream(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(t *testing.T, f *fakeBackend, stream *mui.Tester)
	}{
		{"Disconnect button", func(t *testing.T, _ *fakeBackend, stream *mui.Tester) {
			openMenu(t, stream)
			click(t, stream, "Disconnect")
		}},
		{"Ctrl+Alt+Shift+D", func(_ *testing.T, _ *fakeBackend, stream *mui.Tester) {
			stream.Key(mui.Ctrl|mui.Alt|mui.Shift, mui.KeyD)
			stream.Frame()
		}},
		{"host drop", func(_ *testing.T, f *fakeBackend, _ *mui.Tester) {
			f.set(func(f *fakeBackend) { f.tel = Telemetry{} })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.hosts = sampleHosts()
			a, sw, launcher, stream := newSplitTester(t, f)
			cursor := &fakeCursor{}
			a.SetCursor(cursor)
			click(t, launcher, "Connect to bigboi")
			f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
			f.connectResult <- nil
			streamUntil(t, a, launcher, stream)

			c.end(t, f, stream)
			launcher.Frame()
			stream.Frame()
			if a.streaming || sw.open || sw.closes != 1 {
				t.Fatalf("after the stream: streaming %v, window open %v, closes %d", a.streaming, sw.open, sw.closes)
			}
			if a.screen != ScreenHosts || !launcher.HasText("Hosts") {
				t.Fatalf("launcher after the stream: %v %q", a.screen, launcher.Texts())
			}
			if cursor.ended == 0 {
				t.Fatal("the mouse lock outlived the stream")
			}
		})
	}
}

// TestStreamWindowFailedConnect: a connect that fails closes the window
// and shows its error on Hosts; one that needs pairing opens the pairing
// panel there.
func TestStreamWindowFailedConnect(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, sw, launcher, _ := newSplitTester(t, f)
	click(t, launcher, "Connect to bigboi")
	f.connectResult <- errors.New("host refused the session")
	eventually(t, launcher, func() bool { return launcher.HasText("Could not connect: host refused the session") })
	if sw.open || sw.closes != 1 {
		t.Fatalf("window open %v, closes %d after a failed connect", sw.open, sw.closes)
	}

	click(t, launcher, "Connect to cabin-pc")
	if len(sw.opens) != 1 {
		t.Fatalf("Pair & Connect opened a stream window: %q", sw.opens)
	}
	f.set(func(f *fakeBackend) { f.pair = PairState{Status: PairPending, HostName: "cabin-pc"} })
	f.connectResult <- ErrPairingRequired
	eventually(t, launcher, func() bool {
		_, ok := launcher.Access("PIN")
		return ok && a.screen == ScreenHosts
	})
	if sw.open || sw.closes != 1 {
		t.Fatalf("window open %v, closes %d when pairing is needed", sw.open, sw.closes)
	}
}

// TestStreamWindowClosedByUser: closing the stream's window ends the
// stream, or the connect under way, and the launcher stays open.
func TestStreamWindowClosedByUser(t *testing.T) {
	t.Run("streaming", func(t *testing.T) {
		f := newFake()
		f.hosts = sampleHosts()
		a, sw, launcher, stream := newSplitTester(t, f)
		click(t, launcher, "Connect to bigboi")
		f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
		f.connectResult <- nil
		streamUntil(t, a, launcher, stream)

		sw.open = false
		a.StreamWindowClosed()
		launcher.Frame()
		if f.disconnects != 1 || a.streaming {
			t.Fatalf("disconnects %d, streaming %v after the window closed", f.disconnects, a.streaming)
		}
		if len(sw.opens) != 1 || !launcher.HasText("Hosts") {
			t.Fatalf("opens %q, launcher %q", sw.opens, launcher.Texts())
		}
	})
	t.Run("connecting", func(t *testing.T) {
		f := newFake()
		f.hosts = sampleHosts()
		a, sw, launcher, _ := newSplitTester(t, f)
		click(t, launcher, "Connect to bigboi")
		sw.open = false
		a.StreamWindowClosed()
		eventually(t, launcher, func() bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.connecting == ""
		})
		f.mu.Lock()
		defer f.mu.Unlock()
		if !errors.Is(f.connectCtxErr, context.Canceled) {
			t.Fatalf("connect context error %v, want canceled", f.connectCtxErr)
		}
		if launcher.HasText("Could not connect") {
			t.Fatal("closing the window shows as a failed connect")
		}
	})
	t.Run("connect succeeds anyway", func(t *testing.T) {
		f := newFake()
		f.hosts = sampleHosts()
		d := &deafConnect{fakeBackend: f, release: make(chan struct{})}
		a, sw, launcher, _ := newSplitTester(t, d)
		click(t, launcher, "Connect to bigboi")
		sw.open = false
		a.StreamWindowClosed()
		close(d.release)
		eventually(t, launcher, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.disconnects == 1 })
		launcher.Frame()
		if a.streaming || len(sw.opens) != 1 {
			t.Fatalf("streaming %v, opens %q after an abandoned connect", a.streaming, sw.opens)
		}
	})
}

// deafConnect connects once released, whatever its context says.
type deafConnect struct {
	*fakeBackend
	release chan struct{}
}

func (d *deafConnect) Connect(context.Context, string) error {
	<-d.release
	d.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	return nil
}

// TestStreamWindowHostClicks: Resume on the live host brings its window
// back without a new connect; another host's Connect switches the open
// window to it.
func TestStreamWindowHostClicks(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, sw, launcher, stream := newSplitTester(t, f)
	click(t, launcher, "Connect to bigboi")
	f.set(func(f *fakeBackend) {
		f.tel = sampleTelemetry()
		f.hosts[0].Connected = true
	})
	f.connectResult <- nil
	streamUntil(t, a, launcher, stream)

	click(t, launcher, "Resume bigboi")
	if !slices.Equal(sw.opens, []string{"bigboi", "bigboi"}) || len(f.connects) != 1 {
		t.Fatalf("Resume: opens %q, connects %q", sw.opens, f.connects)
	}

	click(t, launcher, "Connect to studio-mac")
	if !slices.Equal(sw.opens, []string{"bigboi", "bigboi", "studio-mac"}) || sw.closes != 0 {
		t.Fatalf("switch: opens %q, closes %d", sw.opens, sw.closes)
	}
	stream.Frame()
	if !stream.HasText("Connecting to studio-mac") {
		t.Fatalf("stream window while switching: %q", stream.Texts())
	}
	f.set(func(f *fakeBackend) { f.tel.HostName, f.tel.HostID = "studio-mac", "h-studio" })
	f.connectResult <- nil
	streamUntil(t, a, launcher, stream)
	stream.Frame()
	if _, ok := stream.Access("Stream controls"); !ok || sw.closes != 0 {
		t.Fatalf("after the switch: closes %d, stream window %q", sw.closes, stream.Texts())
	}
}

// TestStreamWindowFromPairing: a stream that starts from pairing, while
// the launcher shows another page, opens its window and leaves the
// launcher on Hosts.
func TestStreamWindowFromPairing(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPaired, HostName: "bigboi"}
	a, sw, launcher, _ := newSplitTester(t, f)
	a.screen = ScreenStatistics
	launcher.Frame()
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	launcher.Frame()
	if !slices.Equal(sw.opens, []string{"bigboi"}) || a.screen != ScreenHosts {
		t.Fatalf("opens %q, screen %v", sw.opens, a.screen)
	}
}

// TestStreamWindowFullscreenSwitch: the overlay's Fullscreen switch acts
// on the stream's window, and leaving full screen keeps the stream.
func TestStreamWindowFullscreenSwitch(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, sw, launcher, stream := newSplitTester(t, f)
	w := &fakeWindow{full: true}
	a.SetWindow(w)
	click(t, launcher, "Connect to bigboi")
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	f.connectResult <- nil
	streamUntil(t, a, launcher, stream)

	stream.Key(mui.Ctrl|mui.Alt, mui.KeyF)
	stream.Frame()
	launcher.Frame()
	if w.full || !a.streaming || !sw.open {
		t.Fatalf("after Ctrl+Alt+F: full %v, streaming %v, window open %v", w.full, a.streaming, sw.open)
	}
	stream.Key(mui.Ctrl|mui.Alt, mui.KeyF)
	stream.Frame()
	if !w.full {
		t.Fatal("Ctrl+Alt+F did not go back to full screen")
	}
}

// TestSettingsOpenFullscreen: the Settings switch saves the preference.
func TestSettingsOpenFullscreen(t *testing.T) {
	f := newFake()
	f.settings.OpenFullscreen = true
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	tt.Frame()
	clickScrolled(t, tt, "Open streams in full screen")
	if f.Settings().OpenFullscreen {
		t.Fatal("switch did not turn full screen off")
	}
	clickScrolled(t, tt, "Open streams in full screen")
	if !f.Settings().OpenFullscreen {
		t.Fatal("switch did not turn full screen back on")
	}
}
