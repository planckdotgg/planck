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
	cursor     CursorHook
	sysKeys    SystemKeysHook
	// streamWin, when set, shows streams in a window of their own
	// (StreamContent), and the window Content is in keeps the launcher.
	streamWin StreamWindowHook
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
	// ovlLive is set once the overlay began for the live stream, in the
	// view that draws it.
	ovlLive bool
	// streamFull is set while the window is full screen because the
	// stream's controls made it so, which the stream's end undoes;
	// streamFullSeen once the window was seen full screen since.
	streamFull, streamFullSeen bool
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
	// streamServices are the stream window's, while it is open.
	streamServices     mui.Services
	haveStreamServices bool
	connecting         string
	// connectTarget is what the connect under way dials, and connectGen
	// counts connects, so that one replaced by another drops its result.
	connectTarget string
	connectGen    int
	// abandoned is set when the user closed the stream's window while it
	// was connecting: a connect that succeeds anyway is dropped.
	abandoned   bool
	cancel      context.CancelFunc
	connectErr  string
	settingsErr string
	hostingBusy bool
	pairBusy    bool
	toPairing   bool
	toHosts     bool
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

// StreamContent is the content of the stream's own window, for an app
// that set SetStreamWindow.
func (a *App) StreamContent() *mui.Content { return mui.View(a.StreamView) }

// SetStreamWindow has streams show in a window of their own, which h opens
// and closes, while Content keeps Hosts, Pairing and Settings. Call it on
// the main thread before the first frame.
func (a *App) SetStreamWindow(h StreamWindowHook) { a.streamWin = h }

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
	ss, sok := a.streamServices, a.haveStreamServices
	a.mu.Unlock()
	if ok {
		s.Invalidate()
	}
	if sok {
		ss.Invalidate()
	}
}

// StreamWindowClosed tells the app the user closed the stream's window:
// the stream, or the connect under way, ends. Call it on the main thread.
func (a *App) StreamWindowClosed() {
	a.mu.Lock()
	a.haveStreamServices = false
	if a.connecting != "" {
		a.abandoned = true
	}
	a.mu.Unlock()
	a.cancelConnect()
	if a.backend.Telemetry().Connected {
		a.disconnect()
	}
}

// SetWindow gives the stream overlay the window, for its Fullscreen
// switch. Call it on the main thread.
func (a *App) SetWindow(w WindowHook) { a.window = w }

// SetCursor gives the stream overlay the mouse lock, for its shortcut and
// its switch. Call it on the main thread.
func (a *App) SetCursor(h CursorHook) { a.cursor = h }

// SetSystemKeys has the stream overlay say when immersive mode cannot
// capture the system's keys. Call it before Run.
func (a *App) SetSystemKeys(h SystemKeysHook) { a.sysKeys = h }

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

// View builds one frame of the client. Without SetStreamWindow a stream
// takes its place; with it the stream shows in StreamView, and View keeps
// the launcher.
func (a *App) View(c *mui.Context) {
	tel, connecting := a.frame(c, false)
	if a.streamWin == nil && (tel.Connected || connecting != "") {
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

// StreamView builds one frame of the stream's own window (SetStreamWindow):
// the connect under way, or the stream and its overlay. Switching hosts
// shows the connect, and a window closing after its stream shows the
// empty well.
func (a *App) StreamView(c *mui.Context) {
	tel, connecting := a.frame(c, true)
	if connecting != "" && tel.Connected {
		tel = Telemetry{}
		a.noteOverlay(true)
		c.SetTheme(a.theme)
	}
	a.streamScreen(c, tel, connecting)
}

// frame takes the backend's state for a frame of either window, and moves
// the app along the stream's start and end. stream says the frame is the
// stream window's.
func (a *App) frame(c *mui.Context, stream bool) (Telemetry, string) {
	c.SetTheme(a.theme)
	// A stream window that is closing takes no more redraws.
	live := stream && (a.streamWin == nil || a.streamWin.StreamWindowOpen())
	a.mu.Lock()
	if live {
		a.streamServices, a.haveStreamServices = c.Services(), true
	} else if !stream && !a.haveServices {
		a.services, a.haveServices = c.Services(), true
	}
	if a.toPairing {
		a.toPairing = false
		a.screen = ScreenPairing
	}
	toHosts := a.toHosts
	a.toHosts = false
	connecting, pairBusy := a.connecting, a.pairBusy
	a.mu.Unlock()
	a.frameNow = c.Now()

	tel := a.backend.Telemetry()
	split := a.streamWin != nil
	// A stream, even one started from Pairing, ends on Hosts.
	if a.streaming && !tel.Connected {
		toHosts = true
		a.noteOverlay(false)
		if split {
			// The stream's window leaves full screen as it closes.
			a.streamFull, a.streamFullSeen = false, false
		} else {
			a.leaveStreamFullscreen()
		}
		if a.cursor != nil {
			a.cursor.StreamEnded()
		}
		a.ovlLive = false
	}
	if split && !a.streaming && tel.Connected {
		// A stream from Pairing, or one the backend started, opens its
		// window too; the launcher waits on Hosts.
		toHosts = true
		if !a.streamWin.StreamWindowOpen() {
			a.streamWin.OpenStreamWindow(tel.HostName)
		}
	}
	draws := stream || !split
	if draws && tel.Connected && !a.ovlLive {
		w, h := c.Size()
		a.ovl.begin(a.backend.OverlayLayout(), c.Now(), w, h)
		a.streamFull, a.streamFullSeen = false, false
		a.ovlLive = true
	}
	if tel.Connected {
		a.watchStreamFullscreen()
	}
	a.streaming = tel.Connected
	if toHosts {
		a.screen, a.pin = ScreenHosts, ""
	}
	if split && !tel.Connected && connecting == "" && !pairBusy && a.streamWin.StreamWindowOpen() {
		a.mu.Lock()
		a.haveStreamServices = false
		a.mu.Unlock()
		a.streamWin.CloseStreamWindow()
	}
	if draws && tel.Connected && a.streamView != nil {
		c.SetTheme(a.videoTheme)
	}
	return tel, connecting
}

// connect starts Backend.Connect off the main thread, opening the
// stream's window.
func (a *App) connect(target, name string) { a.startConnect(target, name, true) }

// startConnect starts Backend.Connect; window false leaves the stream's
// window to the stream, for a host that will most likely ask to pair
// first. A connect to another target replaces one under way, which the
// launcher may not show.
func (a *App) startConnect(target, name string, window bool) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.connecting != "" {
		if a.connectTarget == target {
			a.mu.Unlock()
			cancel()
			return
		}
		a.cancel()
	}
	a.connectGen++
	gen := a.connectGen
	a.connecting, a.connectTarget, a.cancel, a.connectErr, a.abandoned = name, target, cancel, "", false
	a.mu.Unlock()
	if a.streamWin != nil && window {
		a.streamWin.OpenStreamWindow(name)
	}
	go func() {
		err := a.backend.Connect(ctx, target)
		a.mu.Lock()
		if a.connectGen != gen {
			a.mu.Unlock()
			cancel()
			return
		}
		abandoned := a.abandoned && err == nil
		a.connecting, a.connectTarget, a.cancel, a.abandoned = "", "", nil, false
		switch {
		case errors.Is(err, ErrPairingRequired):
			a.toPairing = true
		case err != nil && !errors.Is(err, context.Canceled):
			a.connectErr = err.Error()
		}
		a.mu.Unlock()
		cancel()
		if abandoned {
			a.backend.Disconnect()
		}
		a.Notify()
	}()
}

// resume brings back the stream from h: its window, or, without one, the
// stream view in place of the launcher.
func (a *App) resume(h HostInfo) {
	if a.streamWin != nil && a.streaming {
		a.streamWin.OpenStreamWindow(h.Name)
		return
	}
	a.connect(h.ID, h.Name)
}

func (a *App) cancelConnect() {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// setStreamFullscreen turns full screen on or off from the stream's
// controls.
func (a *App) setStreamFullscreen(on bool) {
	if a.window == nil {
		return
	}
	a.window.SetFullscreen(on)
	a.streamFull, a.streamFullSeen = on, false
}

// watchStreamFullscreen forgets full screen the stream turned on once the
// user left it some other way. The window may take a while to get there,
// so leaving counts only after it was seen full screen.
func (a *App) watchStreamFullscreen() {
	if !a.streamFull || a.window == nil {
		return
	}
	switch full := a.window.Fullscreen(); {
	case full:
		a.streamFullSeen = true
	case a.streamFullSeen:
		a.streamFull, a.streamFullSeen = false, false
	}
}

// leaveStreamFullscreen undoes, as the stream ends, full screen its
// controls turned on.
func (a *App) leaveStreamFullscreen() {
	on := a.streamFull
	a.streamFull, a.streamFullSeen = false, false
	if on && a.window != nil && a.window.Fullscreen() {
		a.window.SetFullscreen(false)
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
