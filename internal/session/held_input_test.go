package session

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/input"
)

// recordInjector keeps what the host would have injected.
type recordInjector struct {
	mu  sync.Mutex
	evs []input.Event
}

func (r *recordInjector) Inject(ev input.Event) error {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
	return nil
}

func (r *recordInjector) Close() error { return nil }

func (r *recordInjector) events() []input.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.evs)
}

// TestHostReleasesHeldInputOnDisconnect: a client that drops while it
// holds Alt-Tab and a mouse button, as one whose system key capture was
// cut off, leaves nothing down on the host.
func TestHostReleasesHeldInputOnDisconnect(t *testing.T) {
	rec := &recordInjector{}
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", FPS: 60, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
		OpenAudio:   func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		InjectInput: true, Injector: rec, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	sent := []input.Event{
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftAlt, Pressed: true, Modifiers: input.ModLeftAlt, SystemKeyCapture: true}},
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyTab, Pressed: true, Modifiers: input.ModLeftAlt, SystemKeyCapture: true}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft, Pressed: true}},
	}
	for _, ev := range sent {
		if err := r.client.SendInput(ev); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the presses at the host", func() bool { return len(rec.events()) >= len(sent) })
	r.client.Disconnect()
	want := append(slices.Clone(sent),
		input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft}},
		input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyTab, Modifiers: input.ModLeftAlt}},
		input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyLeftAlt}},
	)
	waitFor(t, "the host to let go", func() bool { return len(rec.events()) >= len(want) })
	if got := rec.events(); !slices.Equal(got, want) {
		t.Fatalf("injected:\n got %+v\nwant %+v", got, want)
	}
}
