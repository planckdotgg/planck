package ui

import (
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/input"
)

// HIDUsage is the HID usage of each MyGo key the host has one for.
var HIDUsage = func() map[mui.Key]uint16 {
	m := map[mui.Key]uint16{
		mui.KeyEnter: input.KeyEnter, mui.KeyEscape: input.KeyEscape, mui.KeyBackspace: input.KeyBackspace,
		mui.KeyTab: input.KeyTab, mui.KeySpace: input.KeySpace, mui.KeyDelete: input.KeyDelete,
		mui.KeyInsert: input.KeyInsert, mui.KeyHome: input.KeyHome, mui.KeyEnd: input.KeyEnd,
		mui.KeyPageUp: input.KeyPageUp, mui.KeyPageDown: input.KeyPageDown,
		mui.KeyLeft: input.KeyLeft, mui.KeyRight: input.KeyRight, mui.KeyUp: input.KeyUp, mui.KeyDown: input.KeyDown,
		mui.KeyMinus: input.KeyMinus, mui.KeyEqual: input.KeyEqual, mui.KeyComma: input.KeyComma,
		mui.KeyPeriod: input.KeyPeriod, mui.KeySlash: input.KeySlash, mui.KeySemicolon: input.KeySemicolon,
		mui.KeyQuote: input.KeyQuote, mui.KeyBracketLeft: input.KeyLeftBracket, mui.KeyBracketRight: input.KeyRightBracket,
		mui.KeyBackslash: input.KeyBackslash, mui.KeyBackquote: input.KeyGrave, mui.KeyContextMenu: input.KeyApplication,
		mui.Key0: input.Key0,
	}
	for i, k := range []mui.Key{mui.KeyF1, mui.KeyF2, mui.KeyF3, mui.KeyF4, mui.KeyF5, mui.KeyF6, mui.KeyF7, mui.KeyF8, mui.KeyF9, mui.KeyF10, mui.KeyF11, mui.KeyF12} {
		m[k] = input.KeyF1 + uint16(i)
	}
	for i, k := range []mui.Key{mui.Key1, mui.Key2, mui.Key3, mui.Key4, mui.Key5, mui.Key6, mui.Key7, mui.Key8, mui.Key9} {
		m[k] = input.Key1 + uint16(i)
	}
	for i := range 26 {
		m[mui.KeyA+mui.Key(i)] = input.KeyA + uint16(i)
	}
	return m
}()

var muiKeys = func() map[uint16]mui.Key {
	m := make(map[uint16]mui.Key, len(HIDUsage))
	for k, u := range HIDUsage {
		m[u] = k
	}
	return m
}()

// ChordOf is the chord of a key with HID usage held with m.
func ChordOf(m mui.Modifiers, usage uint16) config.Chord {
	var mods config.Mods
	for _, p := range modPairs {
		if m&p.mui != 0 {
			mods |= p.mods
		}
	}
	return config.Chord{Mods: mods, Key: usage}
}

var modPairs = []struct {
	mui  mui.Modifiers
	mods config.Mods
}{{mui.Ctrl, config.ModCtrl}, {mui.Alt, config.ModAlt}, {mui.Shift, config.ModShift}, {mui.Super, config.ModSuper}}

// muiChord is a chord as a window shortcut takes it; ok is false for a
// chord MyGo has no key for, such as F13.
func muiChord(chord string) (m mui.Modifiers, k mui.Key, ok bool) {
	ch, err := config.ParseChord(chord)
	if err != nil || ch.IsZero() {
		return 0, 0, false
	}
	k, ok = muiKeys[ch.Key]
	for _, p := range modPairs {
		if ch.Mods&p.mods != 0 {
			m |= p.mui
		}
	}
	return m, k, ok
}

// hotkeyNames are what Settings and the menus call each hotkey.
var hotkeyNames = map[HotkeyAction]string{
	HotkeyStreamMenu:    "Stream menu",
	HotkeyHideButton:    "Hide the overlay button",
	HotkeyMetrics:       "Metrics window",
	HotkeyCycleHUD:      "Cycle HUD mode",
	HotkeyMicrophone:    "Microphone",
	HotkeyFullscreen:    "Full screen",
	HotkeyImmersive:     "Immersive mode (system keys)",
	HotkeyCursorLock:    "Lock the cursor to the stream",
	HotkeySwitchDisplay: "Switch host display",
	HotkeyCtrlAltDel:    "Send Ctrl+Alt+Del to the host",
	HotkeyKeyframe:      "Ask the host for a keyframe",
	HotkeyDisconnect:    "Disconnect",
	HotkeyStopHosting:   "Stop hosting",
	HotkeyKickClient:    "Kick the connected client",
}

var macKeyGlyphs = map[string]string{
	"Backspace": "⌫", "Delete": "⌦", "Enter": "↩", "Escape": "⎋", "Tab": "⇥",
	"Left": "←", "Right": "→", "Up": "↑", "Down": "↓", "PageUp": "⇞", "PageDown": "⇟",
}

// chordLabel shows a chord as goos does: "⌃⌥⇧⌘K" on macOS, "Ctrl Alt
// Shift Win K" elsewhere, and "" for no chord.
func chordLabel(goos, chord string) string {
	ch, err := config.ParseChord(chord)
	if err != nil || ch.IsZero() {
		return ""
	}
	key := input.KeyName(ch.Key)
	if goos == "darwin" {
		var b strings.Builder
		for _, g := range []struct {
			mod   config.Mods
			glyph string
		}{{config.ModCtrl, "⌃"}, {config.ModAlt, "⌥"}, {config.ModShift, "⇧"}, {config.ModSuper, "⌘"}} {
			if ch.Mods&g.mod != 0 {
				b.WriteString(g.glyph)
			}
		}
		if gl, ok := macKeyGlyphs[key]; ok {
			key = gl
		}
		return b.String() + key
	}
	var parts []string
	for _, w := range []struct {
		mod  config.Mods
		word string
	}{{config.ModCtrl, "Ctrl"}, {config.ModAlt, "Alt"}, {config.ModShift, "Shift"}, {config.ModSuper, "Win"}} {
		if ch.Mods&w.mod != 0 {
			parts = append(parts, w.word)
		}
	}
	return strings.Join(append(parts, key), " ")
}

// keyLabel is action's chord as this platform shows it, "" while unbound.
func (a *App) keyLabel(action HotkeyAction) string {
	for _, h := range a.backend.Hotkeys() {
		if h.Action == action {
			return chordLabel(a.goos, h.Chord)
		}
	}
	return ""
}

// keyText is keyLabel for a sentence: "a hotkey" while unbound.
func (a *App) keyText(action HotkeyAction) string {
	if l := a.keyLabel(action); l != "" {
		return l
	}
	return "a hotkey"
}
