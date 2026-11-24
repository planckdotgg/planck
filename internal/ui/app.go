// Package ui is Planck's desktop client interface, drawn with MyGo: the
// icon rail, the Hosts, Pairing and Settings screens, and the stream view
// with its overlay. It reads and drives a Backend and holds no networking
// of its own.
package ui

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// Screen is a rail destination.
type Screen int

const (
	ScreenHosts Screen = iota
	ScreenPairing
	ScreenSettings
)

// App is the client UI. Build it with NewApp and mount Content in a
// window. Its view runs on the main thread; Notify and the backend's
// change callback may run on any goroutine.
type App struct {
	backend    Backend
	theme      *mui.Theme
	goos       string
	streamView func(c *mui.Context)
	window     WindowHook
	// videoTheme is theme with a clear window background, for a stream
	// view whose video shows under the UI's layer.
	videoTheme *mui.Theme

	// Main-thread state, read and written only by the view.
	screen       Screen
	quickConnect string
	pin          string
	streaming    bool
	// ovl is the stream overlay; frameNow is the time of the frame being
	// built, for the overlay's key handlers.
	ovl      overlay
	frameNow time.Time
	// renaming is the HostID whose card shows the rename body; renameOpen
	// selects its text on the frame it opens.
	renaming   string
	renameText string
	renameOpen bool
	renameErr  string
	focusQuick bool
	// hostNameDraft is the Host name field while it has the focus; it is
	// saved on Enter or when the focus leaves.
	hostNameDraft   string
	hostNameFocused bool
	machineName     string
	// ovScope is whose stream format Settings edits; drafts are its
	// text fields.
	ovScope overrideScope
	drafts  drafts

	mu           sync.Mutex
	services     mui.Services
	haveServices bool
	connecting   string
	cancel       context.CancelFunc
	connectErr   string
	settingsErr  string
	hostingBusy  bool
	pairBusy     bool
	toPairing    bool
	toHosts      bool
	// overlayShown is whether the overlay draws over the stream: the
	// button, its tab, the menu, the metrics window or the toast. onOverlay
	// hears it change.
	overlayShown bool
	onOverlay    func(shown bool)
	// micErr is why the last unmute failed.
	micErr string
}

// NewApp returns the UI driving backend, and subscribes to its changes.
func NewApp(backend Backend) *App {
	a := &App{backend: backend, theme: theme.Planck(), goos: runtime.GOOS}
	a.machineName, _ = os.Hostname()
	backend.Subscribe(a.Notify)
	return a
}

// Content is the window content to give mygo.WindowOptions.Content.
func (a *App) Content() *mui.Content { return mui.View(a.View) }

// SetStreamView sets what draws the video under the overlay, such as an
// element a GPU surface presents into. Without one the stream view shows
// the letterbox well. With one, a live stream paints no background at
// all, so that a surface beneath the window's UI layer shows through.
func (a *App) SetStreamView(build func(c *mui.Context)) {
	a.streamView = build
	t := *a.theme
	t.Background = mui.Color{}
	a.videoTheme = &t
}

// Notify asks for a redraw. Safe from any goroutine.
func (a *App) Notify() {
	a.mu.Lock()
	s, ok := a.services, a.haveServices
	a.mu.Unlock()
	if ok {
		s.Invalidate()
	}
}

// SetWindow gives the stream overlay the window, for its Fullscreen
// switch. Call it on the main thread.
func (a *App) SetWindow(w WindowHook) { a.window = w }

// OnOverlayChange registers fn to hear each time the overlay starts or
// stops drawing over the stream, so that a video path the window's UI
// cannot draw over makes way for it. fn runs on the main thread inside a
// frame and must not block.
func (a *App) OnOverlayChange(fn func(shown bool)) {
	a.mu.Lock()
	a.onOverlay = fn
	a.mu.Unlock()
}

// openHostSettings shows Settings with a host's own stream format.
func (a *App) openHostSettings(h HostInfo) {
	a.ovScope = overrideScope{hostID: h.ID, name: h.Name}
	a.drafts.err = ""
	a.screen = ScreenSettings
}

// View builds one frame of the client.
func (a *App) View(c *mui.Context) {
	c.SetTheme(a.theme)
	a.mu.Lock()
	if !a.haveServices {
		a.services, a.haveServices = c.Services(), true
	}
	if a.toPairing {
		a.toPairing = false
		a.screen = ScreenPairing
	}
	toHosts := a.toHosts
	a.toHosts = false
	connecting := a.connecting
	a.mu.Unlock()
	a.frameNow = c.Now()

	tel := a.backend.Telemetry()
	// A stream, even one started from Pairing, ends on Hosts.
	if a.streaming && !tel.Connected {
		toHosts = true
		a.noteOverlay(false)
	}
	if !a.streaming && tel.Connected {
		w, h := c.Size()
		a.ovl.begin(a.backend.OverlayLayout(), c.Now(), w, h)
	}
	a.streaming = tel.Connected
	if toHosts {
		a.screen, a.pin = ScreenHosts, ""
	}
	if tel.Connected && a.streamView != nil {
		c.SetTheme(a.videoTheme)
	}
	if tel.Connected || connecting != "" {
		a.streamScreen(c, tel, connecting)
		return
	}
	mui.Row(c).Fill().Background(theme.RoleGround).Children(func() {
		a.rail(c)
		mui.Scroll(c).Grow(1).FillHeight().Children(func() {
			top, bottom := theme.Space7, theme.Space6
			if a.screen == ScreenHosts {
				top, bottom = theme.Space5, theme.Space5
			}
			mui.Column(c).FillWidth().Padding(top, theme.Space7, bottom).Gap(theme.Space6).Children(func() {
				switch a.screen {
				case ScreenHosts:
					a.hostsScreen(c)
				case ScreenPairing:
					a.pairingScreen(c)
				case ScreenSettings:
					a.settingsScreen(c)
				}
			})
		})
	})
}

// connect starts Backend.Connect off the main thread.
func (a *App) connect(target, name string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.connecting != "" {
		a.mu.Unlock()
		cancel()
		return
	}
	a.connecting, a.cancel, a.connectErr = name, cancel, ""
	a.mu.Unlock()
	go func() {
		err := a.backend.Connect(ctx, target)
		a.mu.Lock()
		a.connecting, a.cancel = "", nil
		switch {
		case errors.Is(err, ErrPairingRequired):
			a.toPairing = true
		case err != nil && !errors.Is(err, context.Canceled):
			a.connectErr = err.Error()
		}
		a.mu.Unlock()
		cancel()
		a.Notify()
	}()
}

func (a *App) cancelConnect() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) disconnect() {
	a.ovl.menuOpen = false
	a.backend.Disconnect()
}

func (a *App) setHosting(on bool) {
	a.mu.Lock()
	if a.hostingBusy {
		a.mu.Unlock()
		return
	}
	a.hostingBusy = true
	a.mu.Unlock()
	go func() {
		// The backend reports failures through Hosting().Err.
		_ = a.backend.SetHosting(context.Background(), on)
		a.mu.Lock()
		a.hostingBusy = false
		a.mu.Unlock()
		a.Notify()
	}()
}

// pairing runs a pairing step off the main thread; the backend reports
// its outcome through Pairing().
func (a *App) pairing(step func(ctx context.Context) error) {
	a.mu.Lock()
	if a.pairBusy {
		a.mu.Unlock()
		return
	}
	a.pairBusy = true
	a.mu.Unlock()
	go func() {
		err := step(context.Background())
		paired := a.backend.Pairing().Status == PairPaired
		a.mu.Lock()
		a.pairBusy = false
		if err != nil && paired {
			// Paired, so the host's PIN has changed: the error belongs on
			// Hosts, where connecting again needs none.
			a.toHosts, a.connectErr = true, err.Error()
		}
		a.mu.Unlock()
		a.Notify()
	}()
}

// ToggleMic mutes or unmutes the microphone of a live stream, for the
// overlay's Microphone switch and its hotkey. Safe from any goroutine.
func (a *App) ToggleMic() {
	t := a.backend.Telemetry()
	if !t.Connected {
		return
	}
	msg := ""
	if err := a.backend.SetMicMuted(!t.MicMuted); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	a.micErr = msg
	a.mu.Unlock()
	a.Notify()
}

// saveSettings stores s if the controls changed it, keeping the error to
// show at the top of Settings.
func (a *App) saveSettings(before, s Settings) {
	if s == before {
		return
	}
	msg := ""
	if err := a.backend.SaveSettings(s); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	a.settingsErr = msg
	a.mu.Unlock()
}
