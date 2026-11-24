package main

import (
	"slices"

	"github.com/egoist/mygo/gpusurface"
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/input"
)

// inputForwarder turns what the stream view sees, the keys held and the
// pointer over it, into input events for the host. It runs in the view's
// build, on the main thread.
type inputForwarder struct {
	send func(input.Event)
	// video is the size of the picture shown and of the drawable it shows
	// in, in pixels; nil, or zeros, map the pointer over the whole view.
	video func() (fw, fh, dw, dh int)

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
		var fw, fh, dw, dh int
		if f.video != nil {
			fw, fh, dw, dh = f.video()
		}
		nx, ny := mapPointer(x, y, b.W, b.H, fw, fh, dw, dh)
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

// mapPointer maps a point of a bw x bh view onto 0..65535 across the
// picture: a fw x fh frame shown whole in the view's dw x dh drawable, as
// the surface places it. Over a bar the point clamps to the picture's
// edge.
func mapPointer(x, y, bw, bh float32, fw, fh, dw, dh int) (uint16, uint16) {
	if dw <= 0 || dh <= 0 {
		dw, dh = int(bw+0.5), int(bh+0.5)
	}
	px, py := x/bw*float32(dw), y/bh*float32(dh)
	rx, ry, rw, rh := gpusurface.FitRect(fw, fh, dw, dh)
	return normalize(px-float32(rx), float32(rw)), normalize(py-float32(ry), float32(rh))
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

// overlayChord is the modifiers of the stream overlay's keys, which stay
// with the client, even while it captures the system's keys.
const overlayChord = mui.Ctrl | mui.Alt

// overlayKey reports whether k with m is one of the overlay's keys.
func overlayKey(k mui.Key, m mui.Modifiers) bool {
	if m&overlayChord != overlayChord {
		return false
	}
	switch k {
	case mui.KeyO, mui.KeyP, mui.KeyH, mui.KeyM, mui.KeyU, mui.KeyF, mui.KeyI:
		return true
	case mui.KeyD:
		return m&mui.Shift != 0
	}
	return false
}

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
		if !slices.Contains(f.keys, k) && !overlayKey(k, m) {
			f.sendKey(k, true)
		}
	}
	f.keys = append(f.keys[:0], held...)
	f.keys = slices.DeleteFunc(f.keys, func(k mui.Key) bool { return overlayKey(k, m) })
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
