package audio

import (
	"runtime"
	"testing"
)

func TestVirtualAudioStatus(t *testing.T) {
	steam := "Speakers (Steam Streaming Microphone)"
	planck := "Speakers (Planck Virtual Audio)"
	for _, tc := range []struct {
		name      string
		installed bool
		outputs   []string
		want      VirtualAudioStatus
	}{
		{"nothing", false, []string{"MacBook Pro Speakers"},
			VirtualAudioStatus{InstallHint: vdriverInstallHint}},
		{"fallback only", false, []string{"Speakers (Realtek(R) Audio)", steam},
			VirtualAudioStatus{DeviceName: steam, DriverName: "Steam Streaming Microphone", InstallHint: vdriverInstallHint}},
		{"planck before fallback", true, []string{steam, planck},
			VirtualAudioStatus{Installed: true, Active: true, DeviceName: planck, DriverName: PlanckVirtualAudio}},
		{"planck running, install not seen", false, []string{PlanckVirtualAudio},
			VirtualAudioStatus{Installed: true, Active: true, DeviceName: PlanckVirtualAudio, DriverName: PlanckVirtualAudio}},
		{"installed, not loaded", true, []string{"CABLE Input (VB-Audio Virtual Cable)"},
			VirtualAudioStatus{Installed: true, DeviceName: "CABLE Input (VB-Audio Virtual Cable)", DriverName: "CABLE Input", InstallHint: vdriverInactiveHint}},
	} {
		if got := virtualAudioStatus(tc.installed, tc.outputs); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestVirtualAudio reads this machine's devices: it must not fail where
// Planck has an audio backend, and its fields must agree.
func TestVirtualAudio(t *testing.T) {
	st, err := VirtualAudio()
	if err != nil {
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	if st.Active && (!st.Installed || st.InstallHint != "") || !st.Active && st.InstallHint == "" {
		t.Fatalf("inconsistent status %+v", st)
	}
	t.Logf("%+v", st)
}
