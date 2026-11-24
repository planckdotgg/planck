package main

import (
	"errors"
	"os"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/internal/apple"
	"github.com/planckdotgg/planck/pkg/input"
)

func TestMacTapEvents(t *testing.T) {
	const ctrlAlt = cgFlagControl | cgFlagAlternate | 0x0001 | 0x0020
	for _, c := range []struct {
		name  string
		typ   uint32
		code  uint16
		flags uint64
		want  []tapEvent
	}{
		{"Tab under Cmd", cgEventKeyDown, 0x30, cgFlagCommand | 0x0008, []tapEvent{{usage: input.KeyTab, down: true}}},
		{"Tab up", cgEventKeyUp, 0x30, 0, []tapEvent{{usage: input.KeyTab}}},
		{"left Cmd down", cgEventFlagsChanged, 0x37, cgFlagCommand | 0x0008, []tapEvent{{usage: input.KeyLeftGUI, down: true}}},
		{"left Cmd up", cgEventFlagsChanged, 0x37, 0, []tapEvent{{usage: input.KeyLeftGUI}}},
		{"right Cmd up, left still down", cgEventFlagsChanged, 0x36, cgFlagCommand | 0x0008, []tapEvent{{usage: input.KeyRightGUI}}},
		{"Cmd down without device bits", cgEventFlagsChanged, 0x37, cgFlagCommand, []tapEvent{{usage: input.KeyLeftGUI, down: true}}},
		{"right Ctrl down", cgEventFlagsChanged, 0x3E, cgFlagControl | 0x2000, []tapEvent{{usage: input.KeyRightControl, down: true, chord: mui.Ctrl}}},
		{"Ctrl+Alt+I", cgEventKeyDown, 0x22, ctrlAlt, []tapEvent{{usage: input.KeyI, down: true, chord: mui.Ctrl | mui.Alt}}},
		{"Ctrl+Alt+Shift+D", cgEventKeyDown, 0x02, ctrlAlt | cgFlagShift | 0x0002, []tapEvent{{usage: input.KeyD, down: true, chord: mui.Ctrl | mui.Alt | mui.Shift}}},
		{"Caps Lock", cgEventFlagsChanged, macKeyCapsLock, 0x10000, []tapEvent{{usage: input.KeyCapsLock, down: true}, {usage: input.KeyCapsLock}}},
		{"Fn, no HID usage", cgEventFlagsChanged, 0x3F, 0x800000, nil},
	} {
		evs, n := macTapEvents(c.typ, c.code, c.flags)
		got := evs[:n]
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			}
		}
	}
}

// TestMacTapNeedsAccessibility: without the permission the tap is not
// made, and the reason says where to grant it.
func TestMacTapNeedsAccessibility(t *testing.T) {
	tap := &macTap{trusted: func() bool { return false }}
	err := tap.start(func(tapEvent) bool { t.Fatal("an event reached a tap never made"); return false })
	var denied errTapDenied
	if !errors.As(err, &denied) || denied.hint != macAccessibilityHint {
		t.Fatalf("start: %v, want the Accessibility hint", err)
	}
	if curMacTap.Load() != nil {
		t.Fatal("a refused tap was left current")
	}
	r := newSysRig()
	r.sys.tap = tap
	r.sys.setEnabled(true)
	if r.sys.frame(true) || r.sys.SystemKeysProblem() != macAccessibilityHint {
		t.Fatalf("captured %v with problem %q", r.sys.capturing(), r.sys.SystemKeysProblem())
	}
}

// TestMacTapHandle runs the tap's callback on keyboard events made, never
// posted: the host's own events pass untouched, a captured one is kept
// from the OS, and a tap macOS turned off is not turned on once stopping.
func TestMacTapHandle(t *testing.T) {
	loadTapOnce.Do(loadTap)
	var (
		create  func(source uintptr, key uint16, down bool) uintptr
		setFlag func(ev uintptr, flags uint64)
		setInt  func(ev uintptr, field uint32, v int64)
	)
	apple.Register(&create, apple.CoreGraphics, "CGEventCreateKeyboardEvent")
	apple.Register(&setFlag, apple.CoreGraphics, "CGEventSetFlags")
	apple.Register(&setInt, apple.CoreGraphics, "CGEventSetIntegerValueField")
	tab := create(0, 0x30, true)
	defer apple.CFRelease(tab)
	setFlag(tab, cgFlagCommand|0x0008)

	var seen []tapEvent
	tap := &macTap{on: func(e tapEvent) bool { seen = append(seen, e); return true }}
	if got := tap.handle(cgEventKeyDown, tab); got != 0 {
		t.Fatal("a captured Cmd-Tab reached the OS")
	}
	if len(seen) != 1 || seen[0] != (tapEvent{usage: input.KeyTab, down: true}) {
		t.Fatalf("the hook saw %+v", seen)
	}
	setInt(tab, cgFieldSourceUserData, input.InjectMarker)
	if got := tap.handle(cgEventKeyDown, tab); got != tab || len(seen) != 1 {
		t.Fatal("the host's own event was captured")
	}
	tap.stopping.Store(true)
	if got := tap.handle(cgTapDisabledTimeout, tab); got != tab {
		t.Fatal("a disabled notice was swallowed")
	}
}

// TestMacTapLive installs the real tap, letting every event through, and
// takes it out again. It needs PLANCK_LIVE_INPUT_TEST=1 and the
// Accessibility permission.
func TestMacTapLive(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_INPUT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_INPUT_TEST=1")
	}
	tap := newKeyTap(nil).(*macTap)
	if err := tap.start(func(tapEvent) bool { return false }); err != nil {
		var denied errTapDenied
		if errors.As(err, &denied) {
			t.Skipf("no tap: %v", err)
		}
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		tap.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the tap did not stop")
	}
	if curMacTap.Load() != nil {
		t.Fatal("the stopped tap is still current")
	}
}
