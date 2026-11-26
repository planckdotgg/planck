package ui

import (
	"errors"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func settingsTester(t *testing.T, f *fakeBackend) (*App, *mui.Tester) {
	t.Helper()
	a, tt := newTester(t, f)
	a.screen, a.set.cat = ScreenSettings, catVideo
	tt.Frame()
	return a, tt
}

// choose opens the dropdown named name and picks option.
func choose(t *testing.T, tt *mui.Tester, name, option string) {
	t.Helper()
	info, ok := tt.AccessByRole(mui.RolePopUpButton, name)
	if !ok {
		t.Fatalf("no %s dropdown: %q", name, tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	click(t, tt, option)
}

// TestOverrideKnobsAllHosts: every knob in Settings starts on Auto and
// saves its choice to the overrides for every host.
func TestOverrideKnobsAllHosts(t *testing.T) {
	f := newFake()
	_, tt := settingsTester(t, f)
	for _, k := range []knob{knobCodec, knobChroma, knobBitDepth, knobFPS, knobVDD, knobSlices, knobSplit, knobDirect, knobVSync} {
		if info, ok := tt.Access(k.title + ": Auto"); !ok || !info.Checked {
			t.Errorf("%s: Auto not chosen at first (%+v, %v)", k.title, info, ok)
		}
	}
	snapshot(t, tt, "settings-overrides")
	choose(t, tt, "Resolution", "2560×1440")
	for _, name := range []string{"Codec: PyroWave", "Chroma: 4:4:4", "Bit depth: 10-bit", "Frame rate: 144",
		"Bitrate: Manual", "Virtual display: On", "Sub-frame slices: 4", "NVENC two-engine split: Off",
		"Direct to display: On", "VSync: On"} {
		clickScrolled(t, tt, name)
	}
	click(t, tt, "Network")
	if info, ok := tt.Access("Route: Auto"); !ok || !info.Checked {
		t.Errorf("Route: Auto not chosen at first (%+v, %v)", info, ok)
	}
	click(t, tt, "Route: LAN")
	click(t, tt, "Video & Display")
	want := defaults.UserOverrides{Codec: "pyrowave", Chroma: "444", BitDepth: "10", Resolution: "1440p", FPS: "144",
		BitrateMbps: manualBitrateMbps, VirtualDisplay: "on", SubFrameSlices: "4", NVENC2EngineSplit: "off",
		MetalDirectDisplay: "on", VSyncMode: "on", RoutePolicy: "lan"}.Normalize()
	if got := f.override(""); got != want {
		t.Fatalf("overrides\n got %+v\nwant %+v", got, want)
	}
	if len(f.overrides) != 1 {
		t.Fatalf("saved for hosts too: %v", f.overrides)
	}
	if info, ok := tt.Access("Codec: PyroWave"); !ok || !info.Checked {
		t.Fatalf("PyroWave not shown chosen after the save")
	}
	clickScrolled(t, tt, "Bitrate: Auto")
	if got := f.override(""); got.BitrateMbps != 0 {
		t.Fatalf("bitrate back to auto: %d", got.BitrateMbps)
	}
}

// TestOverrideCustomSizeAndGPU: Custom starts at 1080p and takes typed
// even sizes in range on Enter, keeping the last good one and saying why
// otherwise; the GPU field saves its text.
func TestOverrideCustomSizeAndGPU(t *testing.T) {
	f := newFake()
	_, tt := settingsTester(t, f)
	choose(t, tt, "Resolution", "Custom…")
	if got := f.override(""); got.Resolution != "custom" || got.CustomWidth != customWidth || got.CustomHeight != customHeight {
		t.Fatalf("custom: %+v", got)
	}
	typeInto(t, tt, "Custom width", "2560")
	if got := f.override(""); got.CustomWidth != 2560 || got.CustomHeight != customHeight {
		t.Fatalf("custom width: %+v", got)
	}
	typeInto(t, tt, "Custom height", "1441")
	if got := f.override(""); got.CustomHeight != customHeight {
		t.Fatalf("odd height saved: %+v", got)
	}
	if !tt.HasText(`"1441" is not an even size`) {
		t.Fatalf("no reason for the refused size: %q", tt.Texts())
	}
	typeInto(t, tt, "Virtual display GPU", "4080")
	if got := f.override(""); got.VirtualDisplayGPU != "4080" || got.CustomWidth != 2560 {
		t.Fatalf("gpu: %+v", got)
	}
	clickScrolled(t, tt, "Virtual display: Off")
	if tt.HasText("Virtual display GPU") {
		t.Fatal("GPU row shown with the virtual display off")
	}
}

// typeInto replaces a text field's text and presses Enter.
func typeInto(t *testing.T, tt *mui.Tester, field, text string) {
	t.Helper()
	info, ok := tt.AccessByRole(mui.RoleTextField, field)
	if !ok {
		t.Fatalf("no %s field: %q", field, tt.Texts())
	}
	scrollTo(t, tt, field)
	info, _ = tt.AccessByRole(mui.RoleTextField, field)
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	for range len(info.Value) + 2 {
		tt.Key(0, mui.KeyBackspace)
	}
	tt.Type(text)
	tt.Key(0, mui.KeyEnter)
	tt.Frame()
}

// TestOverrideHostScope: a host's scope saves under its ID only, says
// what its auto knobs follow, and Clear overrides clears that host alone.
func TestOverrideHostScope(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	f.overrides[""] = defaults.UserOverrides{FPS: "120", BitrateMbps: 300}
	_, tt := settingsTester(t, f)
	if tt.HasText("Stream format for: cabin-pc") {
		t.Fatal("an unpaired host has a scope")
	}
	click(t, tt, "Stream format for: bigboi")
	if !tt.HasText("Auto follows all hosts: 120") || !tt.HasText("Auto follows all hosts: 300 Mbps") {
		t.Fatalf("inherited values not shown: %q", tt.Texts())
	}
	click(t, tt, "Codec: AV1")
	click(t, tt, "Frame rate: 240")
	if got := f.override("h-bigboi"); got.Codec != "av1" || got.FPS != "240" || got.Chroma != defaults.Auto {
		t.Fatalf("bigboi: %+v", got)
	}
	if got := f.override(""); got.Codec != defaults.Auto || got.FPS != "120" {
		t.Fatalf("all hosts changed: %+v", got)
	}
	click(t, tt, "Clear overrides")
	if got := f.override("h-bigboi"); got != (defaults.UserOverrides{}).Normalize() {
		t.Fatalf("bigboi after reset: %+v", got)
	}
	if got := f.override(""); got.FPS != "120" || got.BitrateMbps != 300 {
		t.Fatalf("reset touched all hosts: %+v", got)
	}
}

func TestOverrideHostCardOpensScope(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, tt := newTester(t, f)
	if _, ok := tt.Access("Stream settings for cabin-pc"); ok {
		t.Fatal("an unpaired host's card offers stream settings")
	}
	click(t, tt, "Stream settings for studio-mac")
	if a.screen != ScreenSettings {
		t.Fatalf("screen %v", a.screen)
	}
	if info, ok := tt.Access("Stream format for: studio-mac"); !ok || !info.Checked {
		t.Fatalf("studio-mac's scope not chosen: %q", tt.Texts())
	}
	click(t, tt, "Chroma: 4:2:0")
	if got := f.override("h-studio"); got.Chroma != "420" {
		t.Fatalf("studio-mac: %+v", got)
	}
}

func TestOverrideSaveError(t *testing.T) {
	f := newFake()
	f.ovErr = errors.New("disk full")
	_, tt := settingsTester(t, f)
	click(t, tt, "Codec: HEVC")
	if !tt.HasText("Could not save settings: disk full") {
		t.Fatalf("save error missing: %q", tt.Texts())
	}
}

func sampleProfile() *defaults.ResolvedProfile {
	return &defaults.ResolvedProfile{Codec: defaults.CodecHEVC, BitDepth: 10, Width: 1920, Height: 1080, FPS: 120,
		VirtualDisplay: true, DirectDisplay: true, EncodeAdapter: gpu.Adapter{Name: "NVIDIA GeForce RTX 4080"},
		Warnings: []string{"fps 240: clamped to 120 Hz"}}
}

// TestClearOverridesOnlyWhenSet: the button shows only while the scope
// has an override, and clicking it hides it again.
func TestClearOverridesOnlyWhenSet(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	_, tt := settingsTester(t, f)
	if tt.HasText("Clear overrides") {
		t.Fatalf("all hosts with no overrides: %q", tt.Texts())
	}
	click(t, tt, "Codec: AV1")
	if !tt.HasText("Clear overrides") {
		t.Fatalf("all hosts with an override: %q", tt.Texts())
	}
	click(t, tt, "Clear overrides")
	if tt.HasText("Clear overrides") || f.override("").Codec != defaults.Auto {
		t.Fatalf("all hosts after clear: %+v", f.override(""))
	}

	f.overrides[""] = defaults.UserOverrides{FPS: "120"}
	click(t, tt, "Stream format for: bigboi")
	if tt.HasText("Clear overrides") {
		t.Fatalf("host scope inheriting only all hosts' values: %q", tt.Texts())
	}
	click(t, tt, "Frame rate: 240")
	if !tt.HasText("Clear overrides") {
		t.Fatalf("host scope with an override: %q", tt.Texts())
	}
	click(t, tt, "Clear overrides")
	if tt.HasText("Clear overrides") || f.override("h-bigboi") != (defaults.UserOverrides{}).Normalize() {
		t.Fatalf("host scope after clear: %+v", f.override("h-bigboi"))
	}
	if got := f.override(""); got.FPS != "120" {
		t.Fatalf("clear touched all hosts: %+v", got)
	}
}
