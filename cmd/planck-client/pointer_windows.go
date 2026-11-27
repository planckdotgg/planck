package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/pkg/input"
)

var (
	procShowCursor              = user32.NewProc("ShowCursor")
	procClipCursor              = user32.NewProc("ClipCursor")
	procSetCursorPos            = user32.NewProc("SetCursorPos")
	procClientToScreen          = user32.NewProc("ClientToScreen")
	procRegisterClassExW        = user32.NewProc("RegisterClassExW")
	procCreateWindowExW         = user32.NewProc("CreateWindowExW")
	procDefWindowProcW          = user32.NewProc("DefWindowProcW")
	procGetMessageW             = user32.NewProc("GetMessageW")
	procDispatchMessageW        = user32.NewProc("DispatchMessageW")
	procRegisterRawInputDevices = user32.NewProc("RegisterRawInputDevices")
	procGetRawInputData         = user32.NewProc("GetRawInputData")
)

const (
	wmInput          = 0x00FF
	ridInput         = 0x10000003
	ridevInputSink   = 0x00000100
	rimTypeMouse     = 0
	mouseMoveAbs     = 0x0001
	rawInputHeader   = 24
	rawInputMouseLen = rawInputHeader + 24
)

type winPoint struct{ X, Y int32 }

type winRect struct{ Left, Top, Right, Bottom int32 }

// winPointer holds the Windows pointer: confined, ClipCursor keeps it on
// the picture and the window's own moves go on as usual; relative, it is
// hidden and clipped to the picture's centre, and raw input brings the
// mouse's motion, unaccelerated.
type winPointer struct {
	win func() *mygo.Window
	raw rawMouse
	// clip and setPos are ClipCursor, nil clearing it, and SetCursorPos.
	clip   func(r *winRect)
	setPos func(x, y int32)
}

func newSystemPointer(win func() *mygo.Window, motion func(dx, dy float64)) pointerSys {
	return &winPointer{
		win: win, raw: rawMouse{motion: motion},
		clip:   func(r *winRect) { procClipCursor.Call(uintptr(unsafe.Pointer(r))) },
		setPos: func(x, y int32) { procSetCursorPos.Call(uintptr(x), uintptr(y)) },
	}
}

func (p *winPointer) hide() { procShowCursor.Call(0) }
func (p *winPointer) show() { procShowCursor.Call(1) }

// hold and free warp after the clip changes: SetCursorPos clamps to the
// clip in place, the relative one a pixel wide.
func (p *winPointer) hold(m pointerMode, area rect, at spot) bool {
	r := p.screen(area)
	if m == pointerRelative {
		cx, cy := (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
		r = winRect{cx, cy, cx + 1, cy + 1}
		p.raw.start()
	}
	p.raw.on.Store(m == pointerRelative)
	p.clip(&r)
	p.warp(at)
	return at.set
}

func (p *winPointer) free(at spot) bool {
	p.raw.on.Store(false)
	p.clip(nil)
	p.warp(at)
	return at.set
}

func (p *winPointer) warp(at spot) {
	if !at.set {
		return
	}
	r := p.screen(rect{X: at.x, Y: at.y})
	p.setPos(r.Left, r.Top)
}

// screen is a rectangle of the window's content in the screen's pixels.
func (p *winPointer) screen(a rect) winRect {
	w := p.win()
	if w == nil {
		return winRect{}
	}
	var o winPoint
	procClientToScreen.Call(w.NativeHandle(), uintptr(unsafe.Pointer(&o)))
	scale := float32(1)
	if cw, _ := w.ContentSize(); cw > 0 {
		pw, _ := windowPixels(w)
		scale = float32(pw) / float32(cw)
	}
	px := func(v float32) int32 { return int32(v*scale + 0.5) }
	return winRect{o.X + px(a.X), o.Y + px(a.Y), o.X + px(a.X+a.W), o.Y + px(a.Y+a.H)}
}

// rawMouse reads the mouse's raw input on a message-only window of its
// own thread, from the first relative hold or the stream view's first
// build on: its motion while on is set, and the side buttons, which the
// window hears as Back and Forward alone, to buttons if set before start.
type rawMouse struct {
	once    sync.Once
	on      atomic.Bool
	motion  func(dx, dy float64)
	buttons func(b input.Button, down bool)
}

func (r *rawMouse) start() { r.once.Do(func() { go r.run() }) }

func (r *rawMouse) run() {
	runtime.LockOSThread()
	class, _ := syscall.UTF16PtrFromString("PlanckRawMouse")
	wc := struct {
		size, style                   uint32
		wndProc                       uintptr
		clsExtra, wndExtra            int32
		instance, icon, cursor, brush uintptr
		menuName, className           *uint16
		iconSm                        uintptr
	}{wndProc: syscall.NewCallback(r.wndProc), className: class}
	wc.size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const hwndMessage = ^uintptr(2) // HWND_MESSAGE, (HWND)-3
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
	if hwnd == 0 {
		return
	}
	dev := struct {
		usagePage, usage uint16
		flags            uint32
		target           uintptr
	}{usagePage: 1, usage: 2, flags: ridevInputSink, target: hwnd}
	if ok, _, _ := procRegisterRawInputDevices.Call(uintptr(unsafe.Pointer(&dev)), 1, unsafe.Sizeof(dev)); ok == 0 {
		return
	}
	var msg [48]byte
	for {
		if got, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0); int32(got) <= 0 {
			return
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (r *rawMouse) wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if on := r.on.Load(); msg == wmInput && (on || r.buttons != nil) {
		var buf [rawInputMouseLen]byte
		size := uint32(len(buf))
		n, _, _ := procGetRawInputData.Call(lparam, ridInput, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), rawInputHeader)
		if int32(n) == rawInputMouseLen && *(*uint32)(unsafe.Pointer(&buf[0])) == rimTypeMouse {
			r.report(buf[rawInputHeader:], on)
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

// report passes on a RAWMOUSE: its motion, while on, before its side
// buttons, which a frame may send before this thread is back.
func (r *rawMouse) report(m []byte, on bool) {
	if dx, dy, ok := rawMotion(m); ok && on {
		r.motion(dx, dy)
	}
	if r.buttons != nil {
		rawSideButtons(m, r.buttons)
	}
}

// rawMotion reads a RAWMOUSE's relative motion; a tablet's or a remote
// desktop's absolute positions are not motion.
func rawMotion(m []byte) (dx, dy float64, ok bool) {
	if *(*uint16)(unsafe.Pointer(&m[0]))&mouseMoveAbs != 0 {
		return 0, 0, false
	}
	x, y := *(*int32)(unsafe.Pointer(&m[12])), *(*int32)(unsafe.Pointer(&m[16]))
	return float64(x), float64(y), x != 0 || y != 0
}

// RAWMOUSE usButtonFlags of the side buttons.
const (
	riMouseButton4Down = 0x0040
	riMouseButton4Up   = 0x0080
	riMouseButton5Down = 0x0100
	riMouseButton5Up   = 0x0200
)

// rawSideButtons passes on a RAWMOUSE's side button presses and releases.
func rawSideButtons(m []byte, to func(b input.Button, down bool)) {
	flags := *(*uint16)(unsafe.Pointer(&m[4]))
	for _, e := range [...]struct {
		flag uint16
		b    input.Button
		down bool
	}{
		{riMouseButton4Down, input.ButtonX1, true}, {riMouseButton4Up, input.ButtonX1, false},
		{riMouseButton5Down, input.ButtonX2, true}, {riMouseButton5Up, input.ButtonX2, false},
	} {
		if flags&e.flag != 0 {
			to(e.b, e.down)
		}
	}
}
