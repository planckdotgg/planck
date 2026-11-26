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
}

func newSystemPointer(win func() *mygo.Window, motion func(dx, dy float64)) pointerSys {
	return &winPointer{win: win, raw: rawMouse{motion: motion}}
}

func (p *winPointer) hide() { procShowCursor.Call(0) }
func (p *winPointer) show() { procShowCursor.Call(1) }

func (p *winPointer) hold(m pointerMode, area rect) {
	r := p.screen(area)
	if m == pointerRelative {
		cx, cy := (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
		r = winRect{cx, cy, cx + 1, cy + 1}
		p.raw.start()
	}
	p.raw.on.Store(m == pointerRelative)
	procClipCursor.Call(uintptr(unsafe.Pointer(&r)))
}

func (p *winPointer) free() {
	p.raw.on.Store(false)
	procClipCursor.Call(0)
}

func (p *winPointer) warp(x, y float32) {
	r := p.screen(rect{X: x, Y: y})
	procSetCursorPos.Call(uintptr(r.Left), uintptr(r.Top))
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
			if r.buttons != nil {
				rawSideButtons(buf[rawInputHeader:], r.buttons)
			}
			if dx, dy, ok := rawMotion(buf[rawInputHeader:]); ok && on {
				r.motion(dx, dy)
			}
		}
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
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
