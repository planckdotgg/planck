package ui

import "testing"

// TestSettingsTelemetrySwitch: Settings › General shows the telemetry
// switch with what it sends, and each flip saves the setting at once.
func TestSettingsTelemetrySwitch(t *testing.T) {
	f := newFake()
	f.settings.UsageTelemetry = true
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	tt.Frame()
	if !tt.HasText(telemetryHelp) {
		t.Fatalf("General has no telemetry disclosure; texts %q", tt.Texts())
	}
	clickScrolled(t, tt, telemetryTitle)
	if f.Settings().UsageTelemetry {
		t.Fatal("switch did not turn telemetry off")
	}
	f.mu.Lock()
	saved := len(f.saved)
	f.mu.Unlock()
	if saved == 0 {
		t.Fatal("turning telemetry off saved nothing")
	}
	clickScrolled(t, tt, telemetryTitle)
	if !f.Settings().UsageTelemetry {
		t.Fatal("switch did not turn telemetry back on")
	}
}
