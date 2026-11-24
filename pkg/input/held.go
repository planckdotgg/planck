package input

import "sync"

// Held tracks the keys and mouse buttons a host injected down and not up,
// to let go of them when the client that pressed them is gone: a client
// that drops mid Alt-Tab must not leave Alt down on the host.
type Held struct {
	mu      sync.Mutex
	keys    [256]bool
	buttons [ButtonX2 + 1]bool
}

// Note records an event the host injected.
func (h *Held) Note(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch ev.Type {
	case TypeKey:
		if ev.Key.Usage < uint16(len(h.keys)) {
			h.keys[ev.Key.Usage] = ev.Key.Pressed
		}
	case TypeMouseButton:
		if int(ev.Button.Button) < len(h.buttons) {
			h.buttons[ev.Button.Button] = ev.Button.Pressed
		}
	}
}

// KeyDown reports whether the key with usage is held.
func (h *Held) KeyDown(usage uint16) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return usage < uint16(len(h.keys)) && h.keys[usage]
}

// Modifiers is the modifier keys held, as Mod* bits.
func (h *Held) Modifiers() uint8 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modifiers()
}

func (h *Held) modifiers() uint8 {
	var mods uint8
	for u := KeyLeftControl; u <= KeyRightGUI; u++ {
		if h.keys[u] {
			mods |= 1 << (u - KeyLeftControl)
		}
	}
	return mods
}

// Release forgets what is held and returns the events that let go of it:
// buttons, then keys, then modifiers, each key event carrying the
// modifiers still down after it.
func (h *Held) Release() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Event
	for b, down := range h.buttons {
		if down {
			out = append(out, Event{Type: TypeMouseButton, Button: MouseButton{Button: Button(b)}})
		}
	}
	mods := h.modifiers()
	for u, down := range h.keys {
		if down && (uint16(u) < KeyLeftControl || uint16(u) > KeyRightGUI) {
			out = append(out, Event{Type: TypeKey, Key: KeyEvent{Usage: uint16(u), Modifiers: mods}})
		}
	}
	for u := KeyLeftControl; u <= KeyRightGUI; u++ {
		if h.keys[u] {
			mods &^= 1 << (u - KeyLeftControl)
			out = append(out, Event{Type: TypeKey, Key: KeyEvent{Usage: u, Modifiers: mods}})
		}
	}
	h.keys, h.buttons = [256]bool{}, [ButtonX2 + 1]bool{}
	return out
}
