package ui

import (
	"slices"
	"testing"
	"time"
)

type fakeSysKeys struct{ problem string }

func (f *fakeSysKeys) SystemKeysProblem() string { return f.problem }

// TestSystemKeysProblemShown: when immersive mode cannot take the system's
// keys, a toast says so once, and the menu's Immersive row keeps saying
// why.
func TestSystemKeysProblemShown(t *testing.T) {
	f := streamFake()
	f.settings.SystemKeysCapture = true
	a, tt, _ := newStreamTester(t, f)
	sk := &fakeSysKeys{}
	a.SetSystemKeys(sk)
	tt.Frame()
	title := "System keys stay on this " + machineWord(a.goos)
	if tt.HasText(title) {
		t.Fatal("a toast with no problem")
	}
	hint := "Allow Planck in System Settings › Privacy & Security › Accessibility"
	sk.problem = hint
	tt.Frame()
	if !tt.HasText(title) || !tt.HasText(hint) {
		t.Fatalf("no toast for the problem: %q", tt.Texts())
	}
	tt.Advance(toastIn)
	tt.Frame()
	snapshot(t, tt, "syskeys-toast")
	tt.Advance(10 * time.Second)
	tt.Frame()
	tt.Frame()
	if tt.HasText(title) {
		t.Fatal("the toast came back for the same problem")
	}
	openMenu(t, tt)
	if !slices.Contains(tt.Texts(), hint) {
		t.Fatalf("the Immersive row does not say why: %q", tt.Texts())
	}
}
