package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
)

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
}

func TestDrawerMicAndClipboard(t *testing.T) {
	f := newFake()
	f.tel = sampleTelemetry()
	f.tel.MicMuted, f.tel.MicDevice = true, "Microphone (Steam Streaming Microphone)"
	_, tt := newTester(t, f)
	click(t, tt, "Show stream controls")
	if !tt.HasText("Muted. Plays into Microphone (Steam Streaming Microphone) on the host when on.") {
		t.Fatalf("mic hint missing: %q", tt.Texts())
	}
	click(t, tt, "Unmute")
	waitMuted(t, f, tt, false)
	tt.Frame()
	if !tt.HasText("Live in Microphone (Steam Streaming Microphone) on the host.") {
		t.Fatalf("live mic hint missing: %q", tt.Texts())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	waitMuted(t, f, tt, true)

	click(t, tt, "Share clipboard")
	click(t, tt, "Accept clipboard")
	if s := f.Settings(); !s.ClipboardSend || !s.ClipboardReceive {
		t.Fatalf("clipboard switches saved %+v", s)
	}
}

func TestMicHotkeyWithoutVirtualMic(t *testing.T) {
	f := newFake()
	f.tel = sampleTelemetry()
	f.tel.MicMuted, f.tel.MicHint = true, "Virtual mic not detected — install Steam Streaming Microphone / virtual audio device"
	_, tt := newTester(t, f)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	tt.Frame()
	click(t, tt, "Show stream controls")
	if !f.Telemetry().MicMuted || !tt.HasText(f.tel.MicHint) {
		t.Fatalf("muted %v, texts %q", f.Telemetry().MicMuted, tt.Texts())
	}
}
