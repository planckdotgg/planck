// Package ui is Planck's desktop client interface, drawn with MyGo: the
// icon rail, the Hosts, Pairing and Settings screens, and the stream view
// with its telemetry HUD. It reads and drives a Backend and holds no
// networking of its own.
package ui

import (
	"context"
	"errors"
	"runtime"
	"sync"

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

// HUDMode is how much of the HUD shows over the stream.
type HUDMode int

const (
	HUDPill HUDMode = iota
	HUDDrawer
	HUDHidden
)

// App is the client UI. Build it with NewApp and mount Content in a
// window. Its view runs on the main thread; Notify, CycleHUD and the
// backend's change callback may run on any goroutine.
type App struct {
	backend    Backend
	theme      *mui.Theme
	goos       string
	streamView func(c *mui.Context)

	// Main-thread state, read and written only by the view.
	screen       Screen
	quickConnect string
	pin          string

	mu           sync.Mutex
	services     mui.Services
	haveServices bool
	hud          HUDMode
	connecting   string
	cancel       context.CancelFunc
	connectErr   string
	settingsErr  string
	hostingBusy  bool
	pairBusy     bool
	toPairing    bool
}

// NewApp returns the UI driving backend, and subscribes to its changes.
func NewApp(backend Backend) *App {
	a := &App{backend: backend, theme: theme.Planck(), goos: runtime.GOOS}
	backend.Subscribe(a.Notify)
	return a
}

// Content is the window content to give mygo.WindowOptions.Content.
func (a *App) Content() *mui.Content { return mui.View(a.View) }

// SetStreamView sets what draws the video under the HUD, such as an
// element a GPU surface presents into. Without one the stream view shows
// the letterbox well.
func (a *App) SetStreamView(build func(c *mui.Context)) { a.streamView = build }

// Notify asks for a redraw. Safe from any goroutine.
func (a *App) Notify() {
	a.mu.Lock()
	s, ok := a.services, a.haveServices
	a.mu.Unlock()
	if ok {
		s.Invalidate()
	}
}

// CycleHUD steps the HUD from pill to drawer to hidden and back, for the
// HUD hotkey and for input layers that capture keys before the window.
func (a *App) CycleHUD() {
	a.mu.Lock()
	a.hud = (a.hud + 1) % 3
	a.mu.Unlock()
	a.Notify()
}

func (a *App) hudMode() HUDMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hud
}

func (a *App) setHUD(m HUDMode) {
	a.mu.Lock()
	a.hud = m
	a.mu.Unlock()
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
	connecting := a.connecting
	a.mu.Unlock()

	c.OnWindowShortcut(mui.Ctrl|mui.Alt, mui.KeyH, a.CycleHUD)

	tel := a.backend.Telemetry()
	if tel.Connected || connecting != "" {
		a.streamScreen(c, tel, connecting)
		return
	}
	mui.Row(c).Fill().Background(theme.RoleGround).Children(func() {
		a.rail(c)
		mui.Scroll(c).Grow(1).FillHeight().Children(func() {
			mui.Column(c).FillWidth().Padding(theme.Space7, theme.Space7, theme.Space6).Gap(theme.Space6).Children(func() {
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
	a.backend.Disconnect()
	a.setHUD(HUDPill)
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
		_ = step(context.Background())
		a.mu.Lock()
		a.pairBusy = false
		a.mu.Unlock()
		a.Notify()
	}()
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
