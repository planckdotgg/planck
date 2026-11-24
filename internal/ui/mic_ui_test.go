package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
)

const steamMic = "Microphone (Steam Streaming Microphone)"

func waitMuted(t *testing.T, f *fakeBackend, tt *mui.Tester, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.Telemetry().MicMuted != want {
		if time.Now().After(deadline) {
			t.Fatalf("mic muted = %v, want %v", !want, want)
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
}

// TestOverlayMicAndClipboard: the Sound panel's Microphone switch and
// Ctrl+Alt+U drive the host's virtual mic, and Shared clipboard turns both
// directions on and off together.
func TestOverlayMicAndClipboard(t *testing.T) {
	f := streamFake()
	f.tel.MicMuted, f.tel.MicDevice = true, steamMic
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	if !tt.HasText("80 % · mic off") {
		t.Fatalf("sound row value: %q", tt.Texts())
	}
	click(t, tt, "Sound & mic")
	if !tt.HasText("Off · plays into " + steamMic + " on the host") {
		t.Fatalf("mic hint missing: %q", tt.Texts())
	}
	click(t, tt, "Microphone")
	waitMuted(t, f, tt, false)
	if !tt.HasText("Live in "+steamMic+" on the host") || !tt.HasText("80 % · mic on") {
		t.Fatalf("live mic not shown: %q", tt.Texts())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyU)
	waitMuted(t, f, tt, true)

	click(t, tt, "Shared clipboard")
	if s := f.Settings(); !s.ClipboardSend || !s.ClipboardReceive {
		t.Fatalf("clipboard on saved %+v", s)
	}
	click(t, tt, "Shared clipboard")
	if s := f.Settings(); s.ClipboardSend || s.ClipboardReceive {
		t.Fatalf("clipboard off saved %+v", s)
	}
	f.set(func(f *fakeBackend) { f.settings.ClipboardReceive = true })
	tt.Frame()
	if !tt.HasText("receive only") {
		t.Fatalf("one-way clipboard not shown: %q", tt.Texts())
	}
	click(t, tt, "Shared clipboard")
	if s := f.Settings(); !s.ClipboardSend || !s.ClipboardReceive {
		t.Fatalf("one-way clipboard switched to %+v, want both", s)
	}
}

// TestOverlayMicWithoutVirtualMic: with no virtual mic on the host the mic
// stays muted and the panel says why.
func TestOverlayMicWithoutVirtualMic(t *testing.T) {
	f := streamFake()
	f.tel.MicMuted, f.tel.MicHint = true, "Virtual mic not detected — install Steam Streaming Microphone / virtual audio device"
	_, tt, _ := newStreamTester(t, f)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyU)
	openMenu(t, tt)
	click(t, tt, "Sound & mic")
	click(t, tt, "Microphone")
	time.Sleep(20 * time.Millisecond)
	tt.Frame()
	if !f.Telemetry().MicMuted || !tt.HasText(f.tel.MicHint) {
		t.Fatalf("muted %v, texts %q", f.Telemetry().MicMuted, tt.Texts())
	}
}
