package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/wire"
)

func TestClampOnto(t *testing.T) {
	d := cursor.Rect{X: 1920, W: 5120, H: 1440}
	for _, c := range []struct {
		x, y   int
		wx, wy uint16
	}{
		{1920 + 2559, 1600, 32761, 65535},
		{100, -50, 0, 0},
		{9000, 700, 65535, 31879},
		{1920 + 100, 100, 1280, 4554},
	} {
		x, y, ok := clampOnto(cursor.State{X: c.x, Y: c.y}, d)
		if !ok || x != c.wx || y != c.wy {
			t.Errorf("(%d, %d): %d, %d, %v; want %d, %d", c.x, c.y, x, y, ok, c.wx, c.wy)
		}
	}
	if _, _, ok := clampOnto(cursor.State{X: 5, Y: 5}, cursor.Rect{}); ok {
		t.Error("clamped onto no display")
	}
}

// TestReleaseOffDisplayHasPlace: a game letting go of the mouse with the
// cursor off the streamed display, as one below y 1440 was while the host
// still took the display for 5120x1440, tells the client the nearest
// place on it, so the client's cursor goes there and not to the picture's
// centre, where its relative hold left it.
func TestReleaseOffDisplayHasPlace(t *testing.T) {
	var now atomic.Pointer[cursor.State]
	now.Store(&cursor.State{Visible: true, X: 100, Y: 100})
	var info atomic.Pointer[VideoInfo]
	info.Store(&VideoInfo{Width: 5120, Height: 1440, FPS: 60, Display: rect(0, 0, 5120, 1440), Desktop: rect(0, 0, 5120, 1440)})
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", NoTailscale: true,
		FPS: 60, BitrateKbps: 10_000, PIN: "246810",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &modePipe{fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, &info}, nil
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
	now.Store(&cursor.State{Captured: true, X: 2560, Y: 720})
	waitFor(t, "the capture", func() bool { return len(seen()) == 2 })
	now.Store(&cursor.State{X: 2560, Y: 1600})
	waitFor(t, "the release", func() bool { return len(seen()) == 3 })
	if s := seen()[2]; s.Captured || !s.HasPos || s.X != 32773 || s.Y != 65535 {
		t.Fatalf("release %+v, want the display's bottom edge below the cursor", s)
	}
	// A hidden cursor going off the display with no capture to release says
	// nothing of a place.
	now.Store(&cursor.State{Visible: true, X: 2560, Y: 700})
	waitFor(t, "the cursor shown", func() bool { return len(seen()) == 4 })
	now.Store(&cursor.State{X: 2560, Y: 1600})
	time.Sleep(6 * cursorPoll)
	if s := seen(); len(s) != 5 || s[4].HasPos {
		t.Fatalf("states %+v, want a hide with no place", s)
	}
}
