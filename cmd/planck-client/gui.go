//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/assets"
	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/tailscale"
)

func runGUI(o options) error {
	if err := theme.RegisterFonts(); err != nil {
		return err
	}
	logf := log.Printf
	ts := tailscale.New()
	f := newFinder(fmt.Sprintf(":%d", o.discoveryPort), ts, nil, logf)
	streams := &streamWindows{later: onMainAfter}
	video := &videoView{logf: logf, streams: streams}
	ptr := newPointerLock(nil, nil)
	ptr.sys = newSystemPointer(video.win.Load, ptr.motion)
	ptr.cursor = newHostCursor(video.win.Load)
	defer ptr.close()
	sys := newSystemKeys(newKeyTap(video.win.Load), nil, logf)
	defer sys.streamEnded()
	// The backend's onHostDisplays reads both once a stream runs.
	var b *backend
	var app *ui.App
	b, err := newBackend(backendConfig{
		dir: o.configDir, name: o.name, finder: f, tailscale: ts, openSink: video.open,
		hostConfig: appHostConfig(o.configDir, logf),
		encoders:   localEncoders(), logf: logf, overrides: o.overrides, viewSize: video.liveSize, display: video.display, onCursor: ptr.setHost, onCursorShape: ptr.setShape,
		onSystemKeys: sys.setEnabled, globals: desktopShortcuts{}, openGamepads: session.OpenGamepads,
		onHostDisplays: func(host string, ds []defaults.HostDisplay, streamed string) {
			s := b.Settings()
			p := planDisplayWindows(ds, streamed, s.OpenEveryHostDisplay, s.MaxStreamWindows, hostStreamsAtOnce)
			if title := displayNotice(host, s.OpenEveryHostDisplay, p); title != "" {
				mygo.RunOnMain(func() { app.ShowDisplayNotice(title) })
			}
		},
	})
	if err != nil {
		return err
	}
	app = ui.NewApp(b)
	video.keyframe = b.RequestKeyframe
	app.OnOverlayChange(video.overlayChanged)
	ptr.send, ptr.changed = b.SendInput, app.Notify
	sys.send, sys.changed = b.SendInput, app.Notify
	sys.setEnabled(b.Settings().SystemKeysCapture)
	app.SetSystemKeys(sys)
	local := &localKeys{}
	local.update(b.Hotkeys(), b.NeverSend())
	sys.local, b.onKeys = local, local.update
	fwd := &inputForwarder{send: b.SendInput, video: video.videoSize, lock: ptr, sys: sys, local: local}
	// The UI asks for Ctrl+Alt+Del on the main thread, which fwd runs on.
	b.heldMods = func() uint8 { return fwd.mods | sys.held.Modifiers() }
	app.SetStreamView(fwd.view)
	resizer := &viewResizer{delay: resizeSettle, send: b.resizeStream}

	streams.fullscreen = func() bool { return b.Settings().OpenFullscreen }
	streams.videoLive = video.surfaceLive
	streams.userClosed = app.StreamWindowClosed
	streams.gone = func() {
		sys.streamEnded()
		video.detach()
		ptr.reset()
	}
	video.closed = streams.videoClosed
	streams.create = func(title string) nativeStreamWin {
		w := newStreamWindow(title, video.main.Load(), app.StreamContent())
		video.attach(w)
		// settled is whether the window was in full screen when its last
		// transition ended. The style mask, IsFullScreen, flips as one
		// starts; the sizes macOS reports until it ends are not final, and
		// its end sends the final one. It opens windowed. Main thread only.
		settled := false
		w.OnResize(func() {
			video.resized()
			if w.IsFullScreen() != settled {
				return
			}
			resizer.changed(video.pixelSize())
		})
		// A window moved onto another display may keep its size but not
		// the display's refresh or scale, nor its own size in pixels; the
		// resize it sends once settled carries the display, and one on
		// the same display sends nothing.
		w.OnMove(func() {
			video.storeSize(w)
			if w.IsFullScreen() == settled {
				resizer.changed(video.pixelSize())
			}
		})
		fullScreen := func() {
			settled = w.IsFullScreen()
			video.resized()
			video.applyDirect()
			resizer.now(video.pixelSize())
			streams.fullScreenChanged(settled)
		}
		w.OnEnterFullScreen(fullScreen)
		w.OnLeaveFullScreen(fullScreen)
		w.OnClose(func(e *mygo.CloseEvent) {
			if !streams.closeRequested() {
				e.PreventDefault()
			}
		})
		return w
	}
	app.SetStreamWindow(streamWindowHook{streams})
	app.SetWindow(fullScreenHook{video.win.Load})
	app.SetCursor(cursorHook{ptr, sys})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.run(ctx)
	mygo.App.SetName("Planck")
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { streams.quitting = true })
	mygo.App.OnQuitCanceled(func() { streams.quitting = false })
	mygo.App.WhenReady(func() {
		if exe, _ := os.Executable(); runtime.GOOS != "darwin" || !inAppBundle(exe) {
			if err := mygo.App.Dock.SetIcon(assets.AppIcon(runtime.GOOS)); err != nil {
				logf("app icon: %v", err)
			}
		}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Planck", Width: 1280, Height: 800, MinWidth: 960, MinHeight: 600,
			BackgroundColor: "#0E0F12", Content: app.Content(),
		})
		video.main.Store(win)
		// The launcher is the app: closing it ends any stream and quits.
		win.OnClosed(func() {
			b.Disconnect()
			mygo.App.Quit()
		})
	})
	err = mygo.App.Run()
	b.close()
	return err
}

// Stream windows open at the launcher's default size, over the launcher,
// so that they open on its display.
const streamWidth, streamHeight = 1280, 800

// newStreamWindow makes the window a stream shows in, over main. Main
// thread.
func newStreamWindow(title string, main *mygo.Window, content mygo.Content) *mygo.Window {
	opts := mygo.WindowOptions{
		Title: title, Width: streamWidth, Height: streamHeight, MinWidth: 640, MinHeight: 360,
		BackgroundColor: "#0E0F12", Transparent: composeUnderUI, Content: content,
	}
	if main != nil && !main.IsDestroyed() {
		opts.X, opts.Y = overWindow(main.Bounds(), streamWidth, streamHeight)
	}
	return mygo.NewWindow(opts)
}

// overWindow places a w x h window centred over r, its top-left corner
// kept on r so that it opens on r's display.
func overWindow(r mygo.Rectangle, w, h int) (x, y int) {
	return r.X + max((r.Width-w)/2, 0), r.Y + max((r.Height-h)/2, 0)
}

// onMainAfter runs fn on the main thread after d, or on its next turn.
func onMainAfter(d time.Duration, fn func()) {
	if d <= 0 {
		go mygo.RunOnMain(fn)
		return
	}
	time.AfterFunc(d, func() { mygo.RunOnMain(fn) })
}

// videoView puts each stream's GPU surface under the window's UI and
// tracks the window's size for it.
type videoView struct {
	logf func(string, ...any)
	// win is the stream's window, and main the launcher's, whose display
	// and size a stream starting before its window has stand in.
	win, main atomic.Pointer[mygo.Window]
	cur       atomic.Pointer[surfaceSink]
	streams   *streamWindows
	// surfaces counts the surfaces on win, until each has closed; closed
	// hears each close, on the main thread.
	surfaces atomic.Int32
	closed   func()
	// size is the window's content size in DIPs and pixels in pixels,
	// width in the high half; resize says the surface has yet to follow
	// a change of it.
	size   atomic.Uint64
	pixels atomic.Uint64
	resize atomic.Bool
	// frame is the size of the pictures decoded last and drawable the
	// surface's, in pixels, width in the high half, for the pointer.
	frame, drawable atomic.Uint64
	mode            directMode
	// keyframe asks the host for one, as the present path changes.
	keyframe func()
}

// cursorHook is the mouse lock as the overlay drives it, and immersive
// mode's keyboard capture, which the menu and the stream's end stop too.
type cursorHook struct {
	l   *pointerLock
	sys *systemKeys
}

func (h cursorHook) CursorLocked() bool      { return h.l.locked() }
func (h cursorHook) SetCursorLocked(on bool) { h.l.setLocked(on) }

func (h cursorHook) SetMenuOpen(open bool) {
	h.sys.setMenuOpen(open)
	h.l.setMenuOpen(open)
}

func (h cursorHook) StreamEnded() {
	h.sys.streamEnded()
	h.l.reset()
}

// fullScreenHook is the stream's window for the overlay's Fullscreen
// switch.
type fullScreenHook struct{ win func() *mygo.Window }

func (h fullScreenHook) Fullscreen() bool {
	w := h.win()
	return w != nil && w.IsFullScreen()
}

func (h fullScreenHook) SetFullscreen(on bool) {
	if w := h.win(); w != nil {
		w.SetFullScreen(on)
	}
}

// attach takes the stream's window. Main thread.
func (v *videoView) attach(w *mygo.Window) {
	v.win.Store(w)
	v.storeSize(w)
}

// detach lets the stream's window go as it closes. Main thread.
func (v *videoView) detach() { v.win.Store(nil) }

// surfaceLive reports whether a surface is still on the stream's window.
func (v *videoView) surfaceLive() bool { return v.surfaces.Load() > 0 }

// window is the stream's window, or the launcher's before it has one.
func (v *videoView) window() *mygo.Window {
	if w := v.win.Load(); w != nil {
		return w
	}
	return v.main.Load()
}

func (v *videoView) storeSize(w *mygo.Window) {
	cw, ch := w.ContentSize()
	v.size.Store(packSize(cw, ch))
	v.pixels.Store(packSize(windowPixels(w)))
}

// pixelSize is the window's content size in pixels, the size a stream on a
// virtual display takes.
func (v *videoView) pixelSize() (int, int) { return unpackSize(v.pixels.Load()) }

func (v *videoView) contentSize() (int, int) { return unpackSize(v.size.Load()) }

// liveSize reads the stream window's size in pixels now, for a stream
// that starts before a resize or a full-screen transition was heard of. A
// window on its way to full screen is waited for, so that the host makes
// its display the final size once. Not on the main thread.
func (v *videoView) liveSize() (int, int) {
	if v.streams != nil {
		v.streams.waitReady(streamReadyWait)
	}
	if w := v.window(); w != nil {
		mygo.RunOnMain(func() { v.storeSize(w) })
	}
	return v.pixelSize()
}

// display is the display the stream's window is on, or the launcher's.
func (v *videoView) display() defaults.ClientDisplay {
	var d defaults.ClientDisplay
	if w := v.window(); w != nil {
		mygo.RunOnMain(func() { d = windowDisplay(w) })
	}
	return d
}

func packSize(w, h int) uint64       { return uint64(uint32(w))<<32 | uint64(uint32(h)) }
func unpackSize(s uint64) (int, int) { return int(s >> 32), int(uint32(s)) }

func (v *videoView) videoSize() (fw, fh, dw, dh int) {
	fw, fh = unpackSize(v.frame.Load())
	dw, dh = unpackSize(v.drawable.Load())
	return fw, fh, dw, dh
}

// resized runs on the main thread after the window's size changed.
func (v *videoView) resized() {
	w := v.win.Load()
	if w == nil {
		return
	}
	v.storeSize(w)
	if s := v.cur.Load(); s != nil {
		s.host.fit()
	}
	v.refit()
}

// refit has the surface take the window's size and show its picture
// again, fitted anew, at once rather than with the next picture: a host
// whose screen is still sends none for seconds. It does not wait.
func (v *videoView) refit() {
	v.resize.Store(true)
	if s := v.cur.Load(); s != nil {
		go s.refit(true)
	}
}

var errNoWindow = errors.New("no window to show the stream in")

// open is the session's SinkOpener: a surface on the window, made on the
// main thread, and a hardware decoder on its device.
func (v *videoView) open(cfg decode.Config) (session.Sink, error) {
	var h *surfaceHost
	var err error
	mygo.RunOnMain(func() {
		if v.streams != nil {
			v.streams.ensure()
		}
		w := v.win.Load()
		if w == nil {
			err = errNoWindow
			return
		}
		if h, err = newSurfaceHost(w, cfg.HDR10); err == nil && h != nil {
			v.surfaces.Add(1)
		}
	})
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, errNoWindow
	}
	p, err := player.Open(h.surf, cfg, 0)
	if err != nil {
		mygo.RunOnMain(func() {
			h.close()
			v.surfaceClosed()
		})
		return nil, err
	}
	s := &surfaceSink{v: v, host: h, p: p}
	s.resize = func() error {
		err := h.resizeSurface(p, v)
		v.drawable.Store(packSize(h.surf.Size()))
		return err
	}
	v.cur.Store(s)
	v.resize.Store(true)
	return s, nil
}

// surfaceSink decodes on the session's decode goroutine; the player
// presents on its own thread.
type surfaceSink struct {
	v    *videoView
	host *surfaceHost
	p    *player.Player
	// resize resizes the surface to the window and shows the picture on
	// it again; resizing keeps one at a time.
	resize   func() error
	resizing sync.Mutex
}

// refit resizes the surface if the window changed since it last did.
// With wait false, as on the decode goroutine, it leaves a resize under
// way to finish on its own.
func (s *surfaceSink) refit(wait bool) {
	if !s.v.resize.Load() {
		return
	}
	if wait {
		s.resizing.Lock()
	} else if !s.resizing.TryLock() {
		return
	}
	defer s.resizing.Unlock()
	if s.v.cur.Load() != s || !s.v.resize.Swap(false) {
		return
	}
	if err := s.resize(); err != nil {
		s.v.logf("planck: resize the video surface: %v", err)
	}
}

func (s *surfaceSink) SetObserver(o player.Observer) { s.p.SetObserver(o) }

func (s *surfaceSink) Submit(au []byte, pts int64) (player.Timing, error) {
	s.refit(false)
	t, err := s.p.Submit(au, pts)
	if err == nil {
		_, w, h := s.p.Format()
		s.v.frame.Store(packSize(w, h))
	}
	return t, err
}

// Close leaves direct mode before the surface goes, so the window's own
// layers composite again.
func (s *surfaceSink) Close() error {
	s.resizing.Lock()
	err := s.p.Close()
	s.v.cur.CompareAndSwap(s, nil)
	s.resizing.Unlock()
	mygo.RunOnMain(func() {
		if s.host.surf.Direct() {
			_ = s.host.surf.SetDirect(false)
		}
		s.host.close()
		s.v.surfaceClosed()
	})
	return err
}

// surfaceClosed counts a surface off the window. Main thread.
func (v *videoView) surfaceClosed() {
	v.surfaces.Add(-1)
	if v.closed != nil {
		v.closed()
	}
}

// desktopShortcuts are MyGo's global shortcuts, which fire while Planck
// is not in front.
type desktopShortcuts struct{}

func (desktopShortcuts) Register(acc string, fn func()) error {
	return mygo.GlobalShortcut.Register(acc, fn)
}
func (desktopShortcuts) Unregister(acc string) { mygo.GlobalShortcut.Unregister(acc) }
