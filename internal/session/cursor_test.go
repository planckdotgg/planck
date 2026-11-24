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
