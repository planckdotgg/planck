//go:build darwin

package player_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/player"
)

type glassByPTS struct {
	mu    sync.Mutex
	glass map[int64]float64
	// present is how long each frame's present call took, mostly the wait
	// for a drawable.
	present map[int64]float64
	errs    int
}

func (o *glassByPTS) Presented(t player.Timing, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.errs++
		return
	}
	o.present[t.PTS] = float64(t.Present) / 1e6
}

func (o *glassByPTS) Glass(pts int64, glass, _ time.Duration) {
	o.mu.Lock()
	o.glass[pts] = float64(glass) / 1e6
	o.mu.Unlock()
}

// screenWindow puts a window in native full screen on the main screen,
// the one with the menu bar, as the client is when it turns direct mode
// on: macOS sends a drawable direct to the display only from a full-screen
// window. The window holds a composite surface and, over it, a layer-backed
// view with a small opaque badge standing for the client's own UI.
func screenWindow(t *testing.T) (win, ui uintptr, w, h, hz int) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		t.Fatal(err)
	}
	var initWindow func(obj objc.ID, sel objc.SEL, r rect, style, backing uintptr, deferred bool) objc.ID
	var initView func(obj objc.ID, sel objc.SEL, r rect) objc.ID
	var frame func(obj objc.ID, sel objc.SEL) rect
	var color func(cls objc.ID, sel objc.SEL, r, g, b, a float64) objc.ID
	var dateIn func(cls objc.ID, sel objc.SEL, secs float64) objc.ID
	purego.RegisterLibFunc(&initWindow, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&initView, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&frame, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&color, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&dateIn, purego.RTLD_DEFAULT, "objc_msgSend")
	sel := objc.RegisterName
	const fullScreenMask = 1 << 14
	var app, nsWin, view objc.ID
	// pump runs AppKit's event loop, which the full-screen transition
	// needs, until done or the timeout.
	pump := func(timeout time.Duration, done func() bool) bool {
		mode := objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), "kCFRunLoopDefaultMode\x00")
		for end := time.Now().Add(timeout); time.Now().Before(end); {
			if done() {
				return true
			}
			until := dateIn(objc.ID(objc.GetClass("NSDate")), sel("dateWithTimeIntervalSinceNow:"), 0.02)
			if ev := app.Send(sel("nextEventMatchingMask:untilDate:inMode:dequeue:"), ^uint64(0), until, mode, true); ev != 0 {
				app.Send(sel("sendEvent:"), ev)
			}
		}
		return done()
	}
	full := func() bool { return objc.Send[uint](nsWin, sel("styleMask"))&fullScreenMask != 0 }
	var entered bool
	onMain(func() {
		app = objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
		app.Send(sel("setActivationPolicy:"), 0)
		app.Send(sel("finishLaunching"))
		screen := objc.ID(objc.GetClass("NSScreen")).Send(sel("screens")).Send(sel("objectAtIndex:"), 0)
		f := frame(screen, sel("frame"))
		scale := objc.Send[float64](screen, sel("backingScaleFactor"))
		if hz = objc.Send[int](screen, sel("maximumFramesPerSecond")); hz <= 0 {
			hz = 60
		}
		w, h = int(f.W*scale), int(f.H*scale)
		const titledClosableResizable, buffered, fullScreenPrimary = 1 | 2 | 8, 2, 1 << 7
		nsWin = initWindow(objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")), sel("initWithContentRect:styleMask:backing:defer:"),
			rect{X: f.X + f.W/4, Y: f.Y + f.H/4, W: f.W / 2, H: f.H / 2}, titledClosableResizable, buffered, false)
		nsWin.Send(sel("setReleasedWhenClosed:"), false)
		nsWin.Send(sel("setCollectionBehavior:"), uintptr(fullScreenPrimary))
		content := nsWin.Send(sel("contentView"))
		cb := frame(content, sel("bounds"))
		const widthHeightSizable, minYMargin = 2 | 16, 8
		view = initView(objc.ID(objc.GetClass("NSView")).Send(sel("alloc")), sel("initWithFrame:"), cb)
		view.Send(sel("setWantsLayer:"), true)
		view.Send(sel("setAutoresizingMask:"), uintptr(widthHeightSizable))
		badge := initView(objc.ID(objc.GetClass("NSView")).Send(sel("alloc")), sel("initWithFrame:"), rect{X: cb.W/2 - 150, Y: cb.H - 60, W: 300, H: 40})
		badge.Send(sel("setWantsLayer:"), true)
		badge.Send(sel("setAutoresizingMask:"), uintptr(1|4|minYMargin))
		c := color(objc.ID(objc.GetClass("NSColor")), sel("colorWithSRGBRed:green:blue:alpha:"), 0.055, 0.059, 0.071, 0.85)
		badge.Send(sel("layer")).Send(sel("setBackgroundColor:"), c.Send(sel("CGColor")))
		view.Send(sel("addSubview:"), badge)
		badge.Send(sel("release"))
		content.Send(sel("addSubview:"), view)
		nsWin.Send(sel("makeKeyAndOrderFront:"), 0)
		app.Send(sel("activateIgnoringOtherApps:"), true)
		pump(500*time.Millisecond, func() bool { return false })
		nsWin.Send(sel("toggleFullScreen:"), 0)
		// The transition's animation ends a moment after the style mask
		// changes; let it finish before measuring.
		entered = pump(5*time.Second, full) && !pump(time.Second, func() bool { return false })
	})
	t.Cleanup(func() {
		onMain(func() {
			if full() {
				nsWin.Send(sel("toggleFullScreen:"), 0)
				pump(5*time.Second, func() bool { return !full() })
			}
			nsWin.Send(sel("close"))
			view.Send(sel("release"))
			nsWin.Send(sel("release"))
			pump(200*time.Millisecond, func() bool { return false })
		})
	})
	if !entered {
		t.Fatal("the window did not enter full screen")
	}
	return uintptr(nsWin), uintptr(view), w, h, hz
}

// TestLiveDirectGlass puts a window in full screen on the main screen and
// presents an HEVC stream of the screen's size, at its refresh rate,
// through the player in three phases of PLANCK_LIVE_FRAMES (1200) frames:
// composited under a UI layer, direct, and direct with the Metal HUD pill.
// It reports present-to-glass for each and fails when direct mode's p50 is
// over half a refresh plus 1 ms (5.2 ms at 120 Hz): frames come at any
// point of the refresh, so a frame scanned out direct waits half of one
// for the next on average. With PLANCK_LIVE_SHOTS=dir it saves a screenshot
// mid-phase; run with MTL_HUD_ENABLED=1 to read the presentation mode off
// Metal's HUD. WindowServer decides whether a full-screen layer goes
// direct: when the HUD says Composited, so does the failure.
func TestLiveDirectGlass(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_DIRECT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_DIRECT_TEST=1: covers the main screen for about 40 s")
	}
	n := 1200
	if v := os.Getenv("PLANCK_LIVE_FRAMES"); v != "" {
		fmt.Sscan(v, &n)
	}
	const settle = 60
	win, _, sw, sh, hz := screenWindow(t)
	t.Logf("main screen %dx%d at %d Hz", sw, sh, hz)

	pipe, err := session.DefaultVideoOpener(session.VideoParams{Synth: true, Codec: encode.HEVC, Chroma: encode.NV12,
		Width: sw, Height: sh, FPS: 30, BitrateKbps: 40_000})
	if err != nil {
		t.Fatal(err)
	}
	per := n + settle
	var units [][]byte
	dropped := 0
	for len(units) < per {
		pkt, _, err := pipe.Next(time.Second, len(units) == 0, nil)
		if errors.Is(err, encode.ErrDropped) && dropped < per {
			// VideoToolbox drops frames while it warms up, and more at
			// this size; the stream is encoded ahead of time at 30 fps and
			// shown at the screen's rate.
			dropped++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, slices.Clone(pkt.Data))
	}
	pipe.Close()
	t.Logf("%d units encoded, %d frames dropped by the encoder", len(units), dropped)

	var surf *gpusurface.Surface
	onMain(func() { surf, err = gpusurface.New(gpusurface.Options{Window: win, Composite: true}) })
	if err != nil {
		t.Fatal(err)
	}
	defer onMain(surf.Close)
	p, err := player.Open(surf, decode.Config{Codec: encode.HEVC, Width: sw, Height: sh, FPS: hz}, 0)
	if err != nil {
		t.Fatal(err)
	}
	obs := &glassByPTS{glass: map[int64]float64{}, present: map[int64]float64{}}
	p.SetObserver(obs)

	shots := os.Getenv("PLANCK_LIVE_SHOTS")
	phases := []struct {
		name   string
		direct bool
		hud    bool
	}{{"composited", false, false}, {"direct", true, false}, {"direct+hud", true, true}}
	p50 := map[string]float64{}
	for k, ph := range phases {
		onMain(func() { err = surf.SetDirect(ph.direct) })
		if err != nil {
			t.Fatal(err)
		}
		p.SetHUD(player.HUDState{Visible: ph.hud, Signal: player.SignalDirect, Route: "LAN", FPS: 120, LatencyMs: 4.2, Mbps: 40, Codec: "HEVC"})
		base := int64(k * per)
		next := time.Now()
		for i, au := range units {
			if _, err := p.Submit(au, base+int64(i)); err != nil && err != decode.ErrNoFrame {
				t.Fatalf("%s: access unit %d: %v", ph.name, i, err)
			}
			if shots != "" && i == per/2 {
				out := filepath.Join(shots, ph.name+".png")
				go func() {
					if b, err := exec.Command("screencapture", "-x", "-m", out).CombinedOutput(); err != nil {
						t.Logf("screencapture: %v %s", err, b)
					}
				}()
			}
			next = next.Add(time.Second / time.Duration(hz))
			time.Sleep(time.Until(next))
		}
		time.Sleep(100 * time.Millisecond)
		obs.mu.Lock()
		var g, pr []float64
		for pts := base + settle; pts < base+int64(per); pts++ {
			if v, ok := obs.glass[pts]; ok {
				g = append(g, v)
			}
			if v, ok := obs.present[pts]; ok {
				pr = append(pr, v)
			}
		}
		obs.mu.Unlock()
		slices.Sort(g)
		slices.Sort(pr)
		pct := func(v []float64, q float64) float64 {
			if len(v) == 0 {
				return 0
			}
			return v[min(len(v)-1, int(q*float64(len(v))))]
		}
		p50[ph.name] = pct(g, 0.5)
		t.Logf("%-11s %4d of %d frames reached glass: present to glass p50 %.2f p95 %.2f p99 %.2f ms; present call p50 %.2f p99 %.2f ms",
			ph.name, len(g), n, pct(g, 0.5), pct(g, 0.95), pct(g, 0.99), pct(pr, 0.5), pct(pr, 0.99))
	}
	onMain(func() { err = surf.SetDirect(false) })
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	st := p.Stats()
	t.Logf("%+v, %d present errors", st, obs.errs)
	limit := 1000/float64(hz)/2 + 1
	if p50["direct"] > limit || p50["direct+hud"] > limit {
		t.Errorf("direct mode present to glass p50 %.2f ms, with the HUD %.2f ms, composited %.2f; want at most %.2f at %d Hz. "+
			"Run with MTL_HUD_ENABLED=1 PLANCK_LIVE_SHOTS=dir: a HUD that says Composited means WindowServer did not scan the layer out direct",
			p50["direct"], p50["direct+hud"], p50["composited"], limit, hz)
	}
}
