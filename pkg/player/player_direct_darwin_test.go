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
	errs  int
}

func (o *glassByPTS) Presented(_ player.Timing, err error) {
	if err != nil {
		o.mu.Lock()
		o.errs++
		o.mu.Unlock()
	}
}

func (o *glassByPTS) Glass(pts int64, glass, _ time.Duration) {
	o.mu.Lock()
	o.glass[pts] = float64(glass) / 1e6
	o.mu.Unlock()
}

// screenWindow covers the main screen with a borderless window above the
// menu bar that never takes focus or the mouse, holding a composite
// surface and, over it, a layer-backed view with a small opaque badge
// standing for the client's own UI.
func screenWindow(t *testing.T) (win, ui uintptr, w, h int) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		t.Fatal(err)
	}
	var initWindow func(obj objc.ID, sel objc.SEL, r rect, style, backing uintptr, deferred bool) objc.ID
	var initView func(obj objc.ID, sel objc.SEL, r rect) objc.ID
	var frame func(obj objc.ID, sel objc.SEL) rect
	var color func(cls objc.ID, sel objc.SEL, r, g, b, a float64) objc.ID
	purego.RegisterLibFunc(&initWindow, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&initView, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&frame, purego.RTLD_DEFAULT, "objc_msgSend")
	purego.RegisterLibFunc(&color, purego.RTLD_DEFAULT, "objc_msgSend")
	sel := objc.RegisterName
	var nsWin, view objc.ID
	onMain(func() {
		objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
		screen := objc.ID(objc.GetClass("NSScreen")).Send(sel("mainScreen"))
		f := frame(screen, sel("frame"))
		scale := objc.Send[float64](screen, sel("backingScaleFactor"))
		w, h = int(f.W*scale), int(f.H*scale)
		const borderless, buffered, screenSaverLevel = 0, 2, 1000
		nsWin = initWindow(objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")), sel("initWithContentRect:styleMask:backing:defer:"), f, borderless, buffered, false)
		nsWin.Send(sel("setReleasedWhenClosed:"), false)
		nsWin.Send(sel("setIgnoresMouseEvents:"), true)
		nsWin.Send(sel("setLevel:"), uintptr(screenSaverLevel))
		content := nsWin.Send(sel("contentView"))
		view = initView(objc.ID(objc.GetClass("NSView")).Send(sel("alloc")), sel("initWithFrame:"), rect{W: f.W, H: f.H})
		view.Send(sel("setWantsLayer:"), true)
		badge := initView(objc.ID(objc.GetClass("NSView")).Send(sel("alloc")), sel("initWithFrame:"), rect{X: f.W/2 - 150, Y: f.H - 60, W: 300, H: 40})
		badge.Send(sel("setWantsLayer:"), true)
		c := color(objc.ID(objc.GetClass("NSColor")), sel("colorWithSRGBRed:green:blue:alpha:"), 0.055, 0.059, 0.071, 0.85)
		badge.Send(sel("layer")).Send(sel("setBackgroundColor:"), c.Send(sel("CGColor")))
		view.Send(sel("addSubview:"), badge)
		badge.Send(sel("release"))
		content.Send(sel("addSubview:"), view)
		nsWin.Send(sel("orderFrontRegardless"))
	})
	t.Cleanup(func() {
		onMain(func() {
			nsWin.Send(sel("close"))
			view.Send(sel("release"))
			nsWin.Send(sel("release"))
		})
	})
	return uintptr(nsWin), uintptr(view), w, h
}

// TestLiveDirectGlass covers the main screen and presents a 120 fps HEVC
// stream of the screen's size through the player in three phases of
// PLANCK_LIVE_FRAMES (1200) frames: composited under a UI layer, direct,
// and direct with the Metal HUD pill. It reports present-to-glass for
// each and fails when direct mode's p50 is over 5 ms. With
// PLANCK_LIVE_SHOTS=dir it saves a screenshot mid-phase; run with
// MTL_HUD_ENABLED=1 to read the presentation mode off Metal's HUD.
func TestLiveDirectGlass(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_DIRECT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_DIRECT_TEST=1: covers the main screen for about 40 s")
	}
	n := 1200
	if v := os.Getenv("PLANCK_LIVE_FRAMES"); v != "" {
		fmt.Sscan(v, &n)
	}
	const settle = 60
	win, _, sw, sh := screenWindow(t)
	t.Logf("main screen %dx%d", sw, sh)

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
			// shown at 120.
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
	p, err := player.Open(surf, decode.Config{Codec: encode.HEVC, Width: sw, Height: sh, FPS: 120}, 0)
	if err != nil {
		t.Fatal(err)
	}
	obs := &glassByPTS{glass: map[int64]float64{}}
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
			next = next.Add(time.Second / 120)
			time.Sleep(time.Until(next))
		}
		time.Sleep(100 * time.Millisecond)
		obs.mu.Lock()
		var g []float64
		for pts := base + settle; pts < base+int64(per); pts++ {
			if v, ok := obs.glass[pts]; ok {
				g = append(g, v)
			}
		}
		obs.mu.Unlock()
		slices.Sort(g)
		pct := func(q float64) float64 {
			if len(g) == 0 {
				return 0
			}
			return g[min(len(g)-1, int(q*float64(len(g))))]
		}
		p50[ph.name] = pct(0.5)
		t.Logf("%-11s %4d of %d frames reached glass: present to glass p50 %.2f p95 %.2f p99 %.2f ms",
			ph.name, len(g), n, pct(0.5), pct(0.95), pct(0.99))
	}
	onMain(func() { err = surf.SetDirect(false) })
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	st := p.Stats()
	t.Logf("%+v, %d present errors", st, obs.errs)
	if p50["direct"] > 5 || p50["direct+hud"] > 5 {
		t.Errorf("direct mode present to glass p50 %.2f ms, with the HUD %.2f ms; want at most 5", p50["direct"], p50["direct+hud"])
	}
}
