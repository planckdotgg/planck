package session

import (
	"slices"
	"testing"
)

func TestApplyGain(t *testing.T) {
	pcm := []int16{-32768, -1000, 0, 1000, 32767}
	applyGain(pcm, 1)
	if want := []int16{-32768, -1000, 0, 1000, 32767}; !slices.Equal(pcm, want) {
		t.Fatalf("gain 1 changed samples: %v", pcm)
	}
	applyGain(pcm, 0.5)
	if want := []int16{-16384, -500, 0, 500, 16383}; !slices.Equal(pcm, want) {
		t.Fatalf("gain 0.5: %v, want %v", pcm, want)
	}
	applyGain(pcm, 0)
	if want := make([]int16, 5); !slices.Equal(pcm, want) {
		t.Fatalf("gain 0: %v", pcm)
	}
}

// TestLoopbackVolume: a client silenced before it connects plays only
// silence, and turning it back up reaches the stream already playing.
func TestLoopbackVolume(t *testing.T) {
	hs := startHost(t, t.TempDir(), "127.0.0.1:0", &fakeWhoIs{sameUser: false}, "off")
	r := newClient(t, t.TempDir(), hs.host.PIN())
	r.client.SetVolume(0)
	if err := connect(t, r, hs.host.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "audio frames", func() bool { return r.player.frames.Load() >= 50 })
	if p := r.player.peak.Load(); p != 0 {
		t.Fatalf("muted stream peaked at %d", p)
	}
	r.client.SetVolume(1)
	waitFor(t, "audible audio", func() bool { return r.player.peak.Load() > 2000 })
}
