package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/gamepad"
)

// testRumbleFor is how long Test rumble shakes a controller.
const testRumbleFor = 400 * time.Millisecond

var errNotHosting = errors.New("this machine is not hosting")

// noGamepads is a device with no controllers.
type noGamepads struct{}

func (noGamepads) Controllers() []gamepad.ControllerInfo { return nil }
func (noGamepads) Read(int, *gamepad.RawState) error     { return gamepad.ErrNotConnected }
func (noGamepads) Rumble(int, uint8, uint8) error        { return gamepad.ErrNotConnected }
func (noGamepads) DefaultMapping(int) gamepad.Mapping    { return gamepad.DefaultMapping("", "") }

// gamepadProfile is what the pump does with this device's controllers.
func gamepadProfile(c config.GamepadConfig, logf func(string, ...any)) session.GamepadProfile {
	p := session.GamepadProfile{
		Calibration: gamepad.Calibration{LeftDeadzone: c.LeftDeadzone, RightDeadzone: c.RightDeadzone, TriggerThreshold: c.TriggerThreshold,
			InvertLeftY: c.InvertLeftY, InvertRightY: c.InvertRightY, RumbleOff: !c.RumbleEnabled, RumbleStrength: c.RumbleStrength},
		Mappings: map[string]gamepad.Mapping{},
	}
	for guid, line := range c.Mappings {
		m, err := gamepad.ParseSDLMapping(line)
		if err != nil {
			logf("planck: gamepad mapping for %s: %v", guid, err)
			continue
		}
		p.Mappings[guid] = m
	}
	return p
}

func gamepadSettingsFrom(c config.GamepadConfig) ui.GamepadSettings {
	return ui.GamepadSettings{LeftDeadzone: c.LeftDeadzone, RightDeadzone: c.RightDeadzone, TriggerThreshold: c.TriggerThreshold,
		InvertLeftY: c.InvertLeftY, InvertRightY: c.InvertRightY, RumbleEnabled: c.RumbleEnabled, RumbleStrength: c.RumbleStrength,
		VirtualType: c.VirtualType, AllowGuestGamepads: c.AllowGuestGamepads}
}

func (b *backend) GamepadSettings() ui.GamepadSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return gamepadSettingsFrom(b.gamepadCfg)
}

// SetGamepadSettings keeps s in gamepad.json and applies it at once: to
// the controllers a stream sends, and to hosting.
func (b *backend) SetGamepadSettings(s ui.GamepadSettings) error {
	return b.updateGamepads(func(c *config.GamepadConfig) error {
		c.LeftDeadzone, c.RightDeadzone, c.TriggerThreshold = s.LeftDeadzone, s.RightDeadzone, s.TriggerThreshold
		c.InvertLeftY, c.InvertRightY, c.RumbleEnabled, c.RumbleStrength = s.InvertLeftY, s.InvertRightY, s.RumbleEnabled, s.RumbleStrength
		c.VirtualType, c.AllowGuestGamepads = s.VirtualType, s.AllowGuestGamepads
		return nil
	})
}

// updateGamepads edits gamepad.json and hands the result to the pump and
// the running host.
func (b *backend) updateGamepads(fn func(*config.GamepadConfig) error) error {
	var stored config.GamepadConfig
	err := config.UpdateGamepadConfig(b.dir, func(c *config.GamepadConfig) error {
		if err := fn(c); err != nil {
			return err
		}
		stored = *c
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.gamepadCfg = stored
	h := b.host
	b.mu.Unlock()
	b.pads.SetProfile(gamepadProfile(stored, b.logf))
	if h != nil {
		if err := h.SetGamepadSettings(session.GamepadHostSettingsOf(stored)); err != nil {
			b.logf("planck: virtual controllers: %v", err)
		}
	}
	b.changed()
	return nil
}

// Gamepads keeps the pump reading for Settings and serves its last
// snapshot; it never reads a controller itself.
func (b *backend) Gamepads() []ui.GamepadInfo {
	snap := b.pads.Watch()
	out := make([]ui.GamepadInfo, len(snap))
	for i, s := range snap {
		out[i] = gamepadInfo(s)
	}
	return out
}

func gamepadInfo(s session.PadSnapshot) ui.GamepadInfo {
	g := ui.GamepadInfo{ID: strconv.Itoa(s.Info.ID), Name: s.Info.Name, GUID: s.Info.GUID, BatteryPct: s.Info.BatteryPct, Slot: s.Info.Slot,
		State: s.State, Buttons: s.Raw.Buttons, Hats: s.Raw.Hats, Mapping: s.Mapping.FormatSDLMapping(), Custom: s.Custom}
	if s.Info.Connection != gamepad.ConnectionUnknown {
		g.Transport = s.Info.Connection.String()
	}
	g.Axes = make([]float64, len(s.Raw.Axes))
	for i, a := range s.Raw.Axes {
		g.Axes[i] = a.Norm()
	}
	if s.IsLearning {
		g.Learning = s.Learning.String()
	}
	return g
}

// pad is the controller with id as Gamepads names it.
func (b *backend) pad(id string) (session.PadSnapshot, error) {
	n, err := strconv.Atoi(id)
	if err == nil {
		for _, s := range b.pads.Watch() {
			if s.Info.ID == n {
				return s, nil
			}
		}
	}
	return session.PadSnapshot{}, fmt.Errorf("no controller %q is attached", id)
}

// MapGamepadButton binds target to the next control of controller id that
// moves; the pump watches for it in the background.
func (b *backend) MapGamepadButton(id, target string) error {
	in, err := gamepad.ParseInput(target)
	if err != nil {
		return err
	}
	p, err := b.pad(id)
	if err != nil {
		return err
	}
	b.pads.Learn(p.Info.ID, in, func(bind gamepad.Binding) {
		if err := b.editMapping(p.Info.ID, func(m *gamepad.Mapping) { m.Map(in, bind) }); err != nil {
			b.logf("planck: map %s: %v", target, err)
		}
	})
	b.changed()
	return nil
}

func (b *backend) UnmapGamepadButton(id, target string) error {
	in, err := gamepad.ParseInput(target)
	if err != nil {
		return err
	}
	p, err := b.pad(id)
	if err != nil {
		return err
	}
	return b.editMapping(p.Info.ID, func(m *gamepad.Mapping) { m.Unmap(in) })
}

// ResetGamepadMapping forgets the saved mapping of controller id's model.
func (b *backend) ResetGamepadMapping(id string) error {
	p, err := b.pad(id)
	if err != nil {
		return err
	}
	b.pads.CancelLearn()
	return b.updateGamepads(func(c *config.GamepadConfig) error {
		c.SetMapping(p.Info.GUID, "")
		return nil
	})
}

// SetGamepadSDLMapping saves a gamecontrollerdb line for controller id's
// model. A line written for another GUID applies to this controller, so a
// line found for the same model under another bus or driver works too.
func (b *backend) SetGamepadSDLMapping(id, sdl string) error {
	p, err := b.pad(id)
	if err != nil {
		return err
	}
	m, err := gamepad.ParseSDLMapping(strings.TrimSpace(sdl))
	if err != nil {
		return err
	}
	return b.saveMapping(p.Info, m)
}

// editMapping changes the mapping controller id uses now, as the pump last
// read it, and saves it for the controller's model.
func (b *backend) editMapping(id int, fn func(*gamepad.Mapping)) error {
	for _, s := range b.pads.Watch() {
		if s.Info.ID == id {
			m := s.Mapping
			fn(&m)
			return b.saveMapping(s.Info, m)
		}
	}
	return gamepad.ErrNotConnected
}

func (b *backend) saveMapping(info gamepad.ControllerInfo, m gamepad.Mapping) error {
	if !gamepad.ValidGUID(info.GUID) {
		return fmt.Errorf("%s has no SDL GUID to save a mapping under", info.Name)
	}
	m.GUID = strings.ToLower(info.GUID)
	// A comma in the name would end the field in the saved line.
	m.Name = strings.ReplaceAll(m.Name, ",", " ")
	if m.Name == "" {
		m.Name = strings.ReplaceAll(info.Name, ",", " ")
	}
	return b.updateGamepads(func(c *config.GamepadConfig) error {
		c.SetMapping(m.GUID, m.FormatSDLMapping())
		return nil
	})
}

// TestGamepadRumble shakes controller id off the UI thread.
func (b *backend) TestGamepadRumble(id string) error {
	p, err := b.pad(id)
	if err != nil {
		return err
	}
	if !b.GamepadSettings().RumbleEnabled {
		return errors.New("rumble is off")
	}
	go func() {
		if err := b.pads.TestRumble(p.Info.ID, testRumbleFor); err != nil {
			b.logf("planck: test rumble: %v", err)
		}
	}()
	return nil
}

func (b *backend) VirtualPadStatus() ui.VirtualPadStatus { return b.caches.virtualPads.get() }

func samePadStatus(x, y ui.VirtualPadStatus) bool {
	return x.Installed == y.Installed && x.DriverName == y.DriverName && x.InstallHint == y.InstallHint &&
		slices.Equal(x.SupportedTypes, y.SupportedTypes)
}

func (b *backend) runningHost() *session.Host {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.host
}

// HostGamepadSlots reads the running host's slots, which it keeps in
// memory; nil while not hosting.
func (b *backend) HostGamepadSlots() []ui.HostGamepadSlot {
	h := b.runningHost()
	if h == nil {
		return nil
	}
	slots := h.GamepadSlots()
	out := make([]ui.HostGamepadSlot, len(slots))
	for i, pad := range slots {
		out[i] = ui.HostGamepadSlot{Slot: i, Pad: pad}
	}
	return out
}

func (b *backend) ResetGamepadSlots() error {
	h := b.runningHost()
	if h == nil {
		return errNotHosting
	}
	err := h.ResetGamepadSlots()
	b.changed()
	return err
}

func (b *backend) ReorderGamepadSlot(from, to int) error {
	h := b.runningHost()
	if h == nil {
		return errNotHosting
	}
	err := h.ReorderGamepadSlot(from, to)
	b.changed()
	return err
}
