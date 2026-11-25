package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/gamepad"
)

const padGUID = "030000005e0400008e02000000007200"

// onePad is a device with one XInput-layout controller a test presses.
type onePad struct {
	mu   sync.Mutex
	raw  gamepad.RawState
	hits [][2]uint8
}

func newOnePad() *onePad {
	stick := gamepad.Axis{Min: -32768, Max: 32767}
	trig := gamepad.Axis{Min: 0, Max: 255}
	return &onePad{raw: gamepad.RawState{Buttons: make([]bool, 11), Axes: []gamepad.Axis{stick, stick, stick, stick, trig, trig}, Hats: []uint8{0}}}
}

func (p *onePad) Controllers() []gamepad.ControllerInfo {
	return []gamepad.ControllerInfo{{ID: 7, Name: "Pad, Pro", GUID: padGUID, Connection: gamepad.ConnectionUSB, BatteryPct: -1, Connected: true}}
}

func (p *onePad) Read(id int, raw *gamepad.RawState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw.Buttons, raw.Axes, raw.Hats = append(raw.Buttons[:0], p.raw.Buttons...), append(raw.Axes[:0], p.raw.Axes...), append(raw.Hats[:0], p.raw.Hats...)
	return nil
}

func (p *onePad) Rumble(id int, large, small uint8) error {
	p.mu.Lock()
	p.hits = append(p.hits, [2]uint8{large, small})
	p.mu.Unlock()
	return nil
}

func (p *onePad) DefaultMapping(int) gamepad.Mapping {
	return gamepad.DefaultMapping(padGUID, "Pad, Pro")
}

func (p *onePad) press(b int, down bool) {
	p.mu.Lock()
	p.raw.Buttons[b] = down
	p.mu.Unlock()
}

func newPadBackend(t *testing.T, dir string, pad *onePad, status func() ui.VirtualPadStatus) *backend {
	t.Helper()
	b, err := newBackend(backendConfig{dir: dir, finder: newFinder("", nil, nil, t.Logf), logf: t.Logf,
		openGamepads: func() (session.GamepadSource, error) { return pad, nil }, virtualPads: status})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	return b
}

// TestGamepadSettingsPersist: the settings start at gamepad.json's
// defaults, keep in it, and refuse values config refuses.
func TestGamepadSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	b := newPadBackend(t, dir, newOnePad(), nil)
	s := b.GamepadSettings()
	if s != gamepadSettingsFrom(config.DefaultGamepadConfig()) {
		t.Fatalf("defaults: %+v", s)
	}
	s.LeftDeadzone, s.TriggerThreshold, s.InvertRightY, s.RumbleStrength, s.VirtualType = 12, 8, true, 40, ui.VirtualPadDS4
	if err := b.SetGamepadSettings(s); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadGamepadConfig(dir)
	if err != nil || gamepadSettingsFrom(stored) != s {
		t.Fatalf("stored %+v, %v; want %+v", stored, err, s)
	}
	bad := s
	bad.RightDeadzone = 60
	if err := b.SetGamepadSettings(bad); err == nil || b.GamepadSettings() != s {
		t.Fatalf("a 60 %% deadzone saved: %v, %+v", err, b.GamepadSettings())
	}
}

// TestGamepadsAndMapping: Gamepads serves the pump's controllers; a
// pasted line saves under the controller's GUID, an invalid one is
// refused, Map learns the next press, Unmap and Reset edit what is saved.
func TestGamepadsAndMapping(t *testing.T) {
	dir := t.TempDir()
	pad := newOnePad()
	b := newPadBackend(t, dir, pad, nil)
	var pads []ui.GamepadInfo
	waitUntil(t, "the controller", func() bool { pads = b.Gamepads(); return len(pads) == 1 })
	if p := pads[0]; p.ID != "7" || p.Transport != "USB" || p.Custom || !strings.HasPrefix(p.Mapping, padGUID+",Pad, Pro,") {
		t.Fatalf("controller: %+v", p)
	}
	pad.press(0, true)
	waitUntil(t, "A in the live state", func() bool { return b.Gamepads()[0].State.Buttons == gamepad.ButtonA })
	pad.press(0, false)

	if err := b.SetGamepadSDLMapping("7", "nonsense"); err == nil {
		t.Fatal("an invalid line saved")
	}
	other := "03000000ffffffffffffffffffff0000,Other Pad,a:b1,b:b0,"
	if err := b.SetGamepadSDLMapping("7", other); err != nil {
		t.Fatal(err)
	}
	stored, _ := config.LoadGamepadConfig(dir)
	if got := stored.Mappings[padGUID]; got != padGUID+",Other Pad,a:b1,b:b0," {
		t.Fatalf("saved %q", got)
	}
	waitUntil(t, "the custom mapping in use", func() bool { p := b.Gamepads()[0]; return p.Custom })

	if err := b.MapGamepadButton("7", "start"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "Map to wait", func() bool { return b.Gamepads()[0].Learning == "start" })
	pad.press(4, true)
	waitUntil(t, "start mapped to b4", func() bool {
		c, _ := config.LoadGamepadConfig(dir)
		return strings.Contains(c.Mappings[padGUID], "start:b4")
	})
	if err := b.UnmapGamepadButton("7", "a"); err != nil {
		t.Fatal(err)
	}
	stored, _ = config.LoadGamepadConfig(dir)
	if line := stored.Mappings[padGUID]; strings.Contains(line, ",a:") || !strings.Contains(line, "b:b0") {
		t.Fatalf("after unmap: %q", line)
	}
	if err := b.MapGamepadButton("7", "paddle"); err == nil {
		t.Fatal("mapped an unknown input")
	}
	if err := b.ResetGamepadMapping("7"); err != nil {
		t.Fatal(err)
	}
	if stored, _ := config.LoadGamepadConfig(dir); len(stored.Mappings) != 0 {
		t.Fatalf("after reset: %v", stored.Mappings)
	}
	if err := b.TestGamepadRumble("7"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the test rumble", func() bool {
		pad.mu.Lock()
		defer pad.mu.Unlock()
		return len(pad.hits) > 0 && pad.hits[0] == [2]uint8{255, 255}
	})
	if err := b.TestGamepadRumble("8"); err == nil {
		t.Fatal("rumbled a controller that is not there")
	}
}

// TestVirtualPadStatusCached: Settings asks every frame and the driver is
// probed once per TTL.
func TestVirtualPadStatusCached(t *testing.T) {
	var probes atomic.Int32
	b := newPadBackend(t, t.TempDir(), newOnePad(), func() ui.VirtualPadStatus {
		probes.Add(1)
		return ui.VirtualPadStatus{Installed: true, DriverName: "ViGEmBus", SupportedTypes: []gamepad.PadType{gamepad.PadXbox360}}
	})
	for range 200 {
		if !b.VirtualPadStatus().Installed {
			t.Fatal("status lost")
		}
	}
	if n := probes.Load(); n != 1 {
		t.Fatalf("probed %d times, want 1", n)
	}
}

// TestHostGamepadSlotsWhileHosting: no slots before hosting; while
// hosting, four free ones, which reset and refuse a reorder with nothing
// plugged in; the gamepad settings reach the running host.
func TestHostGamepadSlotsWhileHosting(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir(), nil)
	if b.HostGamepadSlots() != nil || b.ResetGamepadSlots() == nil {
		t.Fatal("slots without hosting")
	}
	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	slots := b.HostGamepadSlots()
	if len(slots) != 4 || slots[3] != (ui.HostGamepadSlot{Slot: 3, Pad: -1}) {
		t.Fatalf("slots: %+v", slots)
	}
	if err := b.ResetGamepadSlots(); err != nil {
		t.Fatal(err)
	}
	if err := b.ReorderGamepadSlot(0, 1); err == nil {
		t.Fatal("reordered empty slots")
	}
	s := b.GamepadSettings()
	s.AllowGuestGamepads, s.VirtualType = false, ui.VirtualPadDS4
	if err := b.SetGamepadSettings(s); err != nil {
		t.Fatal(err)
	}
	if got := b.runningHost().GamepadSettings(); got != (session.GamepadHostSettings{Allow: false, Type: gamepad.PadDS4}) {
		t.Fatalf("host's gamepad settings: %+v", got)
	}
}
