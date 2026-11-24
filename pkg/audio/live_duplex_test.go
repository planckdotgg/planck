//go:build darwin || windows

package audio

import (
	"context"
	"errors"
	"math"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// toneAmp is about -40 dBFS: quiet in a room, plain to a detector.
const toneAmp = 330

func fillTone(dst []int16, channels int, hz float64, start int) {
	for i := 0; i < len(dst)/channels; i++ {
		v := int16(toneAmp * math.Sin(2*math.Pi*hz*float64(start+i)/SampleRate))
		for c := 0; c < channels; c++ {
			dst[i*channels+c] = v
		}
	}
}

// toneLevel is the amplitude of hz in the first channel of pcm (Goertzel).
func toneLevel(pcm []int16, channels int, hz float64) float64 {
	n := len(pcm) / channels
	if n == 0 {
		return 0
	}
	k := 2 * math.Cos(2*math.Pi*hz/SampleRate)
	var s1, s2 float64
	for i := 0; i < n; i++ {
		s := float64(pcm[i*channels]) + k*s1 - s2
		s2, s1 = s1, s
	}
	power := s1*s1 + s2*s2 - k*s1*s2
	return 2 * math.Sqrt(math.Max(power, 0)) / float64(n)
}

// recorder keeps what a capturer delivers.
type recorder struct {
	mu  sync.Mutex
	pcm []int16
}

func (r *recorder) on(pcm []int16, _ int64) {
	r.mu.Lock()
	r.pcm = append(r.pcm, pcm...)
	r.mu.Unlock()
}

func (r *recorder) take() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int16(nil), r.pcm...)
}

// playOwnTone plays hz from this process until ctx ends.
func playOwnTone(t *testing.T, ctx context.Context, hz float64) {
	p, err := NewPlayer(PlayerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer p.Close()
		frame := make([]int16, FrameSamples(DefaultFrameDuration)*Channels)
		tick := time.NewTicker(DefaultFrameDuration)
		defer tick.Stop()
		for n := 0; ctx.Err() == nil; n += len(frame) / Channels {
			<-tick.C
			fillTone(frame, Channels, hz, n)
			_ = p.Write(frame)
		}
	}()
}

// moder is a capturer that says which path it opened.
type moder interface {
	Capturer
	Mode() string
}

// TestLiveDesktopCaptureExcludesOwnProcess plays a quiet 1 kHz tone from
// this process while recording the desktop. Process-scoped capture must
// not hear it. With PLANCK_LIVE_OTHER_TONE_HZ set, a tone at that
// frequency played by another process (started outside the test on
// Windows, where the excluded tree includes children; afplay on macOS) must
// be heard.
func TestLiveDesktopCaptureExcludesOwnProcess(t *testing.T) {
	requireLive(t)
	other, _ := strconv.ParseFloat(os.Getenv("PLANCK_LIVE_OTHER_TONE_HZ"), 64)
	stopOther := startOtherTone(t, other)
	defer stopOther()
	c, err := NewCapturer(CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	playOwnTone(t, ctx, 1000)
	var rec recorder
	if err := c.Start(ctx, rec.on); err != nil {
		t.Fatal(err)
	}
	var capt Capturer = c
	mode := capt.(moder).Mode()
	if te, ok := capt.(interface{ TapError() error }); ok && te.TapError() != nil {
		t.Logf("process tap not used: %v", te.TapError())
	}
	<-ctx.Done()
	c.Close()
	pcm := rec.take()
	skip := min(len(pcm), SampleRate/2*Channels) // let the tone start
	pcm = pcm[skip:]
	own := toneLevel(pcm, Channels, 1000)
	t.Logf("mode %s: %.2f s captured, own 1 kHz tone level %.1f (played at %d)", mode, float64(len(pcm)/Channels)/SampleRate, own, toneAmp)
	if mode != ModeProcessLoopback && mode != ModeProcessTap && mode != ModeScreenCaptureKit {
		t.Fatalf("capture fell back to %s, which hears this process", mode)
	}
	if len(pcm) < SampleRate*Channels {
		t.Fatalf("captured only %d samples", len(pcm))
	}
	if own > toneAmp/10 {
		t.Errorf("own tone came back at %.1f: the capture does not exclude this process", own)
	}
	if other > 0 {
		lvl := toneLevel(pcm, Channels, other)
		t.Logf("other process's %.0f Hz tone level %.1f", other, lvl)
		if lvl < toneAmp/4 {
			t.Errorf("another process's %.0f Hz tone is missing (level %.1f): the capture hears nothing", other, lvl)
		}
	}
}

func TestLiveMicCapture(t *testing.T) {
	requireLive(t)
	m, err := NewMicCapturer()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var rec recorder
	if err := m.Start(ctx, rec.on); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	m.Close()
	pcm := rec.take()
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(max(len(pcm), 1)))
	t.Logf("mic: %.2f s of mono, RMS %.1f (0 can mean the Microphone permission is denied)", float64(len(pcm))/SampleRate, rms)
	if len(pcm) < SampleRate {
		t.Fatalf("captured %d mono samples in 1.5 s", len(pcm))
	}
}

func TestLiveFindVirtualMic(t *testing.T) {
	requireLive(t)
	devs, err := outputDevices()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		t.Logf("output device %q", d.Name)
	}
	vm, err := FindVirtualMic(os.Getenv("PLANCK_LIVE_MIC_SINK"))
	if errors.Is(err, ErrNoVirtualMic) {
		t.Logf("no virtual mic: %v", err)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("virtual mic: %q", vm.Name)
	s, err := OpenMicSink(vm)
	if err != nil {
		t.Fatal(err)
	}
	silence := make([]int16, FrameSamples(DefaultFrameDuration)*MicChannels)
	for range 50 {
		if err := s.Write(silence); err != nil {
			t.Fatal(err)
		}
		time.Sleep(DefaultFrameDuration)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(silence); err != ErrClosed {
		t.Fatalf("write after close: %v", err)
	}
}
