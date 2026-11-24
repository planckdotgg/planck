//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/egoist/mygo"

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
	video := &videoView{logf: logf}
	b, err := newBackend(backendConfig{
		dir: o.configDir, name: o.name, finder: f, tailscale: ts, openSink: video.open,
		hostConfig: func(s ui.Settings) session.HostConfig {
			c := session.DefaultHostConfig()
			c.ConfigDir, c.Adapter, c.InjectInput, c.Logf = o.configDir, s.Encoder, true, logf
			c.DiscoveryAddr = fmt.Sprintf(":%d", o.discoveryPort)
			return c
		},
		encoders: localEncoders(), logf: logf, overrides: o.overrides,
	})
	if err != nil {
		return err
	}
	app := ui.NewApp(b)
	video.stats = func() (session.ClientStats, bool) {
		c := b.current()
		if c == nil {
			return session.ClientStats{}, false
		}
		s := c.Stats()
		return s, s.Connected
	}
	video.hudHidden = func() bool { return app.HUD() == ui.HUDHidden }
	app.OnDrawerChange(video.drawerChanged)
	fwd := &inputForwarder{send: b.SendInput, video: video.videoSize}
	app.SetStreamView(fwd.view)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.run(ctx)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title: "Planck", Width: 1280, Height: 800, MinWidth: 960, MinHeight: 600,
			BackgroundColor: "#0E0F12", Transparent: composeUnderUI, Content: app.Content(),
		})
		video.attach(win)
		win.OnResize(video.resized)
		win.OnEnterFullScreen(video.applyDirect)
		win.OnLeaveFullScreen(video.applyDirect)
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
	cur  atomic.Pointer[surfaceHost]
	// size is the window's content size in DIPs, width in the high half;
	// resize tells the presenting thread it changed.
	size   atomic.Uint64
	resize atomic.Bool
	// frame is the size of the pictures decoded last and drawable the
	// surface's, in pixels, width in the high half, for the pointer.
	frame, drawable atomic.Uint64
	mode            directMode
	// stats reads the live session for the pill drawn into the video, and
	// hudHidden whether the user hid the HUD.
	stats     func() (session.ClientStats, bool)
	hudHidden func() bool
}

// attach runs on the main thread.
func (v *videoView) attach(w *mygo.Window) {
	v.win.Store(w)
	v.storeSize(w)
}

func (v *videoView) storeSize(w *mygo.Window) {
	cw, ch := w.ContentSize()
	v.size.Store(packSize(cw, ch))
}

func (v *videoView) contentSize() (int, int) { return unpackSize(v.size.Load()) }

func packSize(w, h int) uint64       { return uint64(uint32(w))<<32 | uint64(uint32(h)) }
func unpackSize(s uint64) (int, int) { return int(s >> 32), int(uint32(s)) }

func (v *videoView) videoSize() (fw, fh, dw, dh int) {
	fw, fh = unpackSize(v.frame.Load())
	dw, dh = unpackSize(v.drawable.Load())
	return fw, fh, dw, dh
}

// resized runs on the main thread after the window's size changed; the
// presenting thread resizes the surface before its next picture.
func (v *videoView) resized() {
	w := v.win.Load()
	if w == nil {
		return
	}
	v.storeSize(w)
	if h := v.cur.Load(); h != nil {
		h.fit()
	}
	v.resize.Store(true)
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
	v.cur.Store(h)
	v.resize.Store(true)
	s := &surfaceSink{v: v, host: h, p: p, stop: make(chan struct{})}
	go v.hudLoop(s, s.stop)
	return s, nil
}

// surfaceSink decodes on the session's decode goroutine; the player
// presents on its own thread.
type surfaceSink struct {
	v       *videoView
	host    *surfaceHost
	p       *player.Player
	profile atomic.Pointer[defaults.ResolvedProfile]
	stop    chan struct{}
}

func (s *surfaceSink) SetObserver(o player.Observer) { s.p.SetObserver(o) }

func (s *surfaceSink) Submit(au []byte, pts int64) (player.Timing, error) {
	if s.v.resize.Swap(false) {
		if err := s.host.resizeSurface(s.p, s.v); err != nil {
			s.v.logf("planck: resize the video surface: %v", err)
		}
		s.v.drawable.Store(packSize(s.host.surf.Size()))
	}
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
	close(s.stop)
	err := s.p.Close()
	s.v.cur.CompareAndSwap(s.host, nil)
	mygo.RunOnMain(func() {
		if s.host.surf.Direct() {
			_ = s.host.surf.SetDirect(false)
		}
		s.host.close()
	})
	return err
}
