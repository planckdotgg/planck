package ui

import "testing"

// TestVirtualAudioStatusLine: under Virtual mic sink, Settings says whether
// Planck Virtual Audio runs, what Auto falls back to and how to install
// it, and nothing when the microphone is off.
func TestVirtualAudioStatusLine(t *testing.T) {
	const hint = "Install Planck Virtual Audio (drivers/audio/windows) so apps here hear a client's microphone."
	f := newFake()
	f.audioDevices = sampleAudioDevices()
	f.virtualAudio = VirtualAudioStatus{Installed: true, Active: true, DeviceName: "Speakers (Planck Virtual Audio)", DriverName: "Planck Virtual Audio"}
	_, tt := settingsOn(t, f, catAudio, "windows")
	if !tt.HasText("Planck Virtual Audio is running.") || tt.HasText(hint) {
		t.Fatalf("running driver: %q", tt.Texts())
	}

	f.set(func(f *fakeBackend) {
		f.virtualAudio = VirtualAudioStatus{DeviceName: "Speakers (Steam Streaming Microphone)", DriverName: "Steam Streaming Microphone", InstallHint: hint}
	})
	tt.Frame()
	if !tt.HasText("Auto plays into Speakers (Steam Streaming Microphone).") || !tt.HasText(hint) {
		t.Fatalf("fallback: %q", tt.Texts())
	}
	snapshot(t, tt, "settings-audio-vdriver")

	f.set(func(f *fakeBackend) { f.virtualAudio = VirtualAudioStatus{InstallHint: hint} })
	tt.Frame()
	if !tt.HasText("No virtual mic found.") || !tt.HasText(hint) {
		t.Fatalf("no virtual mic: %q", tt.Texts())
	}

	choose(t, tt, "Virtual mic sink", "Off")
	if _, ok := tt.Access("Virtual audio status"); ok || tt.HasText(hint) {
		t.Fatalf("status shown with the microphone off: %q", tt.Texts())
	}
}
