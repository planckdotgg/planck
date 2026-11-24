package main

import (
	"runtime"
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
	// lock holds the pointer while the mouse is locked to the stream; nil
	// leaves it free.
	lock *pointerLock

	keys []mui.Key
	mods uint8
	// over is whether the host has the pointer's position x, y.
	over bool
	x, y uint16
	// button and pressed are the press on the view; held is whether the
	// host has it, as only a press begun on the picture goes to the host.
	button        int
	pressed, held bool
	// bw and bh are the view's size; wx and wy the scrolling not sent
	// yet, under a wheel unit.
	bw, bh, wx, wy float32
}

// view builds the stream's input surface, which fills the stream view.
// The pointer goes to the host over the picture alone, not over the bars
// beside it, except to carry a press begun on the picture to its edge.
// While the lock holds the pointer relative, its motion goes instead, and
// every press is on the picture.
func (f *inputForwarder) view(c *mui.Context) {
	c.Key("planck-stream-input")
	el := mui.Box(c).Fill().Label("Stream")
	el.HandleInput(f.scroll)
	f.keyboard(c.KeysDown(), c.Modifiers())
	x, y, over := el.PointerPosition()
	b := el.Bounds()
	f.bw, f.bh = b.W, b.H
	fw, fh, dw, dh := f.videoSize()
	relative := false
	if f.lock != nil {
		pic := pictureRect(b.W, b.H, fw, fh, dw, dh)
		pic.X, pic.Y = pic.X+b.X, pic.Y+b.Y
		f.lock.frame(c.WindowActive(), pic)
		relative = f.lock.held() == pointerRelative
	}
	in := relative
	if over && b.W > 0 && b.H > 0 && !relative {
		in = onPicture(x, y, b.W, b.H, fw, fh, dw, dh)
		if in || f.held {
			nx, ny := mapPointer(x, y, b.W, b.H, fw, fh, dw, dh)
			if !f.over || nx != f.x || ny != f.y {
				f.send(input.Event{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: nx, Y: ny}})
			}
			f.x, f.y = nx, ny
		}
	}
	f.over = in || (over && f.held)
	btn, pressed := el.PressedButton()
	if f.held && (!pressed || btn != f.button) {
		f.sendButton(f.button, false)
		f.held = false
	}
	if pressed && (!f.pressed || btn != f.button) && in {
		if f.lock == nil || !f.lock.clickPicture() {
			f.held = f.sendButton(btn, true)
		}
	}
	f.button, f.pressed = btn, pressed
}

func (f *inputForwarder) videoSize() (fw, fh, dw, dh int) {
	if f.video != nil {
		return f.video()
	}
	return 0, 0, 0, 0
}

// wheelDIPs is how far one notch of a mouse wheel scrolls a MyGo view,
// which the host takes as WheelNotch: three lines of 33 DIPs on Windows, a
// line of 40 on macOS.
func wheelDIPs() float32 {
	if runtime.GOOS == "windows" {
		return 99
	}
	return 40
}

// scroll sends the wheel turned over the picture, or anywhere while the
// pointer is held relative; a touchpad's finer scrolling adds up to whole
// units.
func (f *inputForwarder) scroll(ev mui.InputEvent) bool {
	if ev.Kind != mui.InputScroll {
		return false
	}
	if f.lock == nil || f.lock.held() != pointerRelative {
		fw, fh, dw, dh := f.videoSize()
		if f.bw <= 0 || f.bh <= 0 || !onPicture(ev.X, ev.Y, f.bw, f.bh, fw, fh, dw, dh) {
			return false
		}
	}
	k := input.WheelNotch / wheelDIPs()
	f.wx += ev.DX * k
	f.wy -= ev.DY * k
	x, y := whole(float64(f.wx)), whole(float64(f.wy))
	if x != 0 || y != 0 {
		f.wx -= float32(x)
		f.wy -= float32(y)
		f.send(input.Event{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DX: x, DY: y}})
	}
	return true
}

// pictureRect is where the picture sits in a bw x bh view, in its DIPs.
func pictureRect(bw, bh float32, fw, fh, dw, dh int) rect {
	if bw <= 0 || bh <= 0 {
		return rect{}
	}
	if dw <= 0 || dh <= 0 {
		dw, dh = int(bw+0.5), int(bh+0.5)
	}
	rx, ry, w, h := gpusurface.FitRect(fw, fh, dw, dh)
	sx, sy := bw/float32(dw), bh/float32(dh)
	return rect{X: float32(rx) * sx, Y: float32(ry) * sy, W: float32(w) * sx, H: float32(h) * sy}
}

// mapPointer maps a point of a bw x bh view onto 0..65535 across the
// picture: a fw x fh frame shown whole in the view's dw x dh drawable, as
// the surface places it. Over a bar the point clamps to the picture's
// edge.
func mapPointer(x, y, bw, bh float32, fw, fh, dw, dh int) (uint16, uint16) {
	vx, vy, rw, rh := pictureOffset(x, y, bw, bh, fw, fh, dw, dh)
	return normalize(vx, rw), normalize(vy, rh)
}

// onPicture reports whether a point of the view, as mapPointer takes it,
// is on the picture rather than on a bar beside it.
func onPicture(x, y, bw, bh float32, fw, fh, dw, dh int) bool {
	vx, vy, rw, rh := pictureOffset(x, y, bw, bh, fw, fh, dw, dh)
	return vx >= 0 && vx <= rw && vy >= 0 && vy <= rh
}

// pictureOffset is where a point of the view is from the picture's
// top-left corner, and the picture's size, in the drawable's pixels.
func pictureOffset(x, y, bw, bh float32, fw, fh, dw, dh int) (vx, vy, rw, rh float32) {
	if dw <= 0 || dh <= 0 {
		dw, dh = int(bw+0.5), int(bh+0.5)
	}
	px, py := x/bw*float32(dw), y/bh*float32(dh)
	rx, ry, w, h := gpusurface.FitRect(fw, fh, dw, dh)
	return px - float32(rx), py - float32(ry), float32(w), float32(h)
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

// sendButton reports whether b is a button the host has.
func (f *inputForwarder) sendButton(b int, pressed bool) bool {
	var hb input.Button
	switch b {
	case 0:
		hb = input.ButtonLeft
	case 1:
		hb = input.ButtonRight
	case 2:
		hb = input.ButtonMiddle
	default:
		return false
	}
	f.send(input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: hb, Pressed: pressed}})
	return true
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
	case mui.KeyO, mui.KeyP, mui.KeyH, mui.KeyM, mui.KeyU, mui.KeyF, mui.KeyI, mui.KeyG:
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
