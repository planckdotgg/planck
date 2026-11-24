package main

import (
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"

	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/input"
)

func TestWinTapEvent(t *testing.T) {
	for _, c := range []struct {
		name string
		kb   kbdLLHook
		want tapEvent
		ok   bool
	}{
		{"left Windows key down", kbdLLHook{vk: 0x5B, flags: llkhfExtended}, tapEvent{usage: input.KeyLeftGUI, down: true}, true},
		{"right Windows key up", kbdLLHook{vk: 0x5C, flags: llkhfExtended | llkhfUp}, tapEvent{usage: input.KeyRightGUI}, true},
		{"left Alt down", kbdLLHook{vk: 0xA4}, tapEvent{usage: input.KeyLeftAlt, down: true}, true},
		{"right Ctrl up", kbdLLHook{vk: 0xA3, flags: llkhfExtended | llkhfUp}, tapEvent{usage: input.KeyRightControl}, true},
		{"Tab", kbdLLHook{vk: 0x09}, tapEvent{usage: input.KeyTab, down: true, chord: mui.Alt}, true},
		{"keypad Enter", kbdLLHook{vk: 0x0D, flags: llkhfExtended}, tapEvent{usage: input.KeyKPEnter, down: true}, true},
		{"no HID usage", kbdLLHook{vk: 0xFF}, tapEvent{}, false},
	} {
		chord := c.want.chord
		got, ok := winTapEvent(&c.kb, chord)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestWinHookLeavesOthersAlone: the host's own injected keys, and keys
// while another window has the foreground, pass untouched.
func TestWinHookLeavesOthersAlone(t *testing.T) {
	h := &winHook{hwnd: 1, on: func(tapEvent) bool { t.Fatal("the hook took a key it must leave"); return true }}
	if h.handle(&kbdLLHook{vk: 0x5B, flags: llkhfExtended | llkhfInjected, extraInfo: input.InjectMarker}) {
		t.Fatal("kept the host's own key")
	}
	if h.handle(&kbdLLHook{vk: 0x5B, flags: llkhfExtended}) {
		t.Fatal("kept a key typed into another window")
	}
}

// TestWinHookLive installs the real hook for the window in the
// foreground and sends F23 through it, which it keeps; F23 marked as the
// host's goes by. It needs PLANCK_LIVE_INPUT_TEST=1.
func TestWinHookLive(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_INPUT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_INPUT_TEST=1")
	}
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == 0 {
		t.Skip("no foreground window")
	}
	var mu sync.Mutex
	var seen []tapEvent
	h := &winHook{window: func() uintptr { return fg }}
	err := h.start(func(e tapEvent) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e)
		return e.usage == input.KeyF23
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.stop()
	send := func(up bool, extra uintptr) {
		var in [40]byte
		*(*uint32)(unsafe.Pointer(&in[0])) = 1 // INPUT_KEYBOARD
		*(*uint16)(unsafe.Pointer(&in[8])) = 0x86
		if up {
			*(*uint32)(unsafe.Pointer(&in[12])) = 0x0002
		}
		*(*uintptr)(unsafe.Pointer(&in[24])) = extra
		if n, _, err := user32.NewProc("SendInput").Call(1, uintptr(unsafe.Pointer(&in[0])), 40); n != 1 {
			t.Fatalf("SendInput: %v", err)
		}
	}
	send(false, 0)
	send(true, 0)
	send(false, input.InjectMarker)
	send(true, input.InjectMarker)
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	want := []tapEvent{{usage: input.KeyF23, down: true}, {usage: input.KeyF23}}
	if len(seen) != 2 || seen[0].usage != want[0].usage || !seen[0].down || seen[1].down {
		t.Fatalf("the hook saw %+v, want F23 down and up once each", seen)
	}
}
