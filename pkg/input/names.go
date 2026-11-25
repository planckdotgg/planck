package input

import (
	"fmt"
	"strings"
)

// keyNames are the names chords use for keys, as an accelerator writes
// them: letters and digits as themselves, punctuation as its character.
var keyNames = func() map[uint16]string {
	m := map[uint16]string{
		KeyEnter: "Enter", KeyEscape: "Escape", KeyBackspace: "Backspace", KeyTab: "Tab", KeySpace: "Space",
		KeyMinus: "-", KeyEqual: "=", KeyLeftBracket: "[", KeyRightBracket: "]", KeyBackslash: `\`,
		KeySemicolon: ";", KeyQuote: "'", KeyGrave: "`", KeyComma: ",", KeyPeriod: ".", KeySlash: "/",
		KeyCapsLock: "CapsLock", KeyPrintScreen: "PrintScreen", KeyScrollLock: "ScrollLock", KeyPause: "Pause",
		KeyInsert: "Insert", KeyHome: "Home", KeyPageUp: "PageUp", KeyDelete: "Delete", KeyEnd: "End",
		KeyPageDown: "PageDown", KeyRight: "Right", KeyLeft: "Left", KeyDown: "Down", KeyUp: "Up",
		KeyApplication: "ContextMenu", KeyMute: "VolumeMute", KeyVolumeUp: "VolumeUp", KeyVolumeDown: "VolumeDown",
	}
	for i := range uint16(26) {
		m[KeyA+i] = string(rune('A' + i))
	}
	for i := range uint16(9) {
		m[Key1+i] = string(rune('1' + i))
	}
	m[Key0] = "0"
	for i := range uint16(12) {
		m[KeyF1+i] = fmt.Sprintf("F%d", i+1)
		m[KeyF13+i] = fmt.Sprintf("F%d", i+13)
	}
	return m
}()

var keysByName = func() map[string]uint16 {
	m := make(map[string]uint16, len(keyNames))
	for u, n := range keyNames {
		m[strings.ToLower(n)] = u
	}
	for alias, u := range map[string]uint16{"return": KeyEnter, "esc": KeyEscape, "del": KeyDelete, "ins": KeyInsert} {
		m[alias] = u
	}
	return m
}()

// KeyName is the chord name of the key with this HID usage, "" for a key
// chords cannot name, modifiers among them.
func KeyName(usage uint16) string { return keyNames[usage] }

// KeyByName is the HID usage of a key by its chord name, in any case.
func KeyByName(name string) (uint16, bool) {
	u, ok := keysByName[strings.ToLower(name)]
	return u, ok
}

// SwapsCmdCtrl reports whether a Cmd and Ctrl swap between a client and a
// host on these GOOS values does anything: one is macOS and the other not.
func SwapsCmdCtrl(clientOS, hostOS string) bool {
	return clientOS != "" && hostOS != "" && (clientOS == "darwin") != (hostOS == "darwin")
}

// SwapCmdCtrl is ev with Ctrl and the GUI key (Cmd, Windows) traded, on
// each side: the key itself and the modifiers it carries.
func SwapCmdCtrl(ev Event) Event {
	if ev.Type != TypeKey {
		return ev
	}
	switch ev.Key.Usage {
	case KeyLeftControl:
		ev.Key.Usage = KeyLeftGUI
	case KeyLeftGUI:
		ev.Key.Usage = KeyLeftControl
	case KeyRightControl:
		ev.Key.Usage = KeyRightGUI
	case KeyRightGUI:
		ev.Key.Usage = KeyRightControl
	}
	m := ev.Key.Modifiers
	const ctrl, gui = ModLeftControl | ModRightControl, ModLeftGUI | ModRightGUI
	ev.Key.Modifiers = m&^(ctrl|gui) | (m&ModLeftControl)<<3 | (m&ModRightControl)<<3 | (m&ModLeftGUI)>>3 | (m&ModRightGUI)>>3
	return ev
}
