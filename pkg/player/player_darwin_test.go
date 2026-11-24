//go:build darwin

package player_test

import (
	"os"
	"runtime"
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

// AppKit takes windows on the main thread only: the tests run on another
// goroutine and hand their AppKit calls to onMain.
func init() { runtime.LockOSThread() }

var mainCalls = make(chan func())

func onMain(fn func()) {
	done := make(chan struct{})
	mainCalls <- func() { fn(); close(done) }
	<-done
}

func TestMain(m *testing.M) {
	code := make(chan int, 1)
	go func() { code <- m.Run() }()
	for {
		select {
		case fn := <-mainCalls:
			fn()
		case c := <-code:
			os.Exit(c)
		}
	}
}

type rect struct{ X, Y, W, H float64 }

// window shows a small window in the bottom-left corner of the screen
// that never takes focus, for presentedTime to be real.
func window(t *testing.T, w, h float64) uintptr {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		t.Fatal(err)
	}
	var initWindow func(obj objc.ID, sel objc.SEL, r rect, style, backing uintptr, deferred bool) objc.ID
	purego.RegisterLibFunc(&initWindow, purego.RTLD_DEFAULT, "objc_msgSend")
	var win objc.ID
	onMain(func() {
		objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
		const borderless, buffered = 0, 2
		win = initWindow(objc.ID(objc.GetClass("NSWindow")).Send(objc.RegisterName("alloc")),
			objc.RegisterName("initWithContentRect:styleMask:backing:defer:"), rect{X: 40, Y: 40, W: w, H: h}, borderless, buffered, false)
		win.Send(objc.RegisterName("setReleasedWhenClosed:"), false)
		win.Send(objc.RegisterName("setIgnoresMouseEvents:"), true)
		win.Send(objc.RegisterName("orderFrontRegardless"))
	})
	t.Cleanup(func() {
		onMain(func() {
			win.Send(objc.RegisterName("close"))
			win.Send(objc.RegisterName("release"))
		})
	})
	return uintptr(win)
}

type observer struct {
	mu        sync.Mutex
	presented int
	errs      int
	firstErr  error
	glass     []float64
}

func (o *observer) Presented(_ player.Timing, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		if o.errs == 0 {
			o.firstErr = err
		}
		o.errs++
		return
	}
	o.presented++
}

func (o *observer) Glass(_ int64, glass, _ time.Duration) {
	o.mu.Lock()
	o.glass = append(o.glass, float64(glass)/1e6)
	o.mu.Unlock()
}

// TestPresentThread decodes a VideoToolbox stream at 120 fps into a shown
// window through the player's present thread, and checks that every
// picture was decoded, the pictures were shown, and glass times came back.
func TestPresentThread(t *testing.T) {
	const w, h, n = 1280, 720, 240
	pipe, err := session.DefaultVideoOpener(session.VideoParams{Synth: true, Codec: encode.HEVC, Chroma: encode.NV12,
		Width: w, Height: h, FPS: 120, BitrateKbps: 20_000})
	if err != nil {
		t.Fatal(err)
	}
	var units [][]byte
	for i := range n {
		pkt, _, err := pipe.Next(time.Second, i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, slices.Clone(pkt.Data))
	}
	pipe.Close()

	win := window(t, 320, 180)
	var surf *gpusurface.Surface
	onMain(func() { surf, err = gpusurface.New(gpusurface.Options{Window: win}) })
	if err != nil {
		t.Fatal(err)
	}
	defer onMain(surf.Close)
	p, err := player.Open(surf, decode.Config{Codec: encode.HEVC, Width: w, Height: h, FPS: 120}, 0)
	if err != nil {
		t.Fatal(err)
	}
	obs := &observer{}
	p.SetObserver(obs)
	next := time.Now()
	for i, au := range units {
		if _, err := p.Submit(au, int64(i)); err != nil && err != decode.ErrNoFrame {
			t.Fatalf("access unit %d: %v", i, err)
		}
		next = next.Add(time.Second / 120)
		time.Sleep(time.Until(next))
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	st := p.Stats()
	obs.mu.Lock()
	defer obs.mu.Unlock()
	slices.Sort(obs.glass)
	pct := func(q float64) float64 {
		if len(obs.glass) == 0 {
			return 0
		}
		return obs.glass[min(len(obs.glass)-1, int(q*float64(len(obs.glass))))]
	}
	t.Logf("%+v; observer %d presented, %d errors (first %v), %d glass, present to glass p50 %.2f p95 %.2f p99 %.2f ms",
		st, obs.presented, obs.errs, obs.firstErr, len(obs.glass), pct(0.5), pct(0.95), pct(0.99))
	switch {
	case st.Pictures+st.NoPicture != n || st.NoPicture > n/10:
		t.Fatalf("%d pictures, %d units without one, of %d", st.Pictures, st.NoPicture, n)
	case st.Presented+st.Superseded+st.PresentErrors != st.Pictures || st.Presented < n/2:
		t.Fatalf("%d presented, %d superseded, %d failed of %d pictures", st.Presented, st.Superseded, st.PresentErrors, st.Pictures)
	case st.PresentErrors != obs.errs || st.PresentErrors > n/100:
		// Core Animation now and then has no drawable within its 1 s
		// timeout on a loaded machine; the picture goes back unshown.
		t.Fatalf("%d present errors, first %v", st.PresentErrors, obs.firstErr)
	case len(obs.glass) < st.Presented/2:
		t.Fatalf("glass time for %d of %d pictures shown", len(obs.glass), st.Presented)
	}
}
