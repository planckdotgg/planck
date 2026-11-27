package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

func calHosts() []HostInfo {
	return []HostInfo{{ID: "h-bigboi", Name: "bigboi", AdvertisedName: "bigboi", OS: "Windows 11", GPU: "RTX 4080",
		PathKind: discovery.PathLANDirect, RTTMs: 0.2, Online: true, Paired: true, LANReachable: true,
		Height: 1080, RefreshHz: 60, Address: "192.168.188.20:47700"}}
}

func (f *fakeBackend) setCal(fn func(s *CalibrationState)) {
	defer f.lock()()
	fn(&f.cal.state)
}

func calLinkFixture() *CalibrationLink {
	return &CalibrationLink{PathKind: discovery.PathLANDirect, RTT: StageStat{P50: 0.4, P99: 1.1}, JitterMs: 0.2,
		BandwidthMbps: 940, LossPct: 0.1, DatagramBytes: 1152, QueueDatagrams: 256, DrainMbps: 900}
}

func calCandidatesFixture() []CalibrationCandidate {
	return []CalibrationCandidate{
		{Name: "HEVC 4:2:0 1 slice", Codec: encode.HEVC, BitDepth: 8, Encode: StageStat{P50: 1.4}, Network: StageStat{P50: 0.6},
			Decode: StageStat{P50: 1.9}, Total: StageStat{P50: 4.8}, BitrateMbps: 120, BadPct: 0.4, Score: 5.1},
		{Name: "HEVC 4:2:0 4 slices", Codec: encode.HEVC, BitDepth: 10, Encode: StageStat{P50: 1.1}, Network: StageStat{P50: 0.5},
			Decode: StageStat{P50: 1.3}, Total: StageStat{P50: 3.4}, BitrateMbps: 160, BadPct: 0.2, Score: 3.6, Best: true},
		{Name: "AV1 4:4:4", Codec: encode.AV1, Err: "the host has no AV1 encoder"},
	}
}

func calDisplayFixture() *CalibrationDisplay {
	return &CalibrationDisplay{Status: CalibrationDisplayMeasured, Variants: []CalibrationDisplayVariant{
		{Name: "vsync off · phase lock off", Glass: StageStat{P50: 7.9}, Total: StageStat{P50: 12.1}, ScanOut: "composited", CompositedPct: 100, RefreshHz: 60},
		{Name: "vsync off · phase lock on", Glass: StageStat{P50: 3.1}, Total: StageStat{P50: 6.5}, ScanOut: "direct", RefreshHz: 60, Locked: true, Best: true},
	}}
}

func calResultFixture() *CalibrationResult {
	return &CalibrationResult{HostID: "h-bigboi", HostName: "bigboi", At: time.Date(2026, 11, 26, 17, 2, 0, 0, time.UTC),
		Link: *calLinkFixture(), Candidates: calCandidatesFixture(), Display: *calDisplayFixture(),
		Recommended: defaults.UserOverrides{Codec: "hevc", BitDepth: "10", SubFrameSlices: "4", BitrateMbps: 160},
		BeforeMs:    12.4, AfterMs: 6.5,
		Warnings: []string{"The stream window is composited: switch to full screen to scan out directly."}}
}

func calKept() CalibrationSummary {
	return CalibrationSummary{PathKind: discovery.PathLANDirect, At: time.Date(2026, 11, 26, 17, 2, 0, 0, time.UTC),
		GlassMs: 6.5, RTTMs: 0.4, BandwidthMbps: 940, QueueBytes: 295000, DrainMbps: 900,
		Overrides: defaults.UserOverrides{SubFrameSlices: "4"}}
}

func startFromCard(t *testing.T, f *fakeBackend) (*App, *mui.Tester) {
	t.Helper()
	f.hosts = calHosts()
	a, tt := newTester(t, f)
	click(t, tt, "Auto-tune bigboi")
	return a, tt
}

func TestCalibrateOpensFromHostCard(t *testing.T) {
	f := newFake()
	a, tt := startFromCard(t, f)
	if len(f.cal.starts) != 1 || f.cal.starts[0] != "h-bigboi" {
		t.Fatalf("starts = %q", f.cal.starts)
	}
	if !a.cal.open || !tt.HasText("Auto-tune") || !tt.HasText("Measuring") || !tt.HasText("Network") {
		t.Fatalf("window not open: %q", tt.Texts())
	}
	if !tt.HasText("Cancel") {
		t.Fatal("no Cancel while running")
	}
	snapshot(t, tt, "calibrate-starting")
}

func TestCalibrateOpensFromSettings(t *testing.T) {
	f := newFake()
	f.hosts = calHosts()
	f.cal.status = map[string][]CalibrationSummary{"h-bigboi": {calKept()}}
	_, tt := newTester(t, f)
	click(t, tt, "Stream settings for bigboi")
	for _, s := range []string{"Auto-tune", "Reset calibration", "tuned Nov 26", "6.5 ms to glass", "round trip 0.4 ms", "940 Mbps"} {
		if !tt.HasText(s) {
			t.Errorf("settings panel missing %q: %q", s, tt.Texts())
		}
	}
	snapshot(t, tt, "calibrate-settings-tuned")
	clickScrolled(t, tt, "Auto-tune")
	if len(f.cal.starts) != 1 || f.cal.starts[0] != "h-bigboi" {
		t.Fatalf("starts = %q", f.cal.starts)
	}
	click(t, tt, "Cancel")
	if len(f.cal.cancels) != 1 {
		t.Fatalf("cancels = %d", len(f.cal.cancels))
	}
}

func TestCalibrateSettingsResetAndAllHosts(t *testing.T) {
	f := newFake()
	f.hosts = calHosts()
	f.cal.status = map[string][]CalibrationSummary{"h-bigboi": {calKept()}}
	_, tt := newTester(t, f)
	click(t, tt, "Stream settings for bigboi")
	clickScrolled(t, tt, "Reset calibration")
	if len(f.cal.resets) != 1 || len(f.cal.resets[0]) != 0 {
		t.Fatalf("resets = %v", f.cal.resets)
	}
	tt.Frame()
	if tt.HasText("Reset calibration") || !tt.HasText("bigboi is not tuned. Auto-tune measures the route and picks the codec, slices, vsync and bitrate for it.") {
		t.Fatalf("panel after reset: %q", tt.Texts())
	}
	click(t, tt, "All hosts")
	if !tt.HasText("Choose a host under “Stream format for” to auto-tune it. Calibration is kept per host and route.") {
		t.Fatalf("all-hosts scope: %q", tt.Texts())
	}
}

func TestCalibrateHostCardStatusAndStreaming(t *testing.T) {
	f := newFake()
	f.hosts = calHosts()
	f.cal.status = map[string][]CalibrationSummary{"h-bigboi": {calKept()}}
	_, tt := newTester(t, f)
	if !tt.HasText("Tuned · 6.5 ms · LAN") || !tt.HasText("Re-tune") {
		t.Fatalf("tuned card: %q", tt.Texts())
	}
	snapshot(t, tt, "calibrate-card-tuned")

	f.mu.Lock()
	f.hosts[0].Connected = true
	f.mu.Unlock()
	tt.Frame()
	info, ok := tt.Access("Auto-tune bigboi, end the stream first")
	if !ok || !info.Disabled {
		t.Fatalf("streaming card: access %+v ok %v, texts %q", info, ok, tt.Texts())
	}
	snapshot(t, tt, "calibrate-card-streaming")
}

func TestCalibrateRefusedWhileStreaming(t *testing.T) {
	f := newFake()
	f.cal.startErr = ErrStreaming
	_, tt := startFromCard(t, f)
	if !tt.HasText("Could not start") || !tt.HasText(ErrStreaming.Error()) || !tt.HasText("Failed") {
		t.Fatalf("refusal not shown: %q", tt.Texts())
	}
	f.cal.startErr = nil
	click(t, tt, "Re-run")
	if len(f.cal.starts) != 1 || !tt.HasText("Measuring") {
		t.Fatalf("re-run after refusal: starts %q texts %q", f.cal.starts, tt.Texts())
	}
	snapshot(t, tt, "calibrate-refused")
}

func TestCalibrateJoinsRunningCalibration(t *testing.T) {
	f := newFake()
	f.setCal(func(s *CalibrationState) {
		*s = CalibrationState{Running: true, HostID: "h-bigboi", Phase: CalibratingCodec, Percent: 40}
	})
	f.cal.startErr = ErrCalibrating
	_, tt := startFromCard(t, f)
	if tt.HasText("Could not start") || !tt.HasText("Measuring") {
		t.Fatalf("a running calibration of this host was refused: %q", tt.Texts())
	}
}

func TestCalibrateLivePhases(t *testing.T) {
	f := newFake()
	_, tt := startFromCard(t, f)

	f.setCal(func(s *CalibrationState) {
		s.Phase, s.Percent, s.Step = CalibratingNetwork, 12, "burst of 256 datagrams"
		s.Link = calLinkFixture()
	})
	tt.Frame()
	for _, s := range []string{"burst of 256 datagrams", "12%", "LAN direct", "0.4 ms · p99 1.1", "940 Mbps", "0.1%", "256 dgrams · 900 Mbps", "Receive queue"} {
		if !tt.HasText(s) {
			t.Errorf("network phase missing %q: %q", s, tt.Texts())
		}
	}
	if info, ok := tt.Access("Phase Network, active"); !ok || info.Name == "" {
		t.Error("network phase not the active step")
	}
	if _, ok := tt.Access("Phase Codec, pending"); !ok {
		t.Error("codec phase not pending")
	}
	snapshot(t, tt, "calibrate-running-network")

	f.setCal(func(s *CalibrationState) {
		s.Phase, s.Percent, s.Step = CalibratingCodec, 55, "NVENC HEVC 4 slices"
		s.Candidates = calCandidatesFixture()[:2]
	})
	tt.Frame()
	for _, s := range []string{"NVENC HEVC 4 slices", "55%", "CODEC CANDIDATES", "HEVC 4:2:0 4 slices", "HEVC 10-bit", "3.4", "BEST"} {
		if !tt.HasText(s) {
			t.Errorf("codec phase missing %q: %q", s, tt.Texts())
		}
	}
	if _, ok := tt.Access("Phase Network, done"); !ok {
		t.Error("network phase not done")
	}
	snapshot(t, tt, "calibrate-running-codec")

	f.setCal(func(s *CalibrationState) {
		s.Phase, s.Percent, s.Step = CalibratingDisplay, 85, "vsync off, phase lock on"
		s.Candidates = calCandidatesFixture()
		s.Display = calDisplayFixture()
	})
	tt.Frame()
	for _, s := range []string{"85%", "DISPLAY", "direct", "composited 100%", "locked", "60 Hz", "the host has no AV1 encoder"} {
		if !tt.HasText(s) {
			t.Errorf("display phase missing %q: %q", s, tt.Texts())
		}
	}
	snapshot(t, tt, "calibrate-running-display")

	click(t, tt, "Cancel")
	if len(f.cal.cancels) != 1 {
		t.Fatalf("cancels = %d", len(f.cal.cancels))
	}
	tt.Frame()
	if !tt.HasText("Cancelled") || !tt.HasText("Re-run") || tt.HasText("Apply") {
		t.Fatalf("cancelled state: %q", tt.Texts())
	}
	snapshot(t, tt, "calibrate-cancelled")
	click(t, tt, "Close")
	if tt.HasText("Re-run") {
		t.Fatal("window still open")
	}
}

func finishCal(f *fakeBackend) {
	f.setCal(func(s *CalibrationState) {
		s.Running, s.Phase, s.Percent, s.Result = false, CalibrationFinished, 100, calResultFixture()
	})
}

func TestCalibrateFinishedAndApply(t *testing.T) {
	f := newFake()
	_, tt := startFromCard(t, f)
	finishCal(f)
	tt.Frame()
	for _, s := range []string{"Finished", "12.4", "6.5", "5.9 ms faster · 48% lower", "BEFORE · DEFAULTS", "AFTER · AUTO-TUNED",
		"Codec HEVC", "Bit depth 10-bit", "Sub-frame slices 4", "Bitrate 160 Mbps",
		"The stream window is composited: switch to full screen to scan out directly.",
		"HEVC 4:2:0 4 slices", "direct", "composited 100%", "locked", "Apply", "Re-run", "Close"} {
		if !tt.HasText(s) {
			t.Errorf("finished missing %q: %q", s, tt.Texts())
		}
	}
	if _, ok := tt.Access("Warning: The stream window is composited: switch to full screen to scan out directly."); !ok {
		t.Error("warning callout not exposed")
	}
	snapshot(t, tt, "calibrate-finished")

	click(t, tt, "Apply")
	if len(f.cal.applied) != 1 || f.cal.applied[0] != "h-bigboi" {
		t.Fatalf("applied = %q", f.cal.applied)
	}
	if !tt.HasText("bigboi streams with these settings on this route wherever the stream format is Auto.") || !tt.HasText("Applied") {
		t.Fatalf("apply not confirmed: %q", tt.Texts())
	}
	snapshot(t, tt, "calibrate-applied")
	click(t, tt, "Close")
	if len(f.cal.applied) != 1 {
		t.Fatal("applied twice")
	}
}

func TestCalibrateApplyError(t *testing.T) {
	f := newFake()
	a, tt := startFromCard(t, f)
	finishCal(f)
	tt.Frame()
	f.setCal(func(s *CalibrationState) { s.Result = nil })
	a.applyCalibration()
	tt.Frame()
	if !tt.HasText(ErrNoCalibration.Error()) || a.cal.applied {
		t.Fatalf("apply error not shown: %q", tt.Texts())
	}
}

func TestCalibrateRerunAndReset(t *testing.T) {
	f := newFake()
	f.cal.status = map[string][]CalibrationSummary{"h-bigboi": {calKept()}}
	_, tt := startFromCard(t, f)
	finishCal(f)
	tt.Frame()
	click(t, tt, "Re-run")
	if len(f.cal.starts) != 2 {
		t.Fatalf("starts = %q", f.cal.starts)
	}
	tt.Frame()
	if !tt.HasText("Measuring") || tt.HasText("Apply") {
		t.Fatalf("re-run state: %q", tt.Texts())
	}
	finishCal(f)
	tt.Frame()
	click(t, tt, "Reset calibration")
	if len(f.cal.resets) != 1 || len(f.cal.resets[0]) != 0 {
		t.Fatalf("resets = %v", f.cal.resets)
	}
	if !tt.HasText("bigboi streams with the defaults again.") || tt.HasText("Reset calibration") {
		t.Fatalf("reset not confirmed: %q", tt.Texts())
	}
	snapshot(t, tt, "calibrate-reset")
}

func TestCalibrateFailedAndSkippedDisplay(t *testing.T) {
	f := newFake()
	_, tt := startFromCard(t, f)
	f.setCal(func(s *CalibrationState) {
		s.Running, s.Phase, s.Err = false, CalibratingCodec, "the host closed the stream"
		s.Candidates = calCandidatesFixture()[:1]
	})
	tt.Frame()
	if !tt.HasText("Calibration failed") || !tt.HasText("the host closed the stream") || !tt.HasText("Failed") || tt.HasText("Apply") {
		t.Fatalf("failure: %q", tt.Texts())
	}
	snapshot(t, tt, "calibrate-failed")
	click(t, tt, "Re-run")
	if len(f.cal.starts) != 2 {
		t.Fatalf("starts = %q", f.cal.starts)
	}

	res := calResultFixture()
	res.Display = CalibrationDisplay{Status: CalibrationDisplayUnavailable, Note: "This window's present path does not say when pictures reach the screen."}
	res.Warnings = []string{"Calibrated over a DERP relay: expect a higher floor than on the LAN."}
	res.Link.PathKind = discovery.PathTailscaleDERP
	f.setCal(func(s *CalibrationState) { s.Running, s.Err, s.Phase, s.Result = false, "", CalibrationFinished, res })
	tt.Frame()
	for _, s := range []string{"DERP relay", "This window's present path does not say when pictures reach the screen.",
		"Calibrated over a DERP relay: expect a higher floor than on the LAN."} {
		if !tt.HasText(s) {
			t.Errorf("relay result missing %q: %q", s, tt.Texts())
		}
	}
	snapshot(t, tt, "calibrate-relay")
}

func TestCalibrateEscapeClosesOnlyWhenIdle(t *testing.T) {
	f := newFake()
	a, _ := startFromCard(t, f)
	a.closeCalibration()
	if !a.cal.open {
		t.Fatal("closed while running")
	}
	finishCal(f)
	a.closeCalibration()
	if a.cal.open {
		t.Fatal("did not close when finished")
	}
}
