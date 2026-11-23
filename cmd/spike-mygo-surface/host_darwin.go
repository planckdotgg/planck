package main

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/gpusurface"
)

// AppKit wants its windows made on the main thread, which the main
// goroutine stays on.
func init() { runtime.LockOSThread() }

var appkit struct {
	once        sync.Once
	err         error
	msgSend     uintptr
	poolPush    uintptr
	poolPop     uintptr
	initWindow  func(obj uintptr, sel objc.SEL, r nsRect, style, backing uint, deferFlag bool) uintptr
	floatGetter func(obj uintptr, sel objc.SEL) float64
	otherEvent  func(cls uintptr, sel objc.SEL, typ uint, loc nsPoint, flags uint, ts float64, wn int, ctx uintptr, subtype int16, d1, d2 int) uintptr
}

type nsRect struct{ X, Y, W, H float64 }
type nsPoint struct{ X, Y float64 }

func loadAppKit() error {
	appkit.once.Do(func() {
		objcLib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err == nil {
			_, err = purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		}
		if err == nil {
			appkit.msgSend, err = purego.Dlsym(objcLib, "objc_msgSend")
		}
		if err == nil {
			appkit.poolPush, err = purego.Dlsym(objcLib, "objc_autoreleasePoolPush")
		}
		if err == nil {
			appkit.poolPop, err = purego.Dlsym(objcLib, "objc_autoreleasePoolPop")
		}
		if err != nil {
			appkit.err = err
			return
		}
		purego.RegisterFunc(&appkit.initWindow, appkit.msgSend)
		purego.RegisterFunc(&appkit.floatGetter, appkit.msgSend)
		purego.RegisterFunc(&appkit.otherEvent, appkit.msgSend)
	})
	return appkit.err
}

func send(obj uintptr, sel string, args ...any) uintptr {
	return uintptr(objc.ID(obj).Send(objc.RegisterName(sel), args...))
}

func class(name string) uintptr { return uintptr(objc.GetClass(name)) }

// host is a borderless window that ignores the mouse and never activates
// the app: it shows the frames without taking focus.
type host struct{ win uintptr }

func newHost(w, h, x, y int) (*host, error) {
	if err := loadAppKit(); err != nil {
		return nil, err
	}
	pool, _, _ := purego.SyscallN(appkit.poolPush)
	defer purego.SyscallN(appkit.poolPop, pool)
	app := send(class("NSApplication"), "sharedApplication")
	const activationPolicyAccessory = 1
	send(app, "setActivationPolicy:", uintptr(activationPolicyAccessory))
	scale := appkit.floatGetter(send(class("NSScreen"), "mainScreen"), objc.RegisterName("backingScaleFactor"))
	if scale <= 0 {
		return nil, fmt.Errorf("no screen")
	}
	const styleBorderless, backingBuffered = 0, 2
	win := appkit.initWindow(send(class("NSWindow"), "alloc"), objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
		nsRect{float64(x) / scale, float64(y) / scale, float64(w) / scale, float64(h) / scale}, styleBorderless, backingBuffered, false)
	if win == 0 {
		return nil, fmt.Errorf("cannot make a window")
	}
	send(win, "setReleasedWhenClosed:", false)
	send(win, "setIgnoresMouseEvents:", true)
	send(win, "setOpaque:", true)
	send(win, "orderFrontRegardless")
	return &host{win: win}, nil
}

func (h *host) handle() uintptr { return h.win }

// run runs the app's event loop on the main thread until stop.
func (h *host) run() {
	send(send(class("NSApplication"), "sharedApplication"), "run")
}

// stop ends run, from another thread: the loop checks for stop: after
// each event, so an empty one follows it.
func (h *host) stop() {
	app := send(class("NSApplication"), "sharedApplication")
	send(app, "performSelectorOnMainThread:withObject:waitUntilDone:", objc.RegisterName("stop:"), uintptr(0), true)
	const eventApplicationDefined = 15
	ev := appkit.otherEvent(class("NSEvent"), objc.RegisterName("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
		eventApplicationDefined, nsPoint{}, 0, 0, 0, 0, 0, 0, 0)
	send(app, "postEvent:atStart:", ev, true)
}

func (h *host) close() {
	send(h.win, "orderOut:", uintptr(0))
	send(h.win, "close")
	send(h.win, "release")
}

func msg(obj uintptr, sel objc.SEL) uintptr {
	r, _, _ := syscall6(appkit.msgSend, obj, uintptr(sel), 0, 0, 0, 0)
	return r
}

func msg1(obj uintptr, sel objc.SEL, a uintptr) uintptr {
	r, _, _ := syscall6(appkit.msgSend, obj, uintptr(sel), a, 0, 0, 0)
	return r
}

func msg3(obj uintptr, sel objc.SEL, a, b, c uintptr) uintptr {
	r, _, _ := syscall6(appkit.msgSend, obj, uintptr(sel), a, b, c, 0)
	return r
}

func presentMode(*gpusurface.Surface) string {
	return "CAMetalLayer, 2 drawables, display sync off"
}

func composition(*gpusurface.Surface) string { return "by the window server" }
