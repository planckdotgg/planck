package main

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo"
	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/internal/apple"
	"github.com/planckdotgg/planck/pkg/input"
)

const (
	cgHIDEventTap         = 0
	cgSessionEventTap     = 1
	cgHeadInsertEventTap  = 0
	cgEventTapOptionDflt  = 0
	cgEventKeyDown        = 10
	cgEventKeyUp          = 11
	cgEventFlagsChanged   = 12
	cgTapDisabledTimeout  = 0xFFFFFFFE
	cgTapDisabledByUser   = 0xFFFFFFFF
	cgFieldKeycode        = 9
	cgFieldSourceUserData = 42

	// CGEventFlags: the generic modifier masks, and the device bits that
	// tell left from right.
	cgFlagShift     = 0x20000
	cgFlagControl   = 0x40000
	cgFlagAlternate = 0x80000
	cgFlagCommand   = 0x100000
	cgFlagDevice    = 0x207F

	macKeyCapsLock = 0x39
)

// macModifierMasks are each modifier key's device and generic flag, by
// virtual key code.
var macModifierMasks = map[uint16][2]uint64{
	0x3B: {0x0001, cgFlagControl}, 0x3E: {0x2000, cgFlagControl},
	0x38: {0x0002, cgFlagShift}, 0x3C: {0x0004, cgFlagShift},
	0x37: {0x0008, cgFlagCommand}, 0x36: {0x0010, cgFlagCommand},
	0x3A: {0x0020, cgFlagAlternate}, 0x3D: {0x0040, cgFlagAlternate},
}

var (
	loadTapOnce                   sync.Once
	axIsProcessTrusted            func() bool
	cgEventTapCreate              func(tap, place, options uint32, mask uint64, callback, info uintptr) uintptr
	cgEventTapEnable              func(tap uintptr, enable bool)
	cgEventGetIntegerValueField   func(ev uintptr, field uint32) int64
	cgEventGetFlags               func(ev uintptr) uint64
	cfMachPortCreateRunLoopSource func(alloc, port uintptr, order int) uintptr
	cfMachPortInvalidate          func(port uintptr)
	cfRunLoopGetCurrent           func() uintptr
	cfRunLoopAddSource            func(loop, src, mode uintptr)
	cfRunLoopRemoveSource         func(loop, src, mode uintptr)
	cfRunLoopRunInMode            func(mode uintptr, seconds float64, returnAfterSource bool) int32
	cfRunLoopStop                 func(loop uintptr)
	cfRunLoopCommonModes          uintptr
	cfRunLoopDefaultMode          uintptr
	tapCallback                   uintptr
)

func loadTap() {
	as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err == nil {
		apple.Register(&axIsProcessTrusted, as, "AXIsProcessTrusted")
	}
	cg, cf := apple.CoreGraphics, apple.CoreFoundation
	apple.Register(&cgEventTapCreate, cg, "CGEventTapCreate")
	apple.Register(&cgEventTapEnable, cg, "CGEventTapEnable")
	apple.Register(&cgEventGetIntegerValueField, cg, "CGEventGetIntegerValueField")
	apple.Register(&cgEventGetFlags, cg, "CGEventGetFlags")
	apple.Register(&cfMachPortCreateRunLoopSource, cf, "CFMachPortCreateRunLoopSource")
	apple.Register(&cfMachPortInvalidate, cf, "CFMachPortInvalidate")
	apple.Register(&cfRunLoopGetCurrent, cf, "CFRunLoopGetCurrent")
	apple.Register(&cfRunLoopAddSource, cf, "CFRunLoopAddSource")
	apple.Register(&cfRunLoopRemoveSource, cf, "CFRunLoopRemoveSource")
	apple.Register(&cfRunLoopRunInMode, cf, "CFRunLoopRunInMode")
	apple.Register(&cfRunLoopStop, cf, "CFRunLoopStop")
	cfRunLoopCommonModes = apple.Const(cf, "kCFRunLoopCommonModes")
	cfRunLoopDefaultMode = apple.Const(cf, "kCFRunLoopDefaultMode")
	// One callback for the process: purego never frees one.
	tapCallback = purego.NewCallback(func(_, typ, ev, _ uintptr) uintptr {
		if t := curMacTap.Load(); t != nil {
			return t.handle(uint32(typ), ev)
		}
		return ev
	})
}

// macAccessibilityHint is what immersive mode says without the permission
// an active event tap needs.
const macAccessibilityHint = "Allow Planck in System Settings › Privacy & Security › Accessibility"

// curMacTap is the tap installed, which the callback serves.
var curMacTap atomic.Pointer[macTap]

// macTap is a CGEventTap at the head of the HID event stream, ahead of the
// Dock and WindowServer's shortcuts, run by a CFRunLoop on a locked thread
// of its own.
type macTap struct {
	// trusted reports the Accessibility permission an active tap needs;
	// tests replace it.
	trusted func() bool

	on       func(tapEvent) bool
	stopping atomic.Bool
	done     chan struct{}

	mu         sync.Mutex
	port, loop uintptr
}

func newKeyTap(func() *mygo.Window) keyTap {
	return &macTap{trusted: func() bool { return axIsProcessTrusted != nil && axIsProcessTrusted() }}
}

func (t *macTap) start(on func(tapEvent) bool) error {
	loadTapOnce.Do(loadTap)
	if !t.trusted() {
		return errTapDenied{hint: macAccessibilityHint}
	}
	t.on = on
	t.stopping.Store(false)
	t.done = make(chan struct{})
	ready := make(chan error, 1)
	curMacTap.Store(t)
	go t.run(ready)
	if err := <-ready; err != nil {
		<-t.done
		curMacTap.CompareAndSwap(t, nil)
		return err
	}
	return nil
}

// run keeps the tap on a thread that ends with it, as the thread's run
// loop does.
func (t *macTap) run(ready chan<- error) {
	runtime.LockOSThread()
	defer close(t.done)
	const keys = 1<<cgEventKeyDown | 1<<cgEventKeyUp | 1<<cgEventFlagsChanged
	port := cgEventTapCreate(cgHIDEventTap, cgHeadInsertEventTap, cgEventTapOptionDflt, keys, tapCallback, 0)
	if port == 0 {
		port = cgEventTapCreate(cgSessionEventTap, cgHeadInsertEventTap, cgEventTapOptionDflt, keys, tapCallback, 0)
	}
	if port == 0 {
		ready <- errTapDenied{hint: macAccessibilityHint + " and Input Monitoring"}
		return
	}
	src := cfMachPortCreateRunLoopSource(0, port, 0)
	loop := cfRunLoopGetCurrent()
	cfRunLoopAddSource(loop, src, cfRunLoopCommonModes)
	t.mu.Lock()
	t.port, t.loop = port, loop
	t.mu.Unlock()
	cgEventTapEnable(port, true)
	ready <- nil
	for !t.stopping.Load() {
		cfRunLoopRunInMode(cfRunLoopDefaultMode, 0.25, false)
	}
	cgEventTapEnable(port, false)
	cfRunLoopRemoveSource(loop, src, cfRunLoopCommonModes)
	cfMachPortInvalidate(port)
	apple.CFRelease(src)
	apple.CFRelease(port)
	t.mu.Lock()
	t.port, t.loop = 0, 0
	t.mu.Unlock()
}

func (t *macTap) stop() {
	t.stopping.Store(true)
	t.mu.Lock()
	loop := t.loop
	t.mu.Unlock()
	if loop != 0 {
		cfRunLoopStop(loop)
	}
	<-t.done
	curMacTap.CompareAndSwap(t, nil)
}

// handle is the tap's callback: it keeps a captured event from the OS by
// returning no event.
func (t *macTap) handle(typ uint32, ev uintptr) uintptr {
	switch typ {
	case cgTapDisabledTimeout, cgTapDisabledByUser:
		// macOS turns off a tap it found slow, or on secure input; turn it
		// back on while it is wanted.
		t.mu.Lock()
		port := t.port
		t.mu.Unlock()
		if port != 0 && !t.stopping.Load() {
			cgEventTapEnable(port, true)
		}
		return ev
	case cgEventKeyDown, cgEventKeyUp, cgEventFlagsChanged:
	default:
		return ev
	}
	if cgEventGetIntegerValueField(ev, cgFieldSourceUserData) == input.InjectMarker {
		return ev
	}
	evs, n := macTapEvents(typ, uint16(cgEventGetIntegerValueField(ev, cgFieldKeycode)), cgEventGetFlags(ev))
	keep := false
	for _, e := range evs[:n] {
		if t.on(e) {
			keep = true
		}
	}
	if keep {
		return 0
	}
	return ev
}

// macTapEvents reads a key event of type typ: a key down or up, or a
// modifier's flagsChanged, down if its flag is set. Caps Lock's
// flagsChanged comes at each press, which toggles the host's as a press
// and release.
func macTapEvents(typ uint32, code uint16, flags uint64) (evs [2]tapEvent, n int) {
	usage, ok := input.HIDFromMac(code)
	if !ok {
		return evs, 0
	}
	var chord mui.Modifiers
	if flags&cgFlagControl != 0 {
		chord |= mui.Ctrl
	}
	if flags&cgFlagAlternate != 0 {
		chord |= mui.Alt
	}
	if flags&cgFlagShift != 0 {
		chord |= mui.Shift
	}
	e := tapEvent{usage: usage, down: typ == cgEventKeyDown, chord: chord}
	if typ != cgEventFlagsChanged {
		evs[0] = e
		return evs, 1
	}
	if code == macKeyCapsLock {
		evs[0], evs[1] = e, e
		evs[0].down = true
		return evs, 2
	}
	m, ok := macModifierMasks[code]
	if !ok {
		return evs, 0
	}
	// A keyboard that sets no device bits says only that some key of the
	// kind is down.
	e.down = flags&m[0] != 0 || flags&cgFlagDevice == 0 && flags&m[1] != 0
	evs[0] = e
	return evs, 1
}
