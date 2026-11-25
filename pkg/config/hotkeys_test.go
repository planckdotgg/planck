package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
)

func TestParseChord(t *testing.T) {
	for in, want := range map[string]Chord{
		"Ctrl+Alt+O":         {ModCtrl | ModAlt, input.KeyO},
		"alt+ctrl+o":         {ModCtrl | ModAlt, input.KeyO},
		"Cmd+Shift+5":        {ModSuper | ModShift, input.Key5},
		"Option+Shift+Q":     {ModAlt | ModShift, input.KeyQ},
		"F13":                {Key: input.KeyF13},
		"Ctrl+Alt+]":         {ModCtrl | ModAlt, input.KeyRightBracket},
		"Ctrl+Alt+Backspace": {ModCtrl | ModAlt, input.KeyBackspace},
		"":                   {},
	} {
		got, err := ParseChord(in)
		if err != nil || got != want {
			t.Errorf("ParseChord(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"Ctrl+", "Hyper+K", "Ctrl+Alt+Nope", "Ctrl+Shift"} {
		if _, err := ParseChord(bad); !errors.Is(err, ErrBadChord) {
			t.Errorf("ParseChord(%q) = %v, want ErrBadChord", bad, err)
		}
	}
	for _, s := range []string{"Ctrl+Alt+Shift+Super+K", "Ctrl+F3", "F13", "Ctrl+Alt+]"} {
		if got := mustChord(s).String(); got != s {
			t.Errorf("String of %q = %q", s, got)
		}
	}
}

func TestHotkeyDefaultsAreDistinctAndAllowed(t *testing.T) {
	seen := map[Chord]HotkeyAction{}
	for _, d := range HotkeyDefs {
		if d.Default.IsZero() {
			continue
		}
		if other, ok := seen[d.Default]; ok {
			t.Errorf("%s and %s both default to %s", d.Action, other, d.Default)
		}
		seen[d.Default] = d.Action
		for _, goos := range []string{"darwin", "windows"} {
			if r := ReservedChord(d.Default, goos); r != "" {
				t.Errorf("%s defaults to %s, which %s reserves: %s", d.Action, d.Default, goos, r)
			}
		}
	}
	c := DefaultClientConfig()
	if got := c.Hotkey(HotkeyHideButton); got != mustChord("Ctrl+Alt+P") {
		t.Errorf("hide button = %s", got)
	}
	if got := c.Hotkey(HotkeyCycleHUD); got != mustChord("Ctrl+Alt+H") {
		t.Errorf("cycle HUD = %s", got)
	}
	if _, bound := c.HotkeyChords()[HotkeyKickClient]; bound {
		t.Error("kick client is bound by default")
	}
}

func TestSetHotkeyRefuses(t *testing.T) {
	c := DefaultClientConfig()
	cases := []struct {
		action      HotkeyAction
		chord, goos string
		want        error
		owner       HotkeyAction
	}{
		{HotkeyMetrics, "K", "darwin", ErrNoModifier, ""},
		{HotkeyMetrics, "Ctrl+Alt+O", "darwin", ErrChordInUse, HotkeyStreamMenu},
		{HotkeyMetrics, "Super+Shift+5", "darwin", ErrReservedChord, ""},
		{HotkeyMetrics, "Super+K", "windows", ErrReservedChord, ""},
		{HotkeyMetrics, "Alt+Tab", "windows", ErrReservedChord, ""},
		{"nope", "Ctrl+Alt+J", "darwin", ErrUnknownHotkey, ""},
	}
	for _, tc := range cases {
		err := c.SetHotkey(tc.action, tc.chord, tc.goos)
		if !errors.Is(err, tc.want) {
			t.Errorf("SetHotkey(%s, %q, %s) = %v, want %v", tc.action, tc.chord, tc.goos, err, tc.want)
			continue
		}
		var he *HotkeyError
		if errors.As(err, &he) && he.Owner != tc.owner {
			t.Errorf("SetHotkey(%s, %q) owner %q, want %q", tc.action, tc.chord, he.Owner, tc.owner)
		}
	}
	if len(c.Hotkeys) != 0 {
		t.Errorf("refused chords were kept: %v", c.Hotkeys)
	}
	if err := c.SetHotkey(HotkeyMetrics, "Super+K", "darwin"); err != nil {
		t.Errorf("Cmd+K on macOS: %v", err)
	}
}

func TestSetResetHotkeys(t *testing.T) {
	c := DefaultClientConfig()
	if err := c.SetHotkey(HotkeyStreamMenu, "Ctrl+Alt+J", "darwin"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHotkey(HotkeyMetrics, "Ctrl+Alt+O", "darwin"); err != nil {
		t.Fatalf("taking the menu's old chord: %v", err)
	}
	if err := c.ResetHotkey(HotkeyStreamMenu, "darwin"); !errors.Is(err, ErrChordInUse) {
		t.Errorf("reset onto a chord Metrics took = %v, want ErrChordInUse", err)
	}
	if err := c.SetHotkey(HotkeyDisconnect, "", "darwin"); err != nil {
		t.Fatal(err)
	}
	if !c.Hotkey(HotkeyDisconnect).IsZero() {
		t.Error("disconnect is still bound")
	}
	if err := c.SetHotkey(HotkeyKickClient, "Ctrl+F4", "windows"); err != nil {
		t.Fatal(err)
	}
	if err := SaveClientConfig(t.TempDir(), c); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := SaveClientConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	back, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []HotkeyAction{HotkeyStreamMenu, HotkeyMetrics, HotkeyDisconnect, HotkeyKickClient, HotkeyFullscreen} {
		if back.Hotkey(a) != c.Hotkey(a) {
			t.Errorf("%s = %s after a save, want %s", a, back.Hotkey(a), c.Hotkey(a))
		}
	}
	if err := back.SetHotkey(HotkeyMetrics, "Ctrl+Alt+M", "darwin"); err != nil {
		t.Fatal(err)
	}
	if _, kept := back.Hotkeys[string(HotkeyMetrics)]; kept {
		t.Error("a hotkey set back to its default is still stored")
	}
	back.ResetAllHotkeys()
	if back.Hotkey(HotkeyStreamMenu) != mustChord("Ctrl+Alt+O") || !back.Hotkey(HotkeyKickClient).IsZero() {
		t.Errorf("after reset all: menu %s, kick %s", back.Hotkey(HotkeyStreamMenu), back.Hotkey(HotkeyKickClient))
	}
}

func TestNeverSend(t *testing.T) {
	c := DefaultClientConfig()
	for _, ch := range []string{"Alt+Shift+Q", "F13", "option+shift+q"} {
		if err := c.AddNeverSend(ch); err != nil {
			t.Fatalf("AddNeverSend(%q): %v", ch, err)
		}
	}
	if !slices.Equal(c.NeverSend, []string{"Alt+Shift+Q", "F13"}) {
		t.Errorf("NeverSend = %v", c.NeverSend)
	}
	var he *HotkeyError
	if err := c.AddNeverSend("Ctrl+Alt+M"); !errors.As(err, &he) || he.Owner != HotkeyMetrics {
		t.Errorf("never sending a hotkey's chord = %v", err)
	}
	if err := c.SetHotkey(HotkeyMetrics, "Alt+Shift+Q", "darwin"); !errors.Is(err, ErrChordInUse) {
		t.Errorf("binding a never-sent chord = %v, want ErrChordInUse", err)
	}
	if err := c.RemoveNeverSend("ALT+SHIFT+Q"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.NeverSend, []string{"F13"}) {
		t.Errorf("after remove NeverSend = %v", c.NeverSend)
	}
	c.NeverSend = append(c.NeverSend, "Ctrl+Nope")
	if err := c.Validate(); !errors.Is(err, ErrBadChord) {
		t.Errorf("Validate with a bad chord = %v", err)
	}
}

func TestHostQoLValidate(t *testing.T) {
	c := DefaultHostConfig(t.TempDir())
	c.IdleTimeoutMinutes = -1
	if c.Validate() == nil {
		t.Error("a negative idle timeout passed")
	}
	c.IdleTimeoutMinutes = 30
	c.StayAwake, c.LockOnDisconnect, c.MuteSpeakers, c.PrivacyMode = true, true, true, true
	c.AudioExcludeApps, c.VirtualMic = NewAppList("Discord", " discord ", "Teams"), VirtualMicOff
	dir := t.TempDir()
	if err := SaveHostConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, HostConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"audioExcludeApps": [`) {
		t.Errorf("audioExcludeApps is not a JSON array:\n%s", raw)
	}
	back, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !back.StayAwake || !back.LockOnDisconnect || !back.MuteSpeakers || !back.PrivacyMode || back.IdleTimeoutMinutes != 30 ||
		!slices.Equal(back.AudioExcludeApps.Names(), []string{"Discord", "Teams"}) || back.VirtualMic != VirtualMicOff {
		t.Errorf("host QoL after a save: %+v", back)
	}
}
