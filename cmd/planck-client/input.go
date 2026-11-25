package main

import (
	"runtime"
	"slices"

	"github.com/egoist/mygo/gpusurface"
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui"
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
	// sys is immersive mode, whose hook takes the keyboard over while it
	// captures; nil leaves the keyboard here.
	sys *systemKeys
	// local are the chords never sent to the host.
	local *localKeys

	// keys are the keys down on the host; next is scratch for the next.
	keys, next []mui.Key
	mods       uint8
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
// every press is on the picture. Over the picture, not under the overlay,
// the view asks the UI for no cursor of its own while the lock shows the
// host's there (pointerLock.pictureCursor).
func (f *inputForwarder) view(c *mui.Context) {
	c.Key("planck-stream-input")
	el := mui.Box(c).Fill().Label("Stream")
	el.HandleInput(f.scroll)
	f.keysFrame(c.KeysDown(), c.Modifiers(), c.WindowActive())
	x, y, over := el.PointerPosition()
	hovered := el.Hovered()
	b := el.Bounds()
	f.bw, f.bh = b.W, b.H
	fw, fh, dw, dh := f.videoSize()
	relative := false
	if f.lock != nil {
		pic := pictureRect(b.W, b.H, fw, fh, dw, dh)
		pic.X, pic.Y = pic.X+b.X, pic.Y+b.Y
		f.lock.frame(c.WindowActive(), pic)
		relative = f.lock.held() == pointerRelative
		onPic := over && hovered && b.W > 0 && b.H > 0 && onPicture(x, y, b.W, b.H, fw, fh, dw, dh)
		if f.lock.pictureCursor(onPic) != cursorLocal {
			el.Cursor(mui.CursorNone)
		}
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

// localKey reports whether k with m stays on this device: a hotkey or a
// chord never sent.
func (f *inputForwarder) localKey(k mui.Key, m mui.Modifiers) bool {
	u, ok := hidUsage[k]
	return ok && f.local.has(m, u)
}

// keysFrame sends the keyboard's changes, unless immersive mode's hook has
// the keyboard: then the keys sent so far go up, and the hook sends every
// key until it lets go.
func (f *inputForwarder) keysFrame(held []mui.Key, m mui.Modifiers, active bool) {
	if f.sys != nil && f.sys.frame(active) {
		held, m = nil, 0
	}
	f.keyboard(held, m)
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
		if !slices.Contains(f.keys, k) && !f.localKey(k, m) {
			f.sendKey(k, true)
		}
	}
	f.next = f.next[:0]
	for _, k := range held {
		if slices.Contains(f.keys, k) || !f.localKey(k, m) {
			f.next = append(f.next, k)
		}
	}
	f.keys, f.next = f.next, f.keys
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

var hidUsage = ui.HIDUsage
