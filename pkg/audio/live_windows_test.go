package audio

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The control for TestLiveDesktopCaptureExcludesOwnProcess: default
// endpoint loopback, the old path, does hear this process.
func TestLiveEndpointLoopbackHearsOwnProcess(t *testing.T) {
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	playOwnTone(t, ctx, 1000)
	c := newEndpointLoopbackCapturer()
	var rec recorder
	if err := c.Start(ctx, rec.on); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	c.Close()
	pcm := rec.take()
	pcm = pcm[min(len(pcm), SampleRate/2*Channels):]
	lvl := toneLevel(pcm, Channels, 1000)
	t.Logf("mode %s: own 1 kHz tone level %.1f (played at %d)", c.Mode(), lvl, toneAmp)
	if lvl < toneAmp/4 {
		t.Fatalf("endpoint loopback did not hear this process (level %.1f); the exclusion test proves nothing on this machine", lvl)
	}
}

// TestLiveVirtualMicRoundTrip plays a tone into the virtual mic's playback
// side and records its input side, the device voice apps use.
func TestLiveVirtualMicRoundTrip(t *testing.T) {
	requireLive(t)
	vm, err := FindVirtualMic("")
	if err != nil {
		t.Skip(err)
	}
	inputs, err := inputDevices()
	if err != nil {
		t.Fatal(err)
	}
	// "Speakers (Steam Streaming Microphone)" feeds "Microphone (Steam
	// Streaming Microphone)": match the name in parentheses.
	key := vm.Name
	if i := strings.Index(key, "("); i >= 0 {
		key = strings.TrimSuffix(key[i+1:], ")")
	}
	var input VirtualMic
	for _, d := range inputs {
		if strings.Contains(d.Name, key) {
			input = d
		}
	}
	if input.ID == "" {
		t.Skipf("no input device matches %q", key)
	}
	t.Logf("writing to %q, recording %q", vm.Name, input.Name)
	sink, err := OpenMicSink(vm)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	mic := newMicCapturerOn(input.ID)
	var rec recorder
	if err := mic.Start(ctx, rec.on); err != nil {
		t.Fatal(err)
	}
	// What the sink renders, heard on its own endpoint: tells a sink that
	// writes nothing from a driver that passes nothing on.
	lb := newLoopbackOn(vm.ID)
	var rendered recorder
	if err := lb.Start(ctx, rendered.on); err != nil {
		t.Fatal(err)
	}
	defer func() {
		lb.Close()
		out := rendered.take()
		t.Logf("sink endpoint loopback: 440 Hz level %.1f", toneLevel(out[min(len(out), SampleRate/2*Channels):], Channels, 440))
	}()
	frame := make([]int16, FrameSamples(DefaultFrameDuration)*MicChannels)
	tick := time.NewTicker(DefaultFrameDuration)
	for n := 0; ctx.Err() == nil; n += len(frame) {
		<-tick.C
		fillTone(frame, MicChannels, 440, n)
		if err := sink.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	tick.Stop()
	mic.Close()
	pcm := rec.take()
	pcm = pcm[min(len(pcm), SampleRate/2):]
	lvl := toneLevel(pcm, MicChannels, 440)
	t.Logf("recorded %.2f s from the virtual mic; 440 Hz level %.1f (written at %d)", float64(len(pcm))/SampleRate, lvl, toneAmp)
	if lvl < toneAmp/4 {
		t.Fatalf("the tone written to %q did not come out of %q", vm.Name, input.Name)
	}
}
