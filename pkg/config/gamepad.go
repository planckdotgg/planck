package config

import (
	"encoding/hex"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/planckdotgg/planck/pkg/gamepad"
)

// GamepadConfigFile holds the gamepad settings of both roles, beside
// host.json and client.json; its own file keeps HostConfig comparable.
const GamepadConfigFile = "gamepad.json"

// Virtual controller types a host can plug in, as GamepadConfig.VirtualType
// holds them.
const (
	VirtualPadXbox360   = "xbox360"
	VirtualPadDS4       = "ds4"
	VirtualPadDualSense = "dualsense"
)

var virtualPadTypes = []string{VirtualPadXbox360, VirtualPadDS4, VirtualPadDualSense}

// Limits of the GamepadConfig percentages.
const (
	MaxGamepadDeadzone         = 50
	MaxGamepadTriggerThreshold = 50
	MaxGamepadRumbleStrength   = 100
)

type GamepadConfig struct {
	// The client's processing of its controllers: radial stick deadzones
	// and trigger thresholds in percent, and rumble strength in percent.
	LeftDeadzone     int  `json:"leftDeadzone"`
	RightDeadzone    int  `json:"rightDeadzone"`
	TriggerThreshold int  `json:"triggerThreshold"`
	InvertLeftY      bool `json:"invertLeftY,omitempty"`
	InvertRightY     bool `json:"invertRightY,omitempty"`
	RumbleEnabled    bool `json:"rumbleEnabled"`
	RumbleStrength   int  `json:"rumbleStrength"`
	// Mappings are the SDL gamecontrollerdb lines the user saved, by the
	// controller's SDL GUID; other controllers use their default mapping.
	Mappings map[string]string `json:"mappings,omitempty"`

	// VirtualType is what a host's virtual controllers appear as: one of
	// the VirtualPad constants.
	VirtualType string `json:"virtualType"`
	// AllowGuestGamepads lets clients other than this machine's owner
	// plug in controllers.
	AllowGuestGamepads bool `json:"allowGuestGamepads"`
}

func DefaultGamepadConfig() GamepadConfig {
	return GamepadConfig{
		RumbleEnabled:      true,
		RumbleStrength:     MaxGamepadRumbleStrength,
		VirtualType:        VirtualPadXbox360,
		AllowGuestGamepads: true,
	}
}

func (c GamepadConfig) Validate() error {
	for _, p := range []struct {
		name     string
		v, limit int
	}{
		{"leftDeadzone", c.LeftDeadzone, MaxGamepadDeadzone},
		{"rightDeadzone", c.RightDeadzone, MaxGamepadDeadzone},
		{"triggerThreshold", c.TriggerThreshold, MaxGamepadTriggerThreshold},
		{"rumbleStrength", c.RumbleStrength, MaxGamepadRumbleStrength},
	} {
		if p.v < 0 || p.v > p.limit {
			return fmt.Errorf("config: gamepad: %s %d: want 0 to %d", p.name, p.v, p.limit)
		}
	}
	if !slices.Contains(virtualPadTypes, c.VirtualType) {
		return fmt.Errorf("config: gamepad: unknown virtualType %q (want one of %v)", c.VirtualType, virtualPadTypes)
	}
	for _, guid := range slices.Sorted(maps.Keys(c.Mappings)) {
		if _, err := hex.DecodeString(guid); err != nil || len(guid) != 32 || guid != strings.ToLower(guid) {
			return fmt.Errorf("config: gamepad: mapping key %q: want a lower-case 32-digit SDL GUID", guid)
		}
		if first, _, _ := strings.Cut(c.Mappings[guid], ","); !strings.EqualFold(first, guid) {
			return fmt.Errorf("config: gamepad: mapping for %s starts with GUID %q", guid, first)
		}
		if _, err := gamepad.ParseSDLMapping(c.Mappings[guid]); err != nil {
			return fmt.Errorf("config: gamepad: mapping for %s: %w", guid, err)
		}
	}
	return nil
}

// SetMapping saves an SDL mapping line for its controller's GUID, or
// removes the saved one when line is empty.
func (c *GamepadConfig) SetMapping(guid, line string) {
	guid = strings.ToLower(guid)
	if line == "" {
		delete(c.Mappings, guid)
		return
	}
	if c.Mappings == nil {
		c.Mappings = map[string]string{}
	}
	c.Mappings[guid] = line
}

// LoadGamepadConfig reads <dir>/gamepad.json, or returns defaults when it
// does not exist.
func LoadGamepadConfig(dir string) (GamepadConfig, error) {
	cfg := DefaultGamepadConfig()
	if err := loadJSON(filepath.Join(dir, GamepadConfigFile), &cfg); err != nil {
		return GamepadConfig{}, err
	}
	return cfg, cfg.Validate()
}

func SaveGamepadConfig(dir string, cfg GamepadConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return saveJSON(filepath.Join(dir, GamepadConfigFile), cfg)
}

// UpdateGamepadConfig loads <dir>/gamepad.json, applies fn and saves the
// result.
func UpdateGamepadConfig(dir string, fn func(*GamepadConfig) error) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	cfg, err := LoadGamepadConfig(dir)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return SaveGamepadConfig(dir, cfg)
}
