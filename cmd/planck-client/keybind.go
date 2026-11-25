package main

import (
	"sync/atomic"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/input"
)

// localKeys are the chords that stay on this device, even while
// immersive mode takes the keyboard: the stream's hotkeys, which the
// window acts on, and the never-send list. The key tap reads it on its
// own thread. A nil *localKeys holds the default hotkeys.
type localKeys struct {
	chords atomic.Pointer[map[config.Chord]bool]
}

var defaultLocalKeys = func() *localKeys {
	l := &localKeys{}
	m := map[config.Chord]bool{}
	for _, ch := range config.DefaultClientConfig().HotkeyChords() {
		m[ch] = true
	}
	l.chords.Store(&m)
	return l
}()

// update takes the hotkeys bound now and the never-send list.
func (l *localKeys) update(hotkeys []ui.Hotkey, never []string) {
	m := map[config.Chord]bool{}
	for _, h := range hotkeys {
		if ch, err := config.ParseChord(h.Chord); err == nil && !ch.IsZero() && !h.Global {
			m[ch] = true
		}
	}
	for _, s := range never {
		if ch, err := config.ParseChord(s); err == nil && !ch.IsZero() {
			m[ch] = true
		}
	}
	l.chords.Store(&m)
}

// has reports whether the key with this HID usage, held with exactly m,
// stays on this device.
func (l *localKeys) has(m mui.Modifiers, usage uint16) bool {
	if l == nil {
		l = defaultLocalKeys
	}
	set := l.chords.Load()
	return set != nil && (*set)[ui.ChordOf(m, usage)]
}

// guiModifier is mui.Super when mods, HID modifier bits, hold Cmd or the
// Windows key.
func guiModifier(mods uint8) mui.Modifiers {
	if mods&(input.ModLeftGUI|input.ModRightGUI) != 0 {
		return mui.Super
	}
	return 0
}
