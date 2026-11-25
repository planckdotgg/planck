package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ps4GUID = "030000004c050000c405000000010000"

func TestGamepadConfigRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planck")
	cfg, err := LoadGamepadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RumbleEnabled || cfg.RumbleStrength != 100 || cfg.VirtualType != VirtualPadXbox360 || !cfg.AllowGuestGamepads || cfg.LeftDeadzone != 0 {
		t.Fatalf("defaults = %+v", cfg)
	}
	err = UpdateGamepadConfig(dir, func(c *GamepadConfig) error {
		c.LeftDeadzone, c.RightDeadzone, c.TriggerThreshold = 12, 50, 5
		c.InvertRightY, c.RumbleEnabled, c.RumbleStrength = true, false, 40
		c.VirtualType, c.AllowGuestGamepads = VirtualPadDS4, false
		c.SetMapping(strings.ToUpper(ps4GUID), ps4GUID+",PS4 Controller,a:b1,b:b2,platform:Mac OS X,")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadGamepadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.LeftDeadzone != 12 || got.RightDeadzone != 50 || got.TriggerThreshold != 5 || !got.InvertRightY || got.InvertLeftY ||
		got.RumbleEnabled || got.RumbleStrength != 40 || got.VirtualType != VirtualPadDS4 || got.AllowGuestGamepads {
		t.Errorf("reloaded = %+v", got)
	}
	if !strings.HasPrefix(got.Mappings[ps4GUID], ps4GUID+",PS4 Controller,") {
		t.Errorf("mappings = %v", got.Mappings)
	}
	got.SetMapping(ps4GUID, "")
	if len(got.Mappings) != 0 {
		t.Errorf("SetMapping empty left %v", got.Mappings)
	}

	for _, f := range []string{HostConfigFile, ClientConfigFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("gamepad settings touched %s: %v", f, err)
		}
	}
}

func TestGamepadConfigKeepsDefaultsForMissingKeys(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, GamepadConfigFile), []byte(`{"leftDeadzone": 8}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGamepadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeftDeadzone != 8 || !cfg.RumbleEnabled || cfg.RumbleStrength != 100 || cfg.VirtualType != VirtualPadXbox360 {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestGamepadConfigValidateRejects(t *testing.T) {
	for name, edit := range map[string]func(*GamepadConfig){
		"deadzone":    func(c *GamepadConfig) { c.LeftDeadzone = 51 },
		"negative":    func(c *GamepadConfig) { c.RightDeadzone = -1 },
		"trigger":     func(c *GamepadConfig) { c.TriggerThreshold = 60 },
		"rumble":      func(c *GamepadConfig) { c.RumbleStrength = 101 },
		"type":        func(c *GamepadConfig) { c.VirtualType = "wiimote" },
		"empty type":  func(c *GamepadConfig) { c.VirtualType = "" },
		"bad guid":    func(c *GamepadConfig) { c.Mappings = map[string]string{"xyz": "xyz,Pad,a:b0"} },
		"upper guid":  func(c *GamepadConfig) { c.Mappings = map[string]string{strings.ToUpper(ps4GUID): ps4GUID + ",Pad,"} },
		"bad binding": func(c *GamepadConfig) { c.Mappings = map[string]string{ps4GUID: ps4GUID + ",Pad,a:garbage,"} },
		"mismatched line": func(c *GamepadConfig) {
			c.Mappings = map[string]string{ps4GUID: "78696e70757401000000000000000000,Pad,a:b0"}
		},
	} {
		c := DefaultGamepadConfig()
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
		if err := SaveGamepadConfig(t.TempDir(), c); err == nil {
			t.Errorf("%s: Save accepted it", name)
		}
	}
}
