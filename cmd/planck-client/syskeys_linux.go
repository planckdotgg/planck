package main

import (
	"errors"
	"sync"

	"github.com/egoist/mygo"
	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/input"
)

// linuxTap captures the system's keys through the window's seat. On
// Wayland the compositor's shortcuts inhibitor passes them to the window,
// and the seat's own wl_keyboard reports them; on X11 GTK's keyboard grab
// keeps them from the window manager, and XInput 2 raw key events report
// them. Either way the window still sees every key, so on's verdict only
// says which keys the stream view must leave alone.
type linuxTap struct {
	win func() *mygo.Window

	mu      sync.Mutex
	s       *seat
	grabbed bool
}

func newKeyTap(win func() *mygo.Window) keyTap { return &linuxTap{win: win} }

func (t *linuxTap) start(on func(tapEvent) bool) error {
	var err error
	mygo.RunOnMain(func() { err = t.startMain(on) })
	return err
}

func (t *linuxTap) startMain(on func(tapEvent) bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.win()
	if w == nil || w.IsDestroyed() || w.NativeHandle() == 0 {
		return errors.New("no stream window")
	}
	s, err := seatFor(w.NativeHandle())
	if err != nil {
		return err
	}
	keys := func(code uint16, down bool) {
		var mods uint32
		if s.x != nil {
			mods = s.x.mods()
		} else {
			mods = s.mods.Load()
		}
		on(tapEvent{usage: hidFromEvdev(code), down: down, chord: xkbChord(mods)})
	}
	if s.x == nil {
		if err := s.inhibit(true); err != nil {
			return err
		}
	} else {
		if err := loadGDK(); err != nil {
			return err
		}
		// GDK_SEAT_CAPABILITY_KEYBOARD, no owner events.
		seat := ccall(gdk.displayDefaultSeat, ccall(gdk.displayGetDefault))
		if status := ccall(gdk.seatGrab, seat, s.native.GdkWindow, 8, 0, 0, 0, 0, 0); int32(status) != 0 {
			return errTapDenied{hint: "Another app holds the keyboard."}
		}
		t.grabbed = true
	}
	s.keys.Store(&keys)
	t.s = s
	return nil
}

func (t *linuxTap) stop() {
	mygo.RunOnMain(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.s == nil {
			return
		}
		t.s.keys.Store(nil)
		if t.s.x == nil {
			t.s.inhibit(false)
		}
		if t.grabbed && loadGDK() == nil {
			ccall(gdk.seatUngrab, ccall(gdk.displayDefaultSeat, ccall(gdk.displayGetDefault)))
			t.grabbed = false
		}
		t.s = nil
	})
}

func xkbChord(xkb uint32) mui.Modifiers {
	var m mui.Modifiers
	if xkb&xkbShift != 0 {
		m |= mui.Shift
	}
	if xkb&xkbControl != 0 {
		m |= mui.Ctrl
	}
	if xkb&xkbMod1 != 0 {
		m |= mui.Alt
	}
	return m
}

// hidFromEvdev is the HID usage of a Linux key code (input-event-codes.h),
// 0 for a key the host has none for.
func hidFromEvdev(code uint16) uint16 {
	if int(code) < len(evdevHID) {
		return evdevHID[code]
	}
	return 0
}

var evdevHID = func() [256]uint16 {
	var t [256]uint16
	letters := "qwertyuiop"
	for i := range letters {
		t[16+i] = input.KeyA + uint16(letters[i]-'a')
	}
	for i, c := range "asdfghjkl" {
		t[30+i] = input.KeyA + uint16(c-'a')
	}
	for i, c := range "zxcvbnm" {
		t[44+i] = input.KeyA + uint16(c-'a')
	}
	for i := range 9 {
		t[2+i] = input.Key1 + uint16(i)
	}
	t[11] = input.Key0
	for i := range 10 {
		t[59+i] = input.KeyF1 + uint16(i)
	}
	for i := range 12 {
		t[183+i] = input.KeyF13 + uint16(i)
	}
	for code, u := range map[uint16]uint16{
		1: input.KeyEscape, 12: input.KeyMinus, 13: input.KeyEqual, 14: input.KeyBackspace, 15: input.KeyTab,
		26: input.KeyLeftBracket, 27: input.KeyRightBracket, 28: input.KeyEnter, 29: input.KeyLeftControl,
		39: input.KeySemicolon, 40: input.KeyQuote, 41: input.KeyGrave, 42: input.KeyLeftShift, 43: input.KeyBackslash,
		51: input.KeyComma, 52: input.KeyPeriod, 53: input.KeySlash, 54: input.KeyRightShift, 55: input.KeyKPMultiply,
		56: input.KeyLeftAlt, 57: input.KeySpace, 58: input.KeyCapsLock, 69: input.KeyNumLock, 70: input.KeyScrollLock,
		71: input.KeyKP7, 72: input.KeyKP8, 73: input.KeyKP9, 74: input.KeyKPMinus, 75: input.KeyKP4, 76: input.KeyKP5,
		77: input.KeyKP6, 78: input.KeyKPPlus, 79: input.KeyKP1, 80: input.KeyKP2, 81: input.KeyKP3, 82: input.KeyKP0,
		83: input.KeyKPDecimal, 86: input.KeyNonUSBackslash, 87: input.KeyF11, 88: input.KeyF12, 89: input.KeyInternational1,
		92: input.KeyInternational4, 94: input.KeyInternational5, 96: input.KeyKPEnter, 97: input.KeyRightControl,
		98: input.KeyKPDivide, 99: input.KeyPrintScreen, 100: input.KeyRightAlt, 102: input.KeyHome, 103: input.KeyUp,
		104: input.KeyPageUp, 105: input.KeyLeft, 106: input.KeyRight, 107: input.KeyEnd, 108: input.KeyDown,
		109: input.KeyPageDown, 110: input.KeyInsert, 111: input.KeyDelete, 113: input.KeyMute, 114: input.KeyVolumeDown,
		115: input.KeyVolumeUp, 116: input.KeyPower, 117: input.KeyKPEqual, 119: input.KeyPause, 121: input.KeyKPComma,
		122: input.KeyLang1, 123: input.KeyLang2, 124: input.KeyInternational3, 125: input.KeyLeftGUI,
		126: input.KeyRightGUI, 127: input.KeyApplication,
	} {
		t[code] = u
	}
	return t
}()
