package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/gamepad"
)

// fakeGamepad is the fake backend's gamepad state.
type fakeGamepad struct {
	settings GamepadSettings
	saves    int
	pads     []GamepadInfo
	asked    int
	calls    []string
	status   VirtualPadStatus
	slots    []HostGamepadSlot
}

func (f *fakeBackend) GamepadSettings() GamepadSettings { defer f.lock()(); return f.gamepad.settings }
func (f *fakeBackend) SetGamepadSettings(s GamepadSettings) error {
	defer f.lock()()
	f.gamepad.settings, f.gamepad.saves = s, f.gamepad.saves+1
	return nil
}
func (f *fakeBackend) Gamepads() []GamepadInfo {
	defer f.lock()()
	f.gamepad.asked++
	return slices.Clone(f.gamepad.pads)
}
func (f *fakeBackend) padCall(name, id, arg string) {
	f.gamepad.calls = append(f.gamepad.calls, strings.TrimSpace(name+" "+id+" "+arg))
}
func (f *fakeBackend) MapGamepadButton(id, target string) error {
	defer f.lock()()
	f.padCall("map", id, target)
	for i := range f.gamepad.pads {
		if f.gamepad.pads[i].ID == id {
			f.gamepad.pads[i].Learning = target
		}
	}
	return nil
}
func (f *fakeBackend) UnmapGamepadButton(id, target string) error {
	defer f.lock()()
	f.padCall("unmap", id, target)
	return nil
}
func (f *fakeBackend) ResetGamepadMapping(id string) error {
	defer f.lock()()
	f.padCall("reset", id, "")
	return nil
}
func (f *fakeBackend) SetGamepadSDLMapping(id, sdl string) error {
	defer f.lock()()
	if _, err := gamepad.ParseSDLMapping(sdl); err != nil {
		return err
	}
	f.padCall("paste", id, sdl)
	return nil
}
func (f *fakeBackend) TestGamepadRumble(id string) error {
	defer f.lock()()
	f.padCall("rumble", id, "")
	return nil
}
func (f *fakeBackend) VirtualPadStatus() VirtualPadStatus { defer f.lock()(); return f.gamepad.status }
func (f *fakeBackend) HostGamepadSlots() []HostGamepadSlot {
	defer f.lock()()
	return slices.Clone(f.gamepad.slots)
}
func (f *fakeBackend) ResetGamepadSlots() error {
	defer f.lock()()
	f.padCall("reset slots", "", "")
	return nil
}
func (f *fakeBackend) ReorderGamepadSlot(from, to int) error {
	defer f.lock()()
	if f.gamepad.slots == nil {
		return errors.New("not hosting")
	}
	f.gamepad.slots[from].Pad, f.gamepad.slots[to].Pad = f.gamepad.slots[to].Pad, f.gamepad.slots[from].Pad
	return nil
}

func (f *fakeBackend) padCalls() []string { defer f.lock()(); return slices.Clone(f.gamepad.calls) }

const testPadGUID = "030000005e0400008e02000000007200"

func sampleGamepad() GamepadInfo {
	m := gamepad.DefaultMapping(testPadGUID, "Xbox Wireless Controller")
	return GamepadInfo{ID: "3", Name: "Xbox Wireless Controller", GUID: testPadGUID, Transport: "Bluetooth", BatteryPct: 75, Slot: 0,
		State:   GamepadState{Buttons: gamepad.ButtonA | gamepad.ButtonDPadLeft, LT: 255, LX: 32767, RY: -16384},
		Buttons: make([]bool, 11), Axes: make([]float64, 6), Hats: []uint8{0}, Mapping: m.FormatSDLMapping()}
}

func gamepadFake() *fakeBackend {
	f := newFake()
	f.gamepad.settings = GamepadSettings{RumbleEnabled: true, RumbleStrength: 100, VirtualType: VirtualPadXbox360, AllowGuestGamepads: true}
	return f
}

// TestGamepadEmpty: with no controller attached the page says so and
// offers no preview or mapping, yet still asks for controllers.
func TestGamepadEmpty(t *testing.T) {
	f := gamepadFake()
	_, tt := settingsOn(t, f, catGamepad, "darwin")
	if !tt.HasText("No controllers attached") || tt.HasText("Test rumble") || tt.HasText("Copy SDL map") {
		t.Fatalf("empty state: %q", tt.Texts())
	}
	if f.gamepad.asked == 0 {
		t.Fatal("the page never asked for controllers")
	}
	snapshot(t, tt, "settings-gamepad-empty")
}

// TestGamepadPreview: an attached controller lists its transport and
// battery, and the live state shows what it presses, its sticks and its
// triggers; Test rumble asks the backend.
func TestGamepadPreview(t *testing.T) {
	f := gamepadFake()
	f.gamepad.pads = []GamepadInfo{sampleGamepad()}
	_, tt := settingsOn(t, f, catGamepad, "windows")
	for _, want := range []string{"Xbox Wireless Controller", "Bluetooth", "75 %", "Pad 1 in a stream"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q: %q", want, tt.Texts())
		}
	}
	for _, name := range []string{"A pressed", "← pressed", "B released", "Left stick X +100 %, Y +0 %", "Right stick X +0 %, Y -50 %", "LT 100 %", "RT 0 %"} {
		if _, ok := tt.Access(name); !ok {
			t.Fatalf("no %q in the live state", name)
		}
	}
	snapshot(t, tt, "settings-gamepad-preview")
	click(t, tt, "Test rumble")
	if got := f.padCalls(); !slices.Equal(got, []string{"rumble 3"}) {
		t.Fatalf("calls %q", got)
	}
}

// TestGamepadCalibration: the sliders and switches edit gamepad.json's
// settings, and rumble strength hides while rumble is off.
func TestGamepadCalibration(t *testing.T) {
	f := gamepadFake()
	_, tt := settingsOn(t, f, catGamepad, "darwin")
	for _, name := range []string{"Left stick deadzone", "Right stick deadzone", "Trigger threshold"} {
		clickSlider(t, tt, name)
	}
	s := f.GamepadSettings()
	for _, v := range []int{s.LeftDeadzone, s.RightDeadzone, s.TriggerThreshold} {
		if v < 20 || v > 30 {
			t.Fatalf("sliders' middles gave %+v, want about 25 %%", s)
		}
	}
	clickScrolled(t, tt, "Invert right stick Y")
	if s := f.GamepadSettings(); !s.InvertRightY || s.InvertLeftY {
		t.Fatalf("invert: %+v", s)
	}
	clickSlider(t, tt, "Rumble strength")
	if s := f.GamepadSettings(); s.RumbleStrength < 40 || s.RumbleStrength > 60 {
		t.Fatalf("rumble strength from the middle: %d", s.RumbleStrength)
	}
	clickScrolled(t, tt, "Rumble")
	if s := f.GamepadSettings(); s.RumbleEnabled {
		t.Fatal("Rumble did not turn off")
	}
	if _, ok := tt.AccessByRole(mui.RoleSlider, "Rumble strength"); ok {
		t.Fatal("rumble strength shown with rumble off")
	}
}

// clickSlider clicks the middle of the slider named name.
func clickSlider(t *testing.T, tt *mui.Tester, name string) {
	t.Helper()
	scrollTo(t, tt, name)
	info, ok := tt.AccessByRole(mui.RoleSlider, name)
	if !ok {
		t.Fatalf("no %q slider", name)
	}
	r := info.Bounds
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
}

// TestGamepadMapping: Map waits for a control and says what to move,
// Unmap and Reset reach the backend, Copy puts the line on the clipboard,
// and Paste takes a valid line and refuses an invalid one inline.
func TestGamepadMapping(t *testing.T) {
	f := gamepadFake()
	f.gamepad.pads = []GamepadInfo{sampleGamepad()}
	_, tt := settingsOn(t, f, catGamepad, "darwin")
	if !tt.HasText("b0") {
		t.Fatalf("A's binding not shown: %q", tt.Texts())
	}
	choose(t, tt, "Input to map", "Left stick Y")
	clickScrolled(t, tt, "Map")
	if !tt.HasText("Push the stick for Left stick Y down. Map waits ten seconds.") {
		t.Fatalf("no prompt: %q", tt.Texts())
	}
	clickScrolled(t, tt, "Unmap")
	clickScrolled(t, tt, "Reset to default")
	clickScrolled(t, tt, "Copy SDL map")
	if tt.Clipboard() != sampleGamepad().Mapping {
		t.Fatalf("copied %q", tt.Clipboard())
	}
	f.set(func(f *fakeBackend) { f.gamepad.pads[0].Learning = "" })

	tt.SetClipboard("not a mapping")
	clickScrolled(t, tt, "Paste SDL map")
	if !tt.HasText("That is not an SDL mapping: mapping \"not a mapping\": want GUID,name,bindings") {
		t.Fatalf("no inline error: %q", tt.Texts())
	}
	line := testPadGUID + ",Pad,a:b1,b:b0,"
	tt.SetClipboard(line)
	clickScrolled(t, tt, "Paste SDL map")
	if !tt.HasText("Pasted the mapping.") || tt.HasText("That is not an SDL mapping") {
		t.Fatalf("valid paste: %q", tt.Texts())
	}
	want := []string{"map 3 lefty", "unmap 3 lefty", "reset 3", "paste 3 " + line}
	if got := f.padCalls(); !slices.Equal(got, want) {
		t.Fatalf("calls\n got %q\nwant %q", got, want)
	}
}

// TestGamepadHosting: the virtual type picker saves, DualSense says
// ViGEmBus plugs in a DualShock 4, the driver line reads the driver, and
// the slots move and reset.
func TestGamepadHosting(t *testing.T) {
	f := gamepadFake()
	f.gamepad.status = gamepad.PadDriverStatus{Installed: true, DriverName: "Nefarius Virtual Gamepad Emulation Bus",
		SupportedTypes: []gamepad.PadType{gamepad.PadXbox360, gamepad.PadDS4}}
	f.gamepad.slots = []HostGamepadSlot{{0, 0}, {1, 1}, {2, -1}, {3, -1}}
	_, tt := settingsOn(t, f, catGamepad, "windows")
	if !tt.HasText("Nefarius Virtual Gamepad Emulation Bus is installed.") {
		t.Fatalf("driver line: %q", tt.Texts())
	}
	choose(t, tt, "Virtual controller type", "DualSense")
	if got := f.GamepadSettings().VirtualType; got != config.VirtualPadDualSense {
		t.Fatalf("virtual type %q", got)
	}
	if !tt.HasText("Nefarius Virtual Gamepad Emulation Bus has no DualSense. Games see a DualShock 4, which most treat the same.") {
		t.Fatalf("no DualSense note: %q", tt.Texts())
	}
	clickScrolled(t, tt, "Allow guest gamepads")
	if f.GamepadSettings().AllowGuestGamepads {
		t.Fatal("guest gamepads still allowed")
	}

	if !tt.HasText("Client controller 1") || !tt.HasText("Client controller 2") {
		t.Fatalf("slots: %q", tt.Texts())
	}
	snapshot(t, tt, "settings-gamepad-hosting")
	clickScrolled(t, tt, "Move slot 1 down")
	if got := f.HostGamepadSlots(); got[0].Pad != 1 || got[1].Pad != 0 {
		t.Fatalf("slots after move: %+v", got)
	}
	if info, ok := tt.Access("Move slot 1 up"); !ok || !info.Disabled {
		t.Fatalf("slot 1 can move up: %+v", info)
	}
	clickScrolled(t, tt, "Reset slots")
	if got := f.padCalls(); !slices.Equal(got, []string{"reset slots"}) {
		t.Fatalf("calls %q", got)
	}

	f.set(func(f *fakeBackend) {
		f.gamepad.status = gamepad.PadDriverStatus{DriverName: "Nefarius Virtual Gamepad Emulation Bus", InstallHint: "Install ViGEmBus."}
		f.gamepad.slots = nil
	})
	tt.Frame()
	if !tt.HasText("Nefarius Virtual Gamepad Emulation Bus is not installed.") || !tt.HasText("Install ViGEmBus.") ||
		!tt.HasText("Slots fill while this PC hosts.") || tt.HasText("Reset slots") {
		t.Fatalf("no driver, not hosting: %q", tt.Texts())
	}
	f.set(func(f *fakeBackend) {
		f.gamepad.status = gamepad.PadDriverStatus{InstallHint: "Virtual gamepads are not available on macOS hosts (requires DriverKit)."}
	})
	tt.Frame()
	if !tt.HasText("Virtual gamepads are not available on macOS hosts (requires DriverKit).") {
		t.Fatalf("macOS note: %q", tt.Texts())
	}
}

// TestGamepadSearch: Settings search finds the gamepad rows by their
// words.
func TestGamepadSearch(t *testing.T) {
	for q, want := range map[string]string{
		"deadzone": "gamepad.leftDeadzone", "rumble": "gamepad.rumble", "invert": "gamepad.invertRight",
		"vigembus": "gamepad.driver", "sdl": "gamepad.mapping", "virtual controller": "gamepad.virtualType",
		"slots": "gamepad.slots", "dualshock": "gamepad.controllers",
	} {
		if !slices.ContainsFunc(hits(q)[catGamepad], func(e *settingEntry) bool { return e.id == want }) {
			t.Errorf("search %q does not find %s", q, want)
		}
	}
	f := gamepadFake()
	a, tt := settingsOn(t, f, catGeneral, "darwin")
	a.set.query = "vigembus"
	tt.Frame()
	if !tt.HasText("Virtual gamepad driver") {
		t.Fatalf("search page: %q", tt.Texts())
	}
}
