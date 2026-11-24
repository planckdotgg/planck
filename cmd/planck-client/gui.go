//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/decode"
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
	video := &videoView{logf: logf}
	ptr := newPointerLock(nil, nil)
	ptr.sys = newSystemPointer(video.win.Load, ptr.motion)
	defer ptr.close()
	b, err := newBackend(backendConfig{
		dir: o.configDir, name: o.name, finder: f, tailscale: ts, openSink: video.open,
		hostConfig: func(s ui.Settings) session.HostConfig {
			c := session.DefaultHostConfig()
			c.ConfigDir, c.Adapter, c.InjectInput, c.Logf = o.configDir, s.Encoder, true, logf
			c.DiscoveryAddr = fmt.Sprintf(":%d", o.discoveryPort)
			return c
		},
		encoders: localEncoders(), logf: logf, overrides: o.overrides, viewSize: video.pixelSize, onCursor: ptr.setHost,
	})
	if err != nil {
		return err
	}
	app := ui.NewApp(b)
	video.keyframe = b.RequestKeyframe
	app.OnOverlayChange(video.overlayChanged)
	ptr.send, ptr.changed = b.SendInput, app.Notify
	fwd := &inputForwarder{send: b.SendInput, video: video.videoSize, lock: ptr}
	app.SetStreamView(fwd.view)
	resizer := &viewResizer{delay: resizeSettle, send: b.resizeStream}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.run(ctx)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Planck", Width: 1280, Height: 800, MinWidth: 960, MinHeight: 600,
			BackgroundColor: "#0E0F12", Transparent: composeUnderUI, Content: app.Content(),
		})
		video.attach(win)
		app.SetWindow(fullScreenHook{win})
		app.SetCursor(cursorHook{ptr})
		win.OnResize(func() {
			video.resized()
			resizer.changed(video.pixelSize())
		})
		fullScreen := func() {
			video.resized()
			video.applyDirect()
			resizer.now(video.pixelSize())
		}
		win.OnEnterFullScreen(fullScreen)
		win.OnLeaveFullScreen(fullScreen)
	})
	err = mygo.App.Run()
	b.close()
	return err
}

// videoView puts each stream's GPU surface under the window's UI and
// tracks the window's size for it.
type videoView struct {
	logf func(string, ...any)
	win  atomic.Pointer[mygo.Window]
	cur  atomic.Pointer[surfaceSink]
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

// cursorHook is the mouse lock as the overlay drives it.
type cursorHook struct{ l *pointerLock }

func (h cursorHook) CursorLocked() bool      { return h.l.locked() }
func (h cursorHook) SetCursorLocked(on bool) { h.l.setLocked(on) }
func (h cursorHook) SetMenuOpen(open bool)   { h.l.setMenuOpen(open) }
func (h cursorHook) StreamEnded()            { h.l.reset() }

// fullScreenHook is the window for the overlay's Fullscreen switch.
type fullScreenHook struct{ w *mygo.Window }

func (h fullScreenHook) Fullscreen() bool      { return h.w.IsFullScreen() }
func (h fullScreenHook) SetFullscreen(on bool) { h.w.SetFullScreen(on) }

// attach runs on the main thread.
func (v *videoView) attach(w *mygo.Window) {
	v.win.Store(w)
	v.storeSize(w)
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
	w := v.win.Load()
	if w == nil {
		return nil, errNoWindow
	}
	var h *surfaceHost
	var err error
	mygo.RunOnMain(func() { h, err = newSurfaceHost(w) })
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, errNoWindow
	}
	p, err := player.Open(h.surf, cfg, 0)
	if err != nil {
		mygo.RunOnMain(h.close)
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
	})
	return err
}
