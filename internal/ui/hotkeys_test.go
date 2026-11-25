package ui

import (
	"errors"
	"testing"

	mui "github.com/egoist/mygo/ui"
)

func TestChordLabel(t *testing.T) {
	for _, c := range []struct{ goos, chord, want string }{
		{"darwin", "Ctrl+Alt+O", "⌃⌥O"},
		{"darwin", "Ctrl+Alt+Shift+D", "⌃⌥⇧D"},
		{"darwin", "Ctrl+Alt+Backspace", "⌃⌥⌫"},
		{"darwin", "Super+Shift+5", "⇧⌘5"},
		{"windows", "Ctrl+Alt+O", "Ctrl Alt O"},
		{"windows", "Ctrl+Alt+]", "Ctrl Alt ]"},
		{"windows", "Super+K", "Win K"},
		{"windows", "", ""},
	} {
		if got := chordLabel(c.goos, c.chord); got != c.want {
			t.Errorf("chordLabel(%s, %q) = %q, want %q", c.goos, c.chord, got, c.want)
		}
	}
}

// TestReboundHotkeyDispatches: the stream follows the chords the user
// bound: the new one acts, the old one no longer does, and an unbound
// hotkey does nothing.
func TestReboundHotkeyDispatches(t *testing.T) {
	f := streamFake()
	if err := f.SetHotkey(HotkeyMetrics, "Ctrl+Alt+J"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetHotkey(HotkeyHideButton, ""); err != nil {
		t.Fatal(err)
	}
	_, tt, _ := dockedLeft(t, f)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	tt.Frame()
	if shows(tt, "Metrics") {
		t.Fatal("the old chord still opens the metrics window")
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyJ)
	tt.Frame()
	if !shows(tt, "Metrics") {
		t.Fatal("the new chord did not open the metrics window")
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyP)
	tt.Frame()
	if !shows(tt, "Show stream controls") {
		t.Fatal("an unbound hotkey hid the overlay")
	}
}

// TestHostActionHotkeys: Ctrl+Alt+Del, keyframe and switch display reach
// the backend and say so; switching on a one-display host says why not.
func TestHostActionHotkeys(t *testing.T) {
	f := streamFake()
	f.switchErr = ErrOneDisplay
	_, tt, _ := dockedLeft(t, f)
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyBackspace)
	tt.Frame()
	if f.ctrlAltDels != 1 || !tt.HasText("Ctrl+Alt+Del sent") {
		t.Errorf("Ctrl+Alt+Del: %d sent, texts %q", f.ctrlAltDels, tt.Texts())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyK)
	tt.Frame()
	if f.keyframes != 1 {
		t.Errorf("%d keyframes asked for, want 1", f.keyframes)
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyBracketRight)
	tt.Frame()
	if f.switches != 1 || !tt.HasText("Display not switched") || !tt.HasText(ErrOneDisplay.Error()) {
		t.Errorf("switch display: %d calls, texts %q", f.switches, tt.Texts())
	}
}

// TestFakeHotkeyRules: the fake keeps config's rules, so UI tests see the
// errors the real backend gives.
func TestFakeHotkeyRules(t *testing.T) {
	f := newFake()
	var he *HotkeyError
	if err := f.SetHotkey(HotkeyMetrics, "Ctrl+Alt+O"); !errors.As(err, &he) || he.Owner != HotkeyStreamMenu || !errors.Is(err, ErrChordInUse) {
		t.Errorf("conflict: %v", err)
	}
	if err := f.AddNeverSend("F13"); err != nil {
		t.Fatal(err)
	}
	if got := f.NeverSend(); len(got) != 1 || got[0] != "F13" {
		t.Errorf("never send %v", got)
	}
}
