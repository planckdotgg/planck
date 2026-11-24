package session

import (
	"context"
	"errors"
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

func testShape(n int) cursor.Shape {
	return cursor.Shape{W: 2, H: 1, DPI: 96, RGBA: []byte{byte(n), byte(n >> 8), 0, 255, 0, 0, 0, 0}}
}

// TestShapeCachesStayAlike: across many changes among more shapes than
// either end keeps, and a new stream starting the host's cache afresh in
// the client's session, every reference the host sends names a shape the
// client still has.
func TestShapeCachesStayAlike(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	var host, client shapeCache
	refs := 0
	for i := range 5000 {
		if i == 2500 {
			host = shapeCache{}
		}
		n := r.IntN(48)
		if i%3 == 0 {
			n = r.IntN(4)
		}
		sh := testShape(n)
		var m wire.CursorShape
		if err := host.send(sh, sh.Hash(), func(w wire.Message) error { m = *w.(*wire.CursorShape); return nil }); err != nil {
			t.Fatal(err)
		}
		b, err := wire.AppendMessage(nil, &m)
		if err != nil {
			t.Fatal(err)
		}
		dm, err := wire.DecodeMessage(b[4:])
		if err != nil {
			t.Fatal(err)
		}
		got, ok := client.resolve(*dm.(*wire.CursorShape))
		if !ok || got.ID != sh.Hash() || !slices.Equal(got.RGBA, sh.RGBA) {
			t.Fatalf("change %d to shape %d: got %+v, %v", i, n, got, ok)
		}
		if m.Ref() {
			refs++
		}
		if len(host.shapes) > wire.CursorCacheSize || len(client.shapes) > wire.CursorCacheSize {
			t.Fatalf("caches grew to %d and %d", len(host.shapes), len(client.shapes))
		}
	}
	if refs < 2000 {
		t.Errorf("only %d of 5000 changes went as references", refs)
	}
	if _, ok := client.resolve(wire.CursorShape{ID: 12345}); ok {
		t.Error("resolved a reference to a shape never sent")
	}
}

// TestCursorShapeReachesClient: a client that asks for the host's cursor
// image gets it as the stream starts and as the cursor changes shape,
// whole even when the host refers to one sent before; the host reads each
// cursor once, not on every poll.
func TestCursorShapeReachesClient(t *testing.T) {
	var now atomic.Pointer[cursor.State]
	now.Store(&cursor.State{Visible: true, Handle: 1})
	var reads atomic.Int32
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
		FPS: 60, BitrateKbps: 10_000, PIN: "246810",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
		OpenAudio:  func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		ReadCursor: func() (cursor.State, error) { return *now.Load(), nil },
		ReadCursorShape: func(s cursor.State) (cursor.Shape, error) {
			reads.Add(1)
			return testShape(int(s.Handle % 2)), nil
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	var mu sync.Mutex
	var got []wire.CursorShape
	seen := func() []wire.CursorShape {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.CursorShape(nil), got...)
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "test-client", PIN: "246810", NoAudio: true,
		Quality: Quality{Codec: encode.HEVC, FPS: 60}, OpenSink: NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf,
		OnCursorShape: func(s wire.CursorShape) {
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
	waitFor(t, "the first shape", func() bool { return len(seen()) == 1 })
	time.Sleep(10 * cursorPoll)
	if n := reads.Load(); n != 1 {
		t.Fatalf("read the same cursor %d times", n)
	}
	now.Store(&cursor.State{Visible: true, Handle: 2})
	waitFor(t, "the second shape", func() bool { return len(seen()) == 2 })
	now.Store(&cursor.State{})
	time.Sleep(5 * cursorPoll)
	now.Store(&cursor.State{Visible: true, Handle: 3})
	waitFor(t, "the first shape again", func() bool { return len(seen()) == 3 })
	// Another handle with the image already shown sends nothing.
	now.Store(&cursor.State{Visible: true, Handle: 5})
	time.Sleep(10 * cursorPoll)
	s := seen()
	a, b := testShape(1), testShape(0)
	if len(s) != 3 || s[0].ID != a.Hash() || s[1].ID != b.Hash() || s[2].ID != a.Hash() || !slices.Equal(s[2].RGBA, a.RGBA) || s[2].Width != 2 {
		t.Fatalf("shapes %+v", s)
	}
}

func noisyShape(seed uint64, side, hx, hy, dpi int) cursor.Shape {
	r := rand.New(rand.NewPCG(seed, 9))
	px := make([]byte, side*side*4)
	for i := range px {
		px[i] = byte(r.Uint32())
	}
	return cursor.Shape{W: side, H: side, HotX: hx, HotY: hy, DPI: dpi, RGBA: px}
}

// TestLargeShapeFitsControlFrame: a 256 x 256 cursor that does not
// compress is halved until it fits a control frame, its hotspot and DPI
// with it, so that it shows at the same size and point on the client.
func TestLargeShapeFitsControlFrame(t *testing.T) {
	sh := noisyShape(1, 256, 200, 100, 192)
	if _, err := wire.AppendMessage(nil, &wire.CursorShape{ID: 1, Width: 256, Height: 256, DPI: 192, RGBA: sh.RGBA}); !errors.Is(err, wire.ErrTooLarge) {
		t.Fatalf("the whole shape encodes: %v", err)
	}
	var host, client shapeCache
	var sent []byte
	err := host.send(sh, sh.Hash(), func(m wire.Message) error {
		b, err := wire.AppendMessage(nil, m)
		sent = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := wire.DecodeMessage(sent[4:])
	if err != nil {
		t.Fatal(err)
	}
	got, ok := client.resolve(*dm.(*wire.CursorShape))
	if !ok || got.ID != sh.Hash() || got.Width != 64 || got.Height != 64 || got.HotX != 50 || got.HotY != 25 || got.DPI != 48 {
		t.Fatalf("got %dx%d hot %d,%d DPI %d, %v", got.Width, got.Height, got.HotX, got.HotY, got.DPI, ok)
	}
	if m, _ := host.message(sh, sh.Hash()); !m.Ref() {
		t.Error("the host does not refer to the shape it sent")
	}
}

// TestShapeCachesSurviveFailedSends: a shape or reference the control
// stream refuses, as it does a full queue, never enters the host's cache
// or moves within it, so every reference the client gets resolves.
func TestShapeCachesSurviveFailedSends(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	var host, client shapeCache
	failed := 0
	for i := range 5000 {
		n := r.IntN(48)
		sh := testShape(n)
		if n == 47 {
			sh = noisyShape(uint64(i%3), 256, 3, 3, 96)
		}
		before := slices.Clone(host.shapes)
		fail := r.IntN(3) == 0
		err := host.send(sh, sh.Hash(), func(m wire.Message) error {
			if fail {
				return errors.New("queue full")
			}
			b, err := wire.AppendMessage(nil, m)
			if err != nil {
				return err
			}
			dm, err := wire.DecodeMessage(b[4:])
			if err != nil {
				return err
			}
			if got, ok := client.resolve(*dm.(*wire.CursorShape)); !ok || got.ID != sh.Hash() {
				t.Fatalf("change %d to shape %d: got %+v, %v", i, n, got, ok)
			}
			return nil
		})
		if fail {
			failed++
			if err == nil || !slices.EqualFunc(before, host.shapes, func(a, b wire.CursorShape) bool { return a.ID == b.ID }) {
				t.Fatalf("change %d: a refused send changed the host's cache (err %v)", i, err)
			}
		} else if err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	if failed < 1000 {
		t.Errorf("only %d sends refused", failed)
	}
}

// TestLargeShapeKeepsSession: a 256 x 256 cursor too large for a control
// frame whole reaches the client halved, and the session goes on to carry
// the next shape.
func TestLargeShapeKeepsSession(t *testing.T) {
	var now atomic.Pointer[cursor.State]
	now.Store(&cursor.State{Visible: true, Handle: 1})
	big := noisyShape(2, 256, 10, 10, 96)
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
		FPS: 60, BitrateKbps: 10_000, PIN: "246810",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
		OpenAudio:  func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		ReadCursor: func() (cursor.State, error) { return *now.Load(), nil },
		ReadCursorShape: func(s cursor.State) (cursor.Shape, error) {
			if s.Handle == 1 {
				return big, nil
			}
			return testShape(0), nil
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	var mu sync.Mutex
	var got []wire.CursorShape
	seen := func() []wire.CursorShape {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.CursorShape(nil), got...)
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "test-client", PIN: "246810", NoAudio: true,
		Quality: Quality{Codec: encode.HEVC, FPS: 60}, OpenSink: NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf,
		OnCursorShape: func(s wire.CursorShape) {
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
	waitFor(t, "the large shape", func() bool { return len(seen()) == 1 })
	now.Store(&cursor.State{Visible: true, Handle: 2})
	waitFor(t, "the next shape", func() bool { return len(seen()) == 2 })
	s := seen()
	if s[0].ID != big.Hash() || s[0].Width != 64 || s[0].DPI != 24 || s[1].ID != testShape(0).Hash() {
		t.Fatalf("shapes %+v %+v", s[0].Width, s[1].ID)
	}
}

// TestWarpCaptureStall: a hidden cursor that stops where the last move put
// it while the host's input stalls, as a cursor: none video's can, is not
// a capture for a stall of up to warpStallPolls polls; a game that holds
// the cursor at a point it happened to follow a move to still is, then.
func TestWarpCaptureStall(t *testing.T) {
	var w warpCapture
	var moves uint32
	w.observe(wire.CursorState{HasPos: true, X: 10000, Y: 10000}, moves, 0, 0)
	p := [2]uint16{20000, 20000}
	warpRun(&w, &moves, []*[2]uint16{pt(15000, 15000), pt(20000, 20000)}, [][2]uint16{{15000, 15000}, p})
	var targets []*[2]uint16
	var at [][2]uint16
	for i := range warpStallPolls - 1 {
		targets = append(targets, pt(uint16(22000+i*1500), 20000))
		at = append(at, p)
	}
	for i, c := range warpRun(&w, &moves, targets, at) {
		if c {
			t.Fatalf("captured on poll %d of a stall", i)
		}
	}
	if !warpRun(&w, &moves, []*[2]uint16{pt(50000, 20000)}, [][2]uint16{p})[0] {
		t.Fatal("not captured after warpStallPolls polls held at one point")
	}
}
