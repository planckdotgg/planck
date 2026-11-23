package main

import (
	"slices"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// inputForwarder turns what the stream view sees, the keys held and the
// pointer over it, into input events for the host. It runs in the view's
// build, on the main thread.
type inputForwarder struct {
	send func(input.Event)

	keys    []mui.Key
	mods    uint8
	over    bool
	x, y    uint16
	button  int
	pressed bool
}

// view builds the stream's input surface, which fills the stream view.
func (f *inputForwarder) view(c *mui.Context) {
	c.Key("planck-stream-input")
	el := mui.Box(c).Fill().Label("Stream")
	f.keyboard(c.KeysDown(), c.Modifiers())
	x, y, over := el.PointerPosition()
	b := el.Bounds()
	if over && b.W > 0 && b.H > 0 {
		nx, ny := normalize(x, b.W), normalize(y, b.H)
		if !f.over || nx != f.x || ny != f.y {
			f.send(input.Event{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: nx, Y: ny}})
		}
		f.x, f.y = nx, ny
	}
	f.over = over
	btn, pressed := el.PressedButton()
	if f.pressed && (!pressed || btn != f.button) {
		f.sendButton(f.button, false)
	}
	if pressed && (!f.pressed || btn != f.button) {
		f.sendButton(btn, true)
	}
	f.button, f.pressed = btn, pressed
}

// normalize maps a position along a side of length n onto 0..65535.
func normalize(v, n float32) uint16 {
	switch {
	case v <= 0:
		return 0
	case v >= n:
		return 65535
	}
	return uint16(v / n * 65535)
}

func (f *inputForwarder) sendButton(b int, pressed bool) {
	var hb input.Button
	switch b {
	case 0:
		hb = input.ButtonLeft
	case 1:
		hb = input.ButtonRight
	case 2:
		hb = input.ButtonMiddle
	default:
		return
	}
	f.send(input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: hb, Pressed: pressed}})
}

// hudChord is the HUD hotkey's modifiers; its key stays with the client.
const hudChord = mui.Ctrl | mui.Alt

// keyboard sends the modifiers and keys that changed since the last
// frame, modifiers first going down and last coming up.
func (f *inputForwarder) keyboard(held []mui.Key, m mui.Modifiers) {
	mods := hidModifiers(m)
	for _, mk := range modifierKeys {
		if mods&mk.bit != 0 && f.mods&mk.bit == 0 {
			f.key(mk.usage, true, f.mods|mk.bit)
			f.mods |= mk.bit
		}
	}
	for _, k := range f.keys {
		if !slices.Contains(held, k) {
			f.sendKey(k, false)
		}
	}
	for _, k := range held {
		if !slices.Contains(f.keys, k) && !(k == mui.KeyH && m&hudChord == hudChord) {
			f.sendKey(k, true)
		}
	}
	f.keys = append(f.keys[:0], held...)
	if m&hudChord == hudChord {
		f.keys = slices.DeleteFunc(f.keys, func(k mui.Key) bool { return k == mui.KeyH })
	}
	for _, mk := range modifierKeys {
		if mods&mk.bit == 0 && f.mods&mk.bit != 0 {
			f.mods &^= mk.bit
			f.key(mk.usage, false, f.mods)
		}
	}
}

func (f *inputForwarder) sendKey(k mui.Key, pressed bool) {
	if u, ok := hidUsage[k]; ok {
		f.key(u, pressed, f.mods)
	}
}

func (f *inputForwarder) key(usage uint16, pressed bool, mods uint8) {
	f.send(input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: usage, Pressed: pressed, Modifiers: mods}})
}

// MyGo reports modifiers without their side, so they go as the left keys.
var modifierKeys = []struct {
	bit   uint8
	usage uint16
}{
	{input.ModLeftControl, input.KeyLeftControl},
	{input.ModLeftShift, input.KeyLeftShift},
	{input.ModLeftAlt, input.KeyLeftAlt},
	{input.ModLeftGUI, input.KeyLeftGUI},
}

func hidModifiers(m mui.Modifiers) uint8 {
	var b uint8
	if m&mui.Ctrl != 0 {
		b |= input.ModLeftControl
	}
	if m&mui.Shift != 0 {
		b |= input.ModLeftShift
	}
	if m&mui.Alt != 0 {
		b |= input.ModLeftAlt
	}
	if m&mui.Super != 0 {
		b |= input.ModLeftGUI
	}
	return b
}

var hidUsage = func() map[mui.Key]uint16 {
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
