package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/ratectl"
)

// TestOnSessionHearsStartAndEnd: the host tells OnSession of a client's
// stream as it starts and as it ends, with its codec and route kind.
func TestOnSessionHearsStartAndEnd(t *testing.T) {
	var mu sync.Mutex
	var events []SessionEvent
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 120, BitrateKbps: 40_000, Slices: 2, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		OnSession: func(e SessionEvent) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
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
	seen := func() []SessionEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]SessionEvent(nil), events...)
	}

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session started", func() bool { return len(seen()) >= 1 })
	if e := seen()[0]; !e.Started || e.Codec != encode.HEVC || e.Path != ratectl.PathLAN {
		t.Fatalf("start event %+v", e)
	}
	r.client.Disconnect()
	waitFor(t, "session ended", func() bool { return len(seen()) >= 2 })
	if e := seen()[1]; e.Started || e.Codec != encode.HEVC {
		t.Fatalf("end event %+v", e)
	}
}
