package session

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func TestWindowOrdered(t *testing.T) {
	w := newWindow(3)
	if got := w.ordered(nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	w.add(1)
	w.add(2)
	if got := w.ordered(nil); !slices.Equal(got, []float64{1, 2}) {
		t.Fatalf("partial: %v", got)
	}
	w.add(3)
	w.add(4)
	if got := w.ordered(nil); !slices.Equal(got, []float64{2, 3, 4}) {
		t.Fatalf("wrapped: %v", got)
	}
}

// TestClientStatsHostPolicyAndFrameTimes: the client hears the policy the
// host forces, and keeps the frame times and keyframes of its stream.
func TestClientStatsHostPolicyAndFrameTimes(t *testing.T) {
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: testHostCaps, Logf: t.Logf,
		Policy: defaults.UserOverrides{FPS: "60", BitrateMbps: 40},
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	r := capsClient(t, h.PIN(), defaults.UserOverrides{})
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 20 })
	cs := r.client.Stats()
	if cs.HostPolicy.FPS != "60" || cs.HostPolicy.BitrateMbps != 40 {
		t.Errorf("host policy %+v", cs.HostPolicy)
	}
	if len(cs.FrameTimesMs) < 10 || cs.Keyframes == 0 {
		t.Fatalf("%d frame times, %d keyframes", len(cs.FrameTimesMs), cs.Keyframes)
	}
	slices.Sort(cs.FrameTimesMs)
	if p50 := cs.FrameTimesMs[len(cs.FrameTimesMs)/2]; p50 < 8 || p50 > 40 {
		t.Errorf("frame time p50 %.2f ms at 60 fps", p50)
	}
}
