package audio

import (
	"context"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func requireLiveLinux(t *testing.T) {
	t.Helper()
	if os.Getenv("PLANCK_LIVE_AUDIO_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_AUDIO_TEST=1 to use the sound server")
	}
	if err := probePulse(); err != nil {
		t.Skip(err)
	}
}

func TestLiveLinuxDevices(t *testing.T) {
	requireLiveLinux(t)
	out, err := OutputDevices()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Skip("no output devices")
	}
	def := 0
	for _, d := range out {
		if d.ID == "" || d.Name == "" {
			t.Errorf("incomplete device %+v", d)
		}
		if d.IsDefault {
			def++
		}
	}
	if def != 1 {
		t.Errorf("%d default outputs among %+v", def, out)
	}
	if _, err := InputDevices(); err != nil {
		t.Fatal(err)
	}
	apps, err := RunningApps()
	if err != nil || len(apps) == 0 {
		t.Fatalf("RunningApps = %d, %v", len(apps), err)
	}
}

func TestLiveLinuxPlayerAndCapture(t *testing.T) {
	requireLiveLinux(t)
	c, err := NewCapturer(CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	var samples int
	var first, last int64
	if err := c.Start(ctx, func(pcm []int16, pts int64) {
		mu.Lock()
		defer mu.Unlock()
		if len(pcm)%Channels != 0 {
			t.Errorf("odd stereo chunk of %d samples", len(pcm))
		}
		if first == 0 {
			first = pts
		}
		last = pts
		samples += len(pcm)
	}); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	frames := samples / Channels
	if frames < SampleRate || frames > 2*SampleRate+SampleRate/2 {
		t.Errorf("captured %d frames in 2 s", frames)
	}
	if last <= first {
		t.Errorf("pts did not advance: %d to %d", first, last)
	}
}

func TestLiveLinuxVirtualMicRoundTrip(t *testing.T) {
	requireLiveLinux(t)
	dev, err := FindVirtualMic("")
	if err != nil {
		t.Fatal(err)
	}
	sink, err := OpenMicSink(dev)
	if err != nil {
		t.Skipf("cannot make the virtual mic: %v", err)
	}
	defer sink.Close()
	rec, err := NewMicCapturerOn(sink.Device())
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var peak int16
	if err := rec.Start(ctx, func(pcm []int16, _ int64) {
		mu.Lock()
		defer mu.Unlock()
		for _, v := range pcm {
			if v > peak {
				peak = v
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, FrameSamples(DefaultFrameDuration))
	tick := time.NewTicker(DefaultFrameDuration)
	defer tick.Stop()
	for n := 0; ctx.Err() == nil; n += len(frame) {
		select {
		case <-tick.C:
		case <-ctx.Done():
			continue
		}
		for i := range frame {
			frame[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(n+i)/SampleRate))
		}
		if err := sink.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if peak < 4000 {
		t.Errorf("recorded peak %d from the virtual microphone, want the 440 Hz tone", peak)
	}
}
