package main

import (
	"math"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/gpusurface"
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/inputtrace"
)

// inputForwarder turns what the stream view sees, the keys held and the
// pointer over it, into input events for the host. It runs in the view's
// build, on the main thread.
type inputForwarder struct {
	send func(input.Event)
	// picture draws the frames in the view, where the surface has the UI
	// draw them (Linux picture mode); nil draws nothing.
	picture func(c *mui.Context)
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
	// relative is whether the last frame found the pointer held relative,
	// which moves the host's cursor where no place sent says.
	relative bool
	// relMoves is the lock's count of relative moves sent, as the last
	// frame found it.
	relMoves uint32
	warp     warpPin
	// btnSys tells what MyGo does not of the buttons; nil takes MyGo's
	// buttons 0, 1 and 2 as left, right and middle, and its releases alone.
	btnSys     buttonSys
	btnStarted bool
	// notify asks for a frame, from any goroutine.
	notify func()
	// edges are the presses and releases come since the last frame; a side
	// button's may come from another thread.
	edgeMu            sync.Mutex
	edges, edgesSpare []buttonEdge
	// buttons are the buttons down on the host, as 1<<Button: only a press
	// begun on the picture goes to the host.
	buttons uint8
	// live is whether the stream view takes side buttons' presses.
	live atomic.Bool
	// sideIn is whether the last frame found a side press would go to the
	// host, as for one come since.
	sideIn atomic.Bool
	// bw and bh are the view's size; wx and wy the scrolling not sent
	// yet, under a wheel unit.
	bw, bh, wx, wy float32

	// meta is the stream window for the input trace's session_meta; nil
	// tells nothing of it. lastMeta and lastFrame are what the trace
	// last wrote of them.
	meta      func() windowMeta
	lastMeta  sessionMeta
	metaEpoch uint32
	lastFrame frameKey
}

// buttonSys is what the system tells of the mouse's buttons that MyGo does
// not. MyGo follows one press at a time, the button pressed last, so the
// forwarder takes each button's press and release as it comes instead.
type buttonSys interface {
	// start runs once, on the main thread, as the stream view first builds.
	start()
	// other is the host's button for the press or release of MyGo's button
	// 2 being delivered, which on macOS is any button past the right; ok
	// is false for one the host has not.
	other() (b input.Button, ok bool)
	// down reports whether b is down now.
	down(b input.Button) bool
}

// buttonEdge is a press, begun on the picture or not (in), or a release.
// at tells x, y, in the view's DIPs, is where it came. A side press from
// raw input knows no place: in is what the frame before it found, and the
// frame that sends it finds where the pointer is. cancel lets go of every
// button.
type buttonEdge struct {
	b                          input.Button
	down, in, side, at, cancel bool
	x, y                       float32
}

// warpPin is the lock's warp number n, to x, y in the view, taken while on
// and MyGo still has the pointer at myX, myY, where it had it then.
type warpPin struct {
	n              uint32
	on             bool
	myX, myY, x, y float32
}

// framePointer is where the frame finds the pointer, on the host's
// picture, where ok.
type framePointer struct {
	x, y uint16
	ok   bool
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
	el.HandleInput(f.handle)
	el.ObserveInput(f.observe)
	// Taking presses, the view gets the releases of the button pressed
	// last wherever the pointer goes; the observer gets those over it.
	el.Pressed()
	if f.btnSys != nil && !f.btnStarted {
		f.btnSys.start()
		f.btnStarted = true
	}
	active := c.WindowActive()
	f.keysFrame(c.KeysDown(), c.Modifiers(), active)
	x, y, over := el.PointerPosition()
	rawX, rawY, rawOver := x, y, over
	hovered := el.Hovered()
	b := el.Bounds()
	f.bw, f.bh = b.W, b.H
	fw, fh, dw, dh := f.videoSize()
	relative := false
	if f.lock != nil {
		pic := pictureRect(b.W, b.H, fw, fh, dw, dh)
		pic.X, pic.Y = pic.X+b.X, pic.Y+b.Y
		f.lock.frame(active, pic)
		relative = f.lock.held() == pointerRelative
		x, y, over = f.warped(x, y, over, b.X, b.Y)
		if moves := f.lock.relativeMoves(); f.relative && !relative || moves != f.relMoves {
			f.over = false
			f.relMoves = moves
		}
		f.relative = relative
		onPic := over && hovered && b.W > 0 && b.H > 0 && onPicture(x, y, b.W, b.H, fw, fh, dw, dh)
		if f.lock.pictureCursor(onPic) != cursorLocal {
			el.Cursor(mui.CursorNone)
		}
	}
	in := relative
	var at framePointer
	if over && b.W > 0 && b.H > 0 && !relative {
		in = onPicture(x, y, b.W, b.H, fw, fh, dw, dh)
		at.x, at.y = mapPointer(x, y, b.W, b.H, fw, fh, dw, dh)
		at.ok = true
	}
	reaches := over && hovered && x >= 0 && y >= 0 && x <= b.W && y <= b.H
	f.live.Store(true)
	held := f.buttons
	f.buttonsFrame(active, active && (relative || in && reaches), !relative, at)
	sent := false
	if at.ok && (in || held|f.buttons != 0) {
		sent = f.moveAbs(at.x, at.y)
	}
	if itrace.On() {
		f.traceMeta(traceArea{b.X, b.Y, b.W, b.H}, fw, fh, dw, dh, active, relative)
		f.traceFrame(rawX, rawY, rawOver, relative, b.W > 0 && b.H > 0, at, in || held|f.buttons != 0, sent)
	}
	f.over = in || (over && held|f.buttons != 0)
	if f.picture != nil {
		el.Children(func() { f.picture(c) })
	}
}

// warpPinSlop is how far, in DIPs, from where MyGo had the pointer at a
// warp an event it reports may be and still be one from before the warp:
// the cursor relative mode held stays within a point of its place.
const warpPinSlop = 2

// warped is where the pointer is, x, y and over as MyGo has it in a view
// at bx, by: no system tells MyGo of a warp, so after the lock's last
// warp the pointer is where the warp put it until MyGo's place moves off
// where it had it then.
func (f *inputForwarder) warped(x, y float32, over bool, bx, by float32) (float32, float32, bool) {
	if n, wx, wy := f.lock.lastWarp(); n != f.warp.n {
		f.warp = warpPin{n: n, on: true, myX: x, myY: y, x: wx - bx, y: wy - by}
	}
	if !f.warp.on {
		return x, y, over
	}
	if math.Abs(float64(x-f.warp.myX)) > warpPinSlop || math.Abs(float64(y-f.warp.myY)) > warpPinSlop {
		f.warp.on = false
		return x, y, over
	}
	return f.warp.x, f.warp.y, true
}

// moveAbs puts the host's pointer at x, y, unless it is there, and
// reports whether it sent the move.
func (f *inputForwarder) moveAbs(x, y uint16) bool {
	sent := !f.over || x != f.x || y != f.y
	if sent {
		f.send(input.Event{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: x, Y: y}})
	}
	f.x, f.y, f.over = x, y, true
	return sent
}

// handle takes the wheel, and sees the releases MyGo gives the view as the
// one pressed.
func (f *inputForwarder) handle(ev mui.InputEvent) bool {
	if ev.Kind == mui.InputPointerUp {
		f.observe(ev)
		return false
	}
	return f.scroll(ev)
}

// observe queues each button's press over the view, and its release, with
// where it came, for the frame to send in order. A canceled release, the
// system taking the press, lets go of every button: MyGo gives one release
// of the lot.
func (f *inputForwarder) observe(ev mui.InputEvent) {
	if ev.Kind != mui.InputPointerDown && ev.Kind != mui.InputPointerUp {
		return
	}
	if ev.Kind == mui.InputPointerUp && ev.Canceled {
		f.queue(buttonEdge{cancel: true})
		return
	}
	b, ok := f.hostButton(ev.Button)
	if !ok {
		return
	}
	e := buttonEdge{b: b, down: ev.Kind == mui.InputPointerDown, at: true, x: ev.X, y: ev.Y}
	if e.down {
		fw, fh, dw, dh := f.videoSize()
		e.in = f.lock != nil && f.lock.held() == pointerRelative ||
			f.bw > 0 && f.bh > 0 && onPicture(ev.X, ev.Y, f.bw, f.bh, fw, fh, dw, dh)
	}
	f.queue(e)
}

// sideButton takes a side button's press or release, which MyGo does not
// report, from any goroutine. Whether a press goes to the host is the
// next frame's to say, once it knows where the pointer is: it goes where
// that frame and the one before it both found the pointer on the picture,
// as the pointer may have crossed the overlay between them.
func (f *inputForwarder) sideButton(b input.Button, down bool) {
	if down && !f.live.Load() {
		return
	}
	f.queue(buttonEdge{b: b, down: down, side: down, in: down && f.sideIn.Load()})
	if f.notify != nil {
		f.notify()
	}
}

func (f *inputForwarder) queue(e buttonEdge) {
	f.edgeMu.Lock()
	f.edges = append(f.edges, e)
	f.edgeMu.Unlock()
}

// hostButton is the host's button for MyGo's button b.
func (f *inputForwarder) hostButton(b int) (input.Button, bool) {
	switch b {
	case 0:
		return input.ButtonLeft, true
	case 1:
		return input.ButtonRight, true
	case 2:
		if f.btnSys != nil {
			return f.btnSys.other()
		}
		return input.ButtonMiddle, true
	}
	return 0, false
}

// buttonsFrame sends the presses and releases queued since the last frame,
// each button on its own and, unless the pointer is held relative (abs
// false), at its place: its own, or the frame's (at). A side press goes
// where sideIn. With the window inactive every button goes up. Otherwise a
// button the system no longer holds goes up, as its release may have come
// where the view does not hear it, off the window or over the overlay,
// even with the pointer back since; one pressed this frame waits for the
// next.
func (f *inputForwarder) buttonsFrame(active, sideIn, abs bool, at framePointer) {
	f.edgeMu.Lock()
	edges := f.edges
	f.edges, f.edgesSpare = f.edgesSpare[:0], edges
	f.edgeMu.Unlock()
	var pressed uint8
	for _, e := range edges {
		bit := uint8(1) << e.b
		switch {
		case e.cancel:
			f.releaseButtons(func(input.Button) bool { return true })
		case e.down && active && f.buttons&bit == 0 && e.in && (!e.side || sideIn):
			if f.lock != nil && f.lock.clickPicture() {
				f.traceDroppedButton(e, "engaged_lock")
				continue
			}
			f.edgeAt(e, abs, at)
			f.buttons |= bit
			pressed |= bit
			f.sendButton(e.b, true)
		case !e.down && f.buttons&bit != 0:
			f.edgeAt(e, abs, at)
			f.buttons &^= bit
			f.sendButton(e.b, false)
		case itrace.On():
			f.traceDroppedButton(e, droppedEdge(e, active, f.buttons&bit != 0, sideIn))
		}
	}
	f.sideIn.Store(sideIn)
	f.releaseButtons(func(b input.Button) bool {
		return !active || f.btnSys != nil && pressed&(1<<b) == 0 && !f.btnSys.down(b)
	})
}

// releaseButtons lets go of the buttons held on the host that up says to.
func (f *inputForwarder) releaseButtons(up func(input.Button) bool) {
	for b := input.ButtonLeft; b <= input.ButtonX2 && f.buttons != 0; b++ {
		bit := uint8(1) << b
		if f.buttons&bit != 0 && up(b) {
			f.buttons &^= bit
			f.sendButton(b, false)
		}
	}
}

// edgeAt puts the host's pointer where e came, before e goes.
func (f *inputForwarder) edgeAt(e buttonEdge, abs bool, at framePointer) {
	switch {
	case !abs:
	case e.at && f.bw > 0 && f.bh > 0:
		fw, fh, dw, dh := f.videoSize()
		f.moveAbs(mapPointer(e.x, e.y, f.bw, f.bh, fw, fh, dw, dh))
	case at.ok:
		f.moveAbs(at.x, at.y)
	}
}

// streamEnded forgets the buttons held, as the host lets go of them as the
// stream ends, and the pointer's place: the next host has none, and every
// warp so far, seen by a frame or not, is the old picture's.
func (f *inputForwarder) streamEnded() {
	f.live.Store(false)
	f.edgeMu.Lock()
	f.edges = f.edges[:0]
	f.edgeMu.Unlock()
	f.buttons = 0
	f.over, f.relative = false, false
	f.lastMeta, f.metaEpoch, f.lastFrame = sessionMeta{}, 0, frameKey{}
	f.warp = warpPin{}
	if f.lock != nil {
		f.warp.n, _, _ = f.lock.lastWarp()
	}
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
			if itrace.On() {
				inputtrace.Emit(itrace, "client_input", clientInputRec{Event: "wheel", RawDX: float64(ev.DX), RawDY: float64(ev.DY),
					Mode: f.mode().String(), SentKind: "suppressed", Suppress: "off_picture"})
			}
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
	if itrace.On() {
		r := clientInputRec{Event: "wheel", RawDX: float64(ev.DX), RawDY: float64(ev.DY), Mode: f.mode().String(), SentKind: "wheel",
			SentDX: int32(x), SentDY: int32(y)}
		if x == 0 && y == 0 {
			r.SentKind, r.Suppress = "suppressed", "under_one_unit"
		}
		inputtrace.Emit(itrace, "client_input", r)
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

func (f *inputForwarder) sendButton(b input.Button, pressed bool) {
	f.send(input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: b, Pressed: pressed}})
	if itrace.On() {
		down := pressed
		inputtrace.Emit(itrace, "client_input", clientInputRec{Event: "button", Mode: f.mode().String(), SentKind: "button",
			Button: buttonName(b), Pressed: &down, SentX: f.x, SentY: f.y})
	}
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
