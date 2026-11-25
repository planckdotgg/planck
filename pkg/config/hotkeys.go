package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/planckdotgg/planck/pkg/input"
)

// Mods are a chord's modifier keys, without their side.
type Mods uint8

const (
	ModCtrl Mods = 1 << iota
	ModAlt
	ModShift
	// ModSuper is Cmd on macOS and the Windows key on Windows.
	ModSuper
)

// Chord is a key with the modifiers held for it. The zero Chord is no key:
// an unbound hotkey.
type Chord struct {
	Mods Mods
	// Key is a HID usage that input.KeyName names.
	Key uint16
}

func (c Chord) IsZero() bool { return c.Key == 0 }

// String writes c as ParseChord reads it: "Ctrl+Alt+Shift+Super+K", the
// modifiers in that order, and "" for the zero Chord.
func (c Chord) String() string {
	if c.IsZero() {
		return ""
	}
	var b strings.Builder
	for _, m := range chordMods {
		if c.Mods&m.mod != 0 {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(input.KeyName(c.Key))
	return b.String()
}

var chordMods = []struct {
	mod  Mods
	name string
}{{ModCtrl, "Ctrl"}, {ModAlt, "Alt"}, {ModShift, "Shift"}, {ModSuper, "Super"}}

var modNames = map[string]Mods{
	"ctrl": ModCtrl, "control": ModCtrl,
	"alt": ModAlt, "option": ModAlt,
	"shift": ModShift,
	"super": ModSuper, "cmd": ModSuper, "command": ModSuper, "win": ModSuper, "meta": ModSuper,
}

// ErrBadChord is text ParseChord cannot read.
var ErrBadChord = errors.New("config: not a chord")

// ParseChord reads a chord as "Ctrl+Alt+K": modifiers, in any order and
// case, then one key by its input.KeyName name. Cmd, Win and Meta are
// Super; Option is Alt. "" is the zero Chord.
func ParseChord(s string) (Chord, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Chord{}, nil
	}
	parts := strings.Split(s, "+")
	if strings.HasSuffix(s, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	var c Chord
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if i < len(parts)-1 {
			m, ok := modNames[strings.ToLower(p)]
			if !ok {
				return Chord{}, fmt.Errorf("%w: %q has no modifier %q", ErrBadChord, s, p)
			}
			c.Mods |= m
			continue
		}
		u, ok := input.KeyByName(p)
		if !ok {
			return Chord{}, fmt.Errorf("%w: %q has no key %q", ErrBadChord, s, p)
		}
		c.Key = u
	}
	return c, nil
}

func mustChord(s string) Chord {
	c, err := ParseChord(s)
	if err != nil {
		panic(err)
	}
	return c
}

// HotkeyAction names what a hotkey does.
type HotkeyAction string

// The stream's actions, which work in the stream's window, and while
// immersive mode takes the system's keys, and the host's, which are
// global: they work while Planck is not in front.
const (
	HotkeyStreamMenu    HotkeyAction = "streamMenu"
	HotkeyHideButton    HotkeyAction = "hideButton"
	HotkeyMetrics       HotkeyAction = "metrics"
	HotkeyCycleHUD      HotkeyAction = "cycleHUD"
	HotkeyMicrophone    HotkeyAction = "microphone"
	HotkeyFullscreen    HotkeyAction = "fullscreen"
	HotkeyImmersive     HotkeyAction = "immersive"
	HotkeyCursorLock    HotkeyAction = "cursorLock"
	HotkeySwitchDisplay HotkeyAction = "switchDisplay"
	HotkeyCtrlAltDel    HotkeyAction = "ctrlAltDel"
	HotkeyKeyframe      HotkeyAction = "keyframe"
	HotkeyDisconnect    HotkeyAction = "disconnect"
	HotkeyStopHosting   HotkeyAction = "stopHosting"
	HotkeyKickClient    HotkeyAction = "kickClient"
)

// HotkeyDef is a hotkey's action and its default chord.
type HotkeyDef struct {
	Action  HotkeyAction
	Default Chord
	// Global is a host action, registered with the desktop so that it
	// fires while Planck is not in front.
	Global bool
}

// HotkeyDefs are every hotkey, in the order Settings lists them. The
// stream's keep to Ctrl+Alt so that the Cmd and Windows keys reach the
// host.
var HotkeyDefs = []HotkeyDef{
	{Action: HotkeyStreamMenu, Default: mustChord("Ctrl+Alt+O")},
	{Action: HotkeyHideButton, Default: mustChord("Ctrl+Alt+P")},
	{Action: HotkeyMetrics, Default: mustChord("Ctrl+Alt+M")},
	{Action: HotkeyCycleHUD, Default: mustChord("Ctrl+Alt+H")},
	{Action: HotkeyMicrophone, Default: mustChord("Ctrl+Alt+U")},
	{Action: HotkeyFullscreen, Default: mustChord("Ctrl+Alt+F")},
	{Action: HotkeyImmersive, Default: mustChord("Ctrl+Alt+I")},
	{Action: HotkeyCursorLock, Default: mustChord("Ctrl+Alt+G")},
	{Action: HotkeySwitchDisplay, Default: mustChord("Ctrl+Alt+]")},
	{Action: HotkeyCtrlAltDel, Default: mustChord("Ctrl+Alt+Backspace")},
	{Action: HotkeyKeyframe, Default: mustChord("Ctrl+Alt+K")},
	{Action: HotkeyDisconnect, Default: mustChord("Ctrl+Alt+Shift+D")},
	{Action: HotkeyStopHosting, Default: mustChord("Ctrl+F3"), Global: true},
	{Action: HotkeyKickClient, Global: true},
}

func hotkeyDef(a HotkeyAction) (HotkeyDef, bool) {
	i := slices.IndexFunc(HotkeyDefs, func(d HotkeyDef) bool { return d.Action == a })
	if i < 0 {
		return HotkeyDef{}, false
	}
	return HotkeyDefs[i], true
}

// Why SetHotkey or AddNeverSend refused a chord; a *HotkeyError wraps one.
var (
	ErrUnknownHotkey = errors.New("config: no such hotkey")
	ErrNoModifier    = errors.New("needs at least one modifier")
	ErrReservedChord = errors.New("the system keeps this chord")
	ErrChordInUse    = errors.New("already in use")
)

// HotkeyError is a chord refused for a hotkey or the never-send list.
type HotkeyError struct {
	Chord Chord
	Err   error
	// Owner is the hotkey that has the chord, for ErrChordInUse; empty
	// when the never-send list has it.
	Owner HotkeyAction
	// Reason says what the system does with a reserved chord.
	Reason string
}

func (e *HotkeyError) Error() string {
	switch {
	case e.Err == ErrChordInUse && e.Owner != "":
		return fmt.Sprintf("%s is %s by %s", e.Chord, e.Err, e.Owner)
	case e.Err == ErrChordInUse:
		return fmt.Sprintf("%s is %s: it is never sent to the host", e.Chord, e.Err)
	case e.Reason != "":
		return fmt.Sprintf("%s: %s", e.Chord, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Chord, e.Err)
}

func (e *HotkeyError) Unwrap() error { return e.Err }

// Hotkey is a's chord: the user's, or the default. It is zero while a is
// unbound.
func (c ClientConfig) Hotkey(a HotkeyAction) Chord {
	if s, ok := c.Hotkeys[string(a)]; ok {
		ch, _ := ParseChord(s)
		return ch
	}
	d, _ := hotkeyDef(a)
	return d.Default
}

// HotkeyChords are the bound hotkeys' chords.
func (c ClientConfig) HotkeyChords() map[HotkeyAction]Chord {
	out := make(map[HotkeyAction]Chord, len(HotkeyDefs))
	for _, d := range HotkeyDefs {
		if ch := c.Hotkey(d.Action); !ch.IsZero() {
			out[d.Action] = ch
		}
	}
	return out
}

// NeverSendChords are the chords the client keeps from the host.
func (c ClientConfig) NeverSendChords() []Chord {
	out := make([]Chord, 0, len(c.NeverSend))
	for _, s := range c.NeverSend {
		if ch, err := ParseChord(s); err == nil && !ch.IsZero() {
			out = append(out, ch)
		}
	}
	return out
}

// SetHotkey binds a to chord, or with "" unbinds it. It refuses a chord
// without a modifier, one the system keeps on goos, one another hotkey
// has, and one on the never-send list.
func (c *ClientConfig) SetHotkey(a HotkeyAction, chord, goos string) error {
	d, ok := hotkeyDef(a)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHotkey, a)
	}
	ch, err := ParseChord(chord)
	if err != nil {
		return err
	}
	if !ch.IsZero() {
		if err := c.checkHotkey(a, ch, goos); err != nil {
			return err
		}
	}
	if ch == d.Default {
		delete(c.Hotkeys, string(a))
		return nil
	}
	if c.Hotkeys == nil {
		c.Hotkeys = map[string]string{}
	}
	c.Hotkeys[string(a)] = ch.String()
	return nil
}

// ResetHotkey binds a to its default chord, unless another hotkey has
// taken that chord since.
func (c *ClientConfig) ResetHotkey(a HotkeyAction, goos string) error {
	d, ok := hotkeyDef(a)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownHotkey, a)
	}
	return c.SetHotkey(a, d.Default.String(), goos)
}

// ResetAllHotkeys binds every hotkey to its default chord.
func (c *ClientConfig) ResetAllHotkeys() { c.Hotkeys = nil }

func (c *ClientConfig) checkHotkey(a HotkeyAction, ch Chord, goos string) error {
	if ch.Mods == 0 {
		return &HotkeyError{Chord: ch, Err: ErrNoModifier}
	}
	if reason := ReservedChord(ch, goos); reason != "" {
		return &HotkeyError{Chord: ch, Err: ErrReservedChord, Reason: reason}
	}
	for _, d := range HotkeyDefs {
		if d.Action != a && c.Hotkey(d.Action) == ch {
			return &HotkeyError{Chord: ch, Err: ErrChordInUse, Owner: d.Action}
		}
	}
	if slices.Contains(c.NeverSendChords(), ch) {
		return &HotkeyError{Chord: ch, Err: ErrChordInUse}
	}
	return nil
}

// AddNeverSend keeps chord from the host from now on, even while
// immersive mode takes the system's keys. A bare key, such as F13, is a
// chord here. It refuses a hotkey's chord, which never reaches the host
// already.
func (c *ClientConfig) AddNeverSend(chord string) error {
	ch, err := ParseChord(chord)
	if err != nil {
		return err
	}
	if ch.IsZero() {
		return fmt.Errorf("%w: empty", ErrBadChord)
	}
	for a, have := range c.HotkeyChords() {
		if have == ch {
			return &HotkeyError{Chord: ch, Err: ErrChordInUse, Owner: a}
		}
	}
	if !slices.Contains(c.NeverSendChords(), ch) {
		c.NeverSend = append(c.NeverSend, ch.String())
	}
	return nil
}

// RemoveNeverSend sends chord to the host again.
func (c *ClientConfig) RemoveNeverSend(chord string) error {
	ch, err := ParseChord(chord)
	if err != nil {
		return err
	}
	c.NeverSend = slices.DeleteFunc(c.NeverSend, func(s string) bool {
		have, err := ParseChord(s)
		return err == nil && have == ch
	})
	return nil
}

type reservedChord struct {
	chord  Chord
	reason string
}

var reservedChords = map[string][]reservedChord{
	"darwin": {
		{mustChord("Super+Tab"), "macOS switches apps with it"},
		{mustChord("Super+Shift+Tab"), "macOS switches apps with it"},
		{mustChord("Super+Space"), "macOS opens Spotlight with it"},
		{mustChord("Ctrl+Space"), "macOS switches input sources with it"},
		{mustChord("Ctrl+Super+Space"), "macOS opens the character viewer with it"},
		{mustChord("Super+Q"), "macOS quits Planck with it"},
		{mustChord("Super+H"), "macOS hides Planck with it"},
		{mustChord("Super+M"), "macOS minimizes the window with it"},
		{mustChord("Ctrl+Super+Q"), "macOS locks the screen with it"},
		{mustChord("Alt+Super+Escape"), "macOS opens Force Quit with it"},
		{mustChord("Super+Shift+3"), "macOS takes a screenshot with it"},
		{mustChord("Super+Shift+4"), "macOS takes a screenshot with it"},
		{mustChord("Super+Shift+5"), "macOS opens the screenshot tools with it"},
		{mustChord("Ctrl+Up"), "macOS opens Mission Control with it"},
		{mustChord("Ctrl+Down"), "macOS shows the app's windows with it"},
		{mustChord("Ctrl+Left"), "macOS moves between Spaces with it"},
		{mustChord("Ctrl+Right"), "macOS moves between Spaces with it"},
	},
	"windows": {
		{mustChord("Ctrl+Alt+Delete"), "Windows opens its security screen with it"},
		{mustChord("Ctrl+Shift+Escape"), "Windows opens Task Manager with it"},
		{mustChord("Alt+Tab"), "Windows switches apps with it"},
		{mustChord("Alt+Shift+Tab"), "Windows switches apps with it"},
		{mustChord("Alt+F4"), "Windows closes the window with it"},
		{mustChord("Alt+Escape"), "Windows cycles windows with it"},
		{mustChord("Ctrl+Escape"), "Windows opens Start with it"},
		{mustChord("Alt+Space"), "Windows opens the window menu with it"},
	},
}

// ReservedChord is why the system on goos keeps ch for itself, "" when it
// does not. Windows keeps every chord with the Windows key.
func ReservedChord(ch Chord, goos string) string {
	if goos == "windows" && ch.Mods&ModSuper != 0 {
		return "Windows keeps the Windows key's shortcuts"
	}
	for _, r := range reservedChords[goos] {
		if r.chord == ch {
			return r.reason
		}
	}
	return ""
}

func (c ClientConfig) validateHotkeys() error {
	for a, s := range c.Hotkeys {
		if _, err := ParseChord(s); err != nil {
			return fmt.Errorf("config: client: hotkey %s: %w", a, err)
		}
	}
	for _, s := range c.NeverSend {
		if _, err := ParseChord(s); err != nil {
			return fmt.Errorf("config: client: neverSend: %w", err)
		}
	}
	return nil
}
