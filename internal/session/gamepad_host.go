package session

import (
	"errors"
	"sync"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/gamepad"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// GamepadHostSettings are what a host does with its clients' controllers.
type GamepadHostSettings struct {
	// Allow plugs clients' controllers into virtual ones; off, their
	// gamepad input is dropped and nothing is plugged in.
	Allow bool
	// Type is what the virtual controllers appear as to games.
	Type gamepad.PadType
}

// GamepadHostSettingsOf reads gamepad.json's hosting half; an unknown
// virtual type is an Xbox 360 pad, as config.Validate allows none.
func GamepadHostSettingsOf(c config.GamepadConfig) GamepadHostSettings {
	t, err := gamepad.ParsePadType(c.VirtualType)
	if err != nil {
		t = gamepad.PadXbox360
	}
	return GamepadHostSettings{Allow: c.AllowGuestGamepads, Type: t}
}

// VirtualPadSlots are an injector's plugged-in virtual controllers, as
// *gamepad.VirtualPads keeps them.
type VirtualPadSlots interface {
	Slots() [gamepad.MaxVirtualPads]int
	ResetSlots() error
	ReorderSlot(from, to int) error
	SetRumbleHandler(gamepad.RumbleFunc)
}

// PadInjector is an injector that plugs gamepad events into virtual
// controllers. VirtualPads opens them on first use.
type PadInjector interface {
	SetGamepadType(gamepad.PadType)
	VirtualPads() (VirtualPadSlots, error)
}

// ErrNoVirtualPads is ReorderGamepadSlot's error while no client
// controller is plugged in.
var ErrNoVirtualPads = errors.New("session: no virtual controllers are plugged in")

// hostPads is a host's gamepad settings and, once a client controller has
// plugged one in, its injector's virtual controllers.
type hostPads struct {
	// gate is held across a gamepad event's injection and a settings
	// change; mu, taken inside it, guards the fields.
	gate  sync.Mutex
	mu    sync.Mutex
	set   GamepadHostSettings
	slots VirtualPadSlots
	seq   uint32
}

func (p *hostPads) allowed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.set.Allow
}

func (p *hostPads) current() VirtualPadSlots {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.slots
}

// injectedGamepad runs after a gamepad event plugged into the injector:
// the first one finds its virtual controllers and routes their rumble.
func (h *Host) injectedGamepad() {
	pi, ok := h.inject.(PadInjector)
	if !ok || h.pads.current() != nil {
		return
	}
	slots, err := pi.VirtualPads()
	if err != nil {
		return
	}
	slots.SetRumbleHandler(h.sendRumble)
	h.pads.mu.Lock()
	h.pads.slots = slots
	h.pads.mu.Unlock()
}

// sendRumble passes a game's rumble on a virtual controller to the client
// controller feeding it.
func (h *Host) sendRumble(pad, large, small uint8) {
	sess := h.liveSession()
	if sess == nil {
		return
	}
	h.pads.mu.Lock()
	h.pads.seq++
	r := wire.Rumble{Pad: pad, Seq: h.pads.seq, Low: uint16(large) * 257, High: uint16(small) * 257}
	h.pads.mu.Unlock()
	if err := sess.SendRumble(r); err != nil {
		h.logf("planck: rumble to pad %d: %v", pad, err)
	}
}

// GamepadSettings are what the host does with clients' controllers now.
func (h *Host) GamepadSettings() GamepadHostSettings {
	h.pads.mu.Lock()
	defer h.pads.mu.Unlock()
	return h.pads.set
}

// SetGamepadSettings applies s at once. Turning gamepads off, or another
// virtual type, unplugs the virtual controllers: games see them go, and
// each client controller plugs in again, as the new type, on its next
// input.
func (h *Host) SetGamepadSettings(s GamepadHostSettings) error {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	h.pads.gate.Lock()
	defer h.pads.gate.Unlock()
	h.pads.mu.Lock()
	before, slots := h.pads.set, h.pads.slots
	h.pads.set = s
	h.pads.mu.Unlock()
	if h.injClosed {
		return nil
	}
	if pi, ok := h.inject.(PadInjector); ok {
		pi.SetGamepadType(s.Type)
	}
	if slots != nil && (before.Type != s.Type || before.Allow && !s.Allow) {
		return slots.ResetSlots()
	}
	return nil
}

// GamepadSlots are the client controller in each virtual controller slot,
// by its pad index, -1 for a free slot. They are all free until a client
// controller has plugged one in.
func (h *Host) GamepadSlots() [gamepad.MaxVirtualPads]int {
	if slots := h.pads.current(); slots != nil {
		return slots.Slots()
	}
	return [gamepad.MaxVirtualPads]int{-1, -1, -1, -1}
}

// ResetGamepadSlots unplugs every virtual controller; the client's
// controllers take slots again in the order they next send input.
func (h *Host) ResetGamepadSlots() error {
	if slots := h.pads.current(); slots != nil {
		return slots.ResetSlots()
	}
	return nil
}

// ReorderGamepadSlot swaps the client controllers in slots from and to,
// counted from 0, so a game's player order changes without a replug.
func (h *Host) ReorderGamepadSlot(from, to int) error {
	slots := h.pads.current()
	if slots == nil {
		return ErrNoVirtualPads
	}
	return slots.ReorderSlot(from, to)
}

// releasePads unplugs the virtual controllers of a client that left, so a
// game does not keep its last buttons held.
func (h *Host) releasePads() {
	if err := h.ResetGamepadSlots(); err != nil {
		h.logf("planck: unplug virtual controllers: %v", err)
	}
}

// injectGamepad plugs ev into a virtual controller unless the settings
// refuse gamepads, which drops it. gate keeps SetGamepadSettings from
// unplugging between the check and the plug, which would leave a pad
// plugged in with gamepads off.
func (h *Host) injectGamepad(ev input.Event) (dropped bool, err error) {
	h.pads.gate.Lock()
	defer h.pads.gate.Unlock()
	if !h.pads.allowed() {
		return true, nil
	}
	if err := h.inject.Inject(ev); err != nil {
		return false, err
	}
	h.injectedGamepad()
	return false, nil
}
