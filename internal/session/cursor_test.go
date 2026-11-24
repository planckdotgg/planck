package session

import (
	"context"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/wire"
)

func TestCursorMessage(t *testing.T) {
	d := cursor.Rect{X: 1920, W: 2560, H: 1440}
	m := cursorMessage(cursor.State{Visible: true, X: 1920 + 2559, Y: 0}, d)
	if m != (wire.CursorState{Visible: true, HasPos: true, X: 65535}) {
		t.Errorf("on the display: %+v", m)
	}
	m = cursorMessage(cursor.State{Captured: true, X: 100, Y: 100}, d)
	if m != (wire.CursorState{Captured: true}) {
		t.Errorf("off the display: %+v", m)
	}
}

// TestCursorStateReachesClient: the host tells the client how its cursor
// stands as the stream starts, and again when a game captures the mouse
// and lets it go, but not for moves alone.
func TestCursorStateReachesClient(t *testing.T) {
	var now atomic.Pointer[cursor.State]
	now.Store(&cursor.State{Visible: true})
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
		FPS: 60, BitrateKbps: 10_000, PIN: "246810",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
		OpenAudio:  func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		ReadCursor: func() (cursor.State, error) { return *now.Load(), nil },
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	var mu sync.Mutex
	var got []wire.CursorState
	seen := func() []wire.CursorState {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.CursorState(nil), got...)
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "test-client", PIN: "246810", NoAudio: true,
		Quality: Quality{Codec: encode.HEVC, FPS: 60}, OpenSink: NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf,
		OnCursor: func(s wire.CursorState) {
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first cursor state", func() bool { return len(seen()) == 1 })
	if s := seen()[0]; !s.Visible || s.Captured {
		t.Fatalf("first state %+v, want visible and free", s)
	}

	now.Store(&cursor.State{Visible: true, X: 50, Y: 60})
	time.Sleep(10 * cursorPoll)
	now.Store(&cursor.State{Captured: true})
	waitFor(t, "the capture", func() bool { return len(seen()) == 2 })
	now.Store(&cursor.State{Visible: true})
	waitFor(t, "the release", func() bool { return len(seen()) == 3 })
	s := seen()
	if s[1] != (wire.CursorState{Captured: true}) || !s[2].Visible || s[2].Captured {
		t.Fatalf("states %+v: want captured, then visible and free, and nothing for the move", s)
	}
}

// warpRun feeds w polls of a hidden cursor: each poll injects a move to
// targets[i] unless it is nil, and finds the cursor at at[i].
func warpRun(w *warpCapture, moves *uint32, targets []*[2]uint16, at [][2]uint16) []bool {
	var got []bool
	var last [2]uint16
	for i := range at {
		if targets[i] != nil {
			*moves++
			last = *targets[i]
		}
		m := wire.CursorState{HasPos: true, X: at[i][0], Y: at[i][1]}
		got = append(got, w.observe(m, *moves, last[0], last[1]))
	}
	return got
}

func pt(x, y uint16) *[2]uint16 { return &[2]uint16{x, y} }

// TestWarpCaptureRecentringGame: a hidden cursor taken back to one place
// after each injected move is a capture, on the third poll.
func TestWarpCaptureRecentringGame(t *testing.T) {
	var w warpCapture
	var moves uint32
	w.observe(wire.CursorState{HasPos: true, X: 32768, Y: 32768}, moves, 0, 0)
	centre := [2]uint16{32768, 32768}
	got := warpRun(&w, &moves,
		[]*[2]uint16{pt(40000, 30000), pt(45000, 28000), pt(50000, 26000), pt(52000, 25000)},
		[][2]uint16{centre, {32800, 32700}, centre, centre})
	if want := []bool{false, false, true, true}; !slices.Equal(got, want) {
		t.Fatalf("captured %v, want %v", got, want)
	}
	// It lets go as the cursor shows, as a game's menu has it.
	if w.observe(wire.CursorState{Visible: true, HasPos: true, X: 32768, Y: 32768}, moves, 0, 0) {
		t.Fatal("a shown cursor stayed captured")
	}
}

// TestWarpCaptureNeverFalse: a hidden cursor that goes where it is moved,
// that nothing moves, that lags behind the moves, or that moves on the
// host's own mouse is never a capture.
func TestWarpCaptureNeverFalse(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	rnd := func() [2]uint16 { return [2]uint16{uint16(r.IntN(65536)), uint16(r.IntN(65536))} }
	for name, poll := range map[string]func(i int) (*[2]uint16, [2]uint16){
		"a video in a browser, cursor: none": func(int) (*[2]uint16, [2]uint16) {
			p := rnd()
			return &p, [2]uint16{p[0] + uint16(r.IntN(3)), p[1]}
		},
		"hidden while typing": func(int) (*[2]uint16, [2]uint16) { return nil, [2]uint16{20000, 20000} },
		"the cursor a move behind": func(i int) (*[2]uint16, [2]uint16) {
			return pt(uint16(1000+i*2000%60000), 30000), [2]uint16{uint16(1000 + (i-1)*2000%60000), 30000}
		},
		"the host's own mouse": func(int) (*[2]uint16, [2]uint16) { return nil, rnd() },
		"held at the display's edge": func(int) (*[2]uint16, [2]uint16) {
			p := rnd()
			return &p, [2]uint16{65535, 65535}
		},
	} {
		var w warpCapture
		var moves uint32
		for i := range 500 {
			target, at := poll(i)
			if w.observe(wire.CursorState{HasPos: true, X: at[0], Y: at[1]}, moves, 0, 0) {
				t.Fatalf("%s: captured at poll %d", name, i)
			}
			if target != nil {
				moves++
				w.observe(wire.CursorState{HasPos: true, X: at[0], Y: at[1]}, moves, target[0], target[1])
			}
		}
	}
}

// TestWarpCaptureReleasesOnFollow: a capture holds while the game keeps
// taking the cursor back, through a poll that catches it before the game
// does, and ends after the cursor follows the moves three polls in a row.
func TestWarpCaptureReleasesOnFollow(t *testing.T) {
	var w warpCapture
	var moves uint32
	w.observe(wire.CursorState{HasPos: true, X: 32768, Y: 32768}, moves, 0, 0)
	c := [2]uint16{32768, 32768}
	warpRun(&w, &moves, []*[2]uint16{pt(1e4, 1e4), pt(2e4, 1e4), pt(3e4, 1e4)}, [][2]uint16{c, c, c})
	got := warpRun(&w, &moves,
		[]*[2]uint16{pt(5e3, 5e3), pt(6e3, 5e3), nil, pt(7e3, 5e3), pt(8e3, 5e3), pt(9e3, 5e3)},
		[][2]uint16{{5e3, 5e3}, c, c, {7e3, 5e3}, {8e3, 5e3}, {9e3, 5e3}})
	if want := []bool{true, true, true, true, true, false}; !slices.Equal(got, want) {
		t.Fatalf("captured %v, want %v", got, want)
	}
}
