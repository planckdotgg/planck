package main

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/input"
)

// globalShortcuts registers the host's hotkeys with the desktop, so that
// they fire while Planck is not in front: mygo.GlobalShortcut, or a fake.
type globalShortcuts interface {
	Register(accelerator string, fn func()) error
	Unregister(accelerator string)
}

func (b *backend) Hotkeys() []ui.Hotkey {
	b.mu.Lock()
	keys := b.keys
	b.mu.Unlock()
	return hotkeysOf(keys)
}

func hotkeysOf(c config.ClientConfig) []ui.Hotkey {
	out := make([]ui.Hotkey, 0, len(config.HotkeyDefs))
	for _, d := range config.HotkeyDefs {
		out = append(out, ui.Hotkey{Action: d.Action, Chord: c.Hotkey(d.Action).String(), Default: d.Default.String(), Global: d.Global})
	}
	return out
}

func (b *backend) SetHotkey(a ui.HotkeyAction, chord string) error {
	return b.updateKeys(func(c *config.ClientConfig) error { return c.SetHotkey(a, chord, runtime.GOOS) })
}

func (b *backend) ResetHotkey(a ui.HotkeyAction) error {
	return b.updateKeys(func(c *config.ClientConfig) error { return c.ResetHotkey(a, runtime.GOOS) })
}

func (b *backend) ResetAllHotkeys() error {
	return b.updateKeys(func(c *config.ClientConfig) error { c.ResetAllHotkeys(); return nil })
}

func (b *backend) NeverSend() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.keys.NeverSend)
}

func (b *backend) AddNeverSend(chord string) error {
	return b.updateKeys(func(c *config.ClientConfig) error { return c.AddNeverSend(chord) })
}

func (b *backend) RemoveNeverSend(chord string) error {
	return b.updateKeys(func(c *config.ClientConfig) error { return c.RemoveNeverSend(chord) })
}

// updateKeys changes the hotkeys or the never-send list in client.json,
// then hands them to the key filters and the desktop. While hosting, a
// chord the desktop refuses is not saved and the old ones stay registered;
// a failed save puts the old ones back too.
func (b *backend) updateKeys(fn func(*config.ClientConfig) error) error {
	var keys, old config.ClientConfig
	registered := false
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		if err := fn(c); err != nil {
			return err
		}
		keys = config.ClientConfig{Hotkeys: c.Hotkeys, NeverSend: c.NeverSend}
		b.mu.Lock()
		old = b.keys
		b.mu.Unlock()
		if err := b.registerGlobals(keys, &old); err != nil {
			_ = b.registerGlobals(old, nil)
			return err
		}
		registered = true
		return nil
	})
	if err != nil {
		if registered {
			_ = b.registerGlobals(old, nil)
		}
		return err
	}
	b.mu.Lock()
	b.keys = keys
	b.mu.Unlock()
	if b.onKeys != nil {
		b.onKeys(b.Hotkeys(), b.NeverSend())
	}
	b.changed()
	return nil
}

// syncGlobals registers the host's hotkeys with the desktop while
// hosting, and takes them back otherwise.
func (b *backend) syncGlobals() {
	b.mu.Lock()
	keys := b.keys
	b.mu.Unlock()
	_ = b.registerGlobals(keys, nil)
}

// registerGlobals registers keys' host hotkeys with the desktop while
// hosting, in place of the ones registered now. With old, a chord the
// desktop refuses that differs from old's for its hotkey is an error;
// other refusals are logged.
func (b *backend) registerGlobals(keys config.ClientConfig, old *config.ClientConfig) error {
	if b.globals == nil {
		return nil
	}
	b.globalMu.Lock()
	defer b.globalMu.Unlock()
	for _, acc := range b.registered {
		b.globals.Unregister(acc)
	}
	b.registered = b.registered[:0]
	if !b.Hosting().On {
		return nil
	}
	for _, h := range hotkeysOf(keys) {
		fn := b.hostAction(h.Action)
		if !h.Global || h.Chord == "" || fn == nil {
			continue
		}
		if err := b.globals.Register(h.Chord, fn); err != nil {
			if old != nil && old.Hotkey(h.Action).String() != h.Chord {
				return &config.HotkeyError{Chord: keys.Hotkey(h.Action), Err: config.ErrDesktopRefused, Reason: err.Error()}
			}
			b.logf("planck: hotkey %s (%s): %v", h.Action, h.Chord, err)
			continue
		}
		b.registered = append(b.registered, h.Chord)
	}
	return nil
}

// hostAction is what a host hotkey does.
func (b *backend) hostAction(a ui.HotkeyAction) func() {
	switch a {
	case ui.HotkeyStopHosting:
		return func() {
			go func() {
				if err := b.SetHosting(context.Background(), false); err != nil {
					b.logf("planck: stop hosting: %v", err)
				}
			}()
		}
	case ui.HotkeyKickClient:
		return func() {
			go func() {
				if err := b.KickClient(); err != nil {
					b.logf("planck: kick: %v", err)
				}
			}()
		}
	}
	return nil
}

// SendCtrlAltDel presses and lets go of Ctrl+Alt+Del on the host, leaving
// the modifiers the host holds for this device down.
func (b *backend) SendCtrlAltDel() {
	c := b.current()
	if c == nil {
		return
	}
	for _, ev := range b.ctrlAltDel(c) {
		_ = c.SendInput(ev)
	}
}

// ctrlAltDel is the key events of Ctrl+Alt+Del as the host takes them,
// past the Cmd/Ctrl swap: the swap is for keys pressed here, and the host
// holds this device's Ctrl as its GUI key when it swaps.
func (b *backend) ctrlAltDel(c *session.Client) []input.Event {
	var held uint8
	if b.heldMods != nil {
		held = b.heldMods()
	}
	if b.swaps(c) {
		held = input.SwapCmdCtrl(input.Event{Type: input.TypeKey, Key: input.KeyEvent{Modifiers: held}}).Key.Modifiers
	}
	return input.CtrlAltDel(held)
}

// SwitchDisplay has nothing to switch to while a host streams one
// display, as every host does.
func (b *backend) SwitchDisplay() error {
	if b.current() == nil {
		return session.ErrNotConnected
	}
	return ui.ErrOneDisplay
}

func (b *backend) KickClient() error {
	b.mu.Lock()
	h := b.host
	b.mu.Unlock()
	if h == nil {
		return errors.New("this machine is not hosting")
	}
	if err := h.KickClient(); errors.Is(err, session.ErrNoClient) {
		return errors.New("no device is streaming from this machine")
	} else if err != nil {
		return err
	}
	b.changed()
	return nil
}

func (b *backend) AudioDevices() ui.AudioDevices {
	conv := func(list func() ([]audio.Device, error)) []ui.AudioDevice {
		devs, err := list()
		if err != nil {
			b.logf("planck: sound devices: %v", err)
			return nil
		}
		out := make([]ui.AudioDevice, len(devs))
		for i, d := range devs {
			out[i] = ui.AudioDevice{ID: d.ID, Name: d.Name, IsDefault: d.IsDefault}
		}
		return out
	}
	return ui.AudioDevices{Outputs: conv(b.audioOutputs), Inputs: conv(b.audioInputs)}
}

// RunningApps names each running app once, sorted.
func (b *backend) RunningApps() []string {
	apps, err := b.runningApps()
	if err != nil {
		b.logf("planck: running apps: %v", err)
		return nil
	}
	var names []string
	for _, a := range apps {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, a.Name) }) {
			names = append(names, a.Name)
		}
	}
	slices.SortFunc(names, func(x, y string) int { return strings.Compare(strings.ToLower(x), strings.ToLower(y)) })
	return names
}

func (b *backend) PrivacyModeProblem() string { return session.PrivacyModeProblem() }

// swapInput trades Cmd and Ctrl in ev when the user asked and the host's
// OS differs from this one's on them.
func (b *backend) swapInput(c *session.Client, ev input.Event) input.Event {
	if ev.Type != input.TypeKey || !b.swaps(c) {
		return ev
	}
	return input.SwapCmdCtrl(ev)
}

func (b *backend) swaps(c *session.Client) bool {
	if !b.swap.Load() {
		return false
	}
	b.mu.Lock()
	if b.hostOSOf != c {
		b.hostOSOf, b.hostOS = c, c.HostOS()
	}
	hostOS := b.hostOS
	b.mu.Unlock()
	return input.SwapsCmdCtrl(runtime.GOOS, hostOS)
}
