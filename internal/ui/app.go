// Package ui is Planck's desktop client interface, drawn with MyGo: the
// icon rail, the Hosts, Pairing and Settings screens, and the stream view
// with its telemetry HUD. It reads and drives a Backend and holds no
// networking of its own.
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

// HUDMode is how much of the HUD shows over the stream. The pill shows
// for a while on connect, then while the pointer is at the top edge or
// over it, unless pinned; the drawer keeps it showing; hidden turns the
// HUD off.
type HUDMode int

const (
	HUDPill HUDMode = iota
	HUDDrawer
	HUDHidden
)

const (
	// hudReveal is how long the pill shows on connect and after the HUD
	// hotkey brings it back.
	hudReveal = 2500 * time.Millisecond
	// hudLinger is how long it stays after the pointer leaves it.
	hudLinger = 1500 * time.Millisecond
	// hudEdge is the band along the top of the stream that reveals it.
	hudEdge = theme.Space5
)

// App is the client UI. Build it with NewApp and mount Content in a
// window. Its view runs on the main thread; Notify, CycleHUD and the
// backend's change callback may run on any goroutine.
type App struct {
	backend    Backend
	theme      *mui.Theme
	goos       string
	streamView func(c *mui.Context)
	// videoTheme is theme with a clear window background, for a stream
	// view whose video shows under the UI's layer.
	videoTheme *mui.Theme

	// Main-thread state, read and written only by the view.
	screen       Screen
	quickConnect string
	pin          string
	streaming    bool
	hudPinned    bool
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
	// hudUntil is when the unpinned pill hides, once nothing holds it;
	// hudHeld is whether the drawer, the pin or the pointer held it last
	// frame; hudRects are where the pill and drawer were drawn.
	hudUntil time.Time
	hudHeld  bool
	hudRects []mui.Rect

	mu           sync.Mutex
	services     mui.Services
	haveServices bool
	hud          HUDMode
	hudRevealed  bool
	connecting   string
	cancel       context.CancelFunc
	connectErr   string
	settingsErr  string
	hostingBusy  bool
	pairBusy     bool
	toPairing    bool
	toHosts      bool
	// hudVisible is whether the HUD is on screen, as last drawn or as the
	// hotkey just made it.
	hudVisible bool
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

// SetStreamView sets what draws the video under the HUD, such as an
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

// CycleHUD steps the HUD from pill to drawer to hidden and back, for the
// HUD hotkey and for input layers that capture keys before the window.
// Each step shows the pill for a while, as connecting does; a pill that
// hid itself first just shows again.
func (a *App) CycleHUD() {
	a.mu.Lock()
	if a.hud != HUDPill || a.hudVisible {
		a.hud = (a.hud + 1) % 3
	}
	a.hudRevealed = true
	a.hudVisible = a.hud != HUDHidden
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
	a.hudRevealed = true
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
	toHosts := a.toHosts
	a.toHosts = false
	connecting := a.connecting
	revealed := a.hudRevealed
	a.hudRevealed = false
	a.mu.Unlock()

	c.OnWindowShortcut(mui.Ctrl|mui.Alt, mui.KeyH, a.CycleHUD)

	tel := a.backend.Telemetry()
	// A stream, even one started from Pairing, ends on Hosts.
	if a.streaming && !tel.Connected {
		toHosts = true
	}
	if revealed || (!a.streaming && tel.Connected) {
		a.hudUntil = c.Now().Add(hudReveal)
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
