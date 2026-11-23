//go:build darwin || windows

package audio

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live device tests touch the real audio hardware, so they only run with
// PLANCK_LIVE_AUDIO_TEST=1. The player test writes silence.
func requireLive(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_AUDIO_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_AUDIO_TEST=1 to use the real audio devices")
	}
}

func TestLivePlayerSilence(t *testing.T) {
	requireLive(t)
	p, err := NewPlayer(PlayerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, FrameSamples(DefaultFrameDuration)*Channels)
	tick := time.NewTicker(DefaultFrameDuration)
	defer tick.Stop()
	for range 200 {
		<-tick.C
		if err := p.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	st := p.Stats()
	t.Logf("player stats after 2s: %+v", st)
	if st.Dropped != 0 {
		t.Errorf("dropped %d samples; the device is consuming slower than real time", st.Dropped)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(frame); err != ErrClosed {
		t.Fatalf("write after close = %v, want ErrClosed", err)
	}
}

func TestLiveCapture(t *testing.T) {
	requireLive(t)
	c, err := NewCapturer()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var samples int
	var first, last int64
	calls := 0
	if err := c.Start(ctx, func(pcm []int16, pts int64) {
		if len(pcm)%Channels != 0 {
			t.Errorf("odd sample count %d", len(pcm))
		}
		if calls == 0 {
			first = pts
		}
		last = pts
		calls++
		samples += len(pcm) / Channels
	}); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d callbacks, %d frames (%.0f ms), pts span %.1f ms", calls, samples, float64(samples)/48, float64(last-first)/1e6)
	if samples < SampleRate {
		t.Errorf("captured %d frames in 2s, want at least 1s worth", samples)
	}
}
