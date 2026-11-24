package main

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/pkg/input"
)

var (
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procGetCurrentThreadId  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")
)

const (
	whKeyboardLL = 13
	hcAction     = 0
	wmQuit       = 0x0012
	wmUser       = 0x0400
	pmNoRemove   = 0

	llkhfExtended = 0x01
	llkhfInjected = 0x10
	llkhfUp       = 0x80

	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12
)

// kbdLLHook is KBDLLHOOKSTRUCT.
type kbdLLHook struct {
	vk, scan, flags, time uint32
	extraInfo             uintptr
}

var (
	hookCallbackOnce sync.Once
	hookCallback     uintptr
	// curWinHook is the hook installed, which the callback serves.
	curWinHook atomic.Pointer[winHook]
)

// winHook is a WH_KEYBOARD_LL hook on a locked thread of its own, which
// pumps the messages the hook is called through. It takes keys only while
// the stream's window is in the foreground.
type winHook struct {
	// window is the stream's window, nil while it has none.
	window func() uintptr

	on   func(tapEvent) bool
	hwnd uintptr
	tid  uint32
	done chan struct{}
}

func newKeyTap(win func() *mygo.Window) keyTap {
	return &winHook{window: func() uintptr {
		if w := win(); w != nil && !w.IsDestroyed() {
			return w.NativeHandle()
		}
		return 0
	}}
}

func (h *winHook) start(on func(tapEvent) bool) error {
	hookCallbackOnce.Do(func() { hookCallback = syscall.NewCallback(hookProc) })
	h.hwnd = h.window()
	if h.hwnd == 0 {
		return errors.New("no stream window")
	}
	h.on = on
	h.done = make(chan struct{})
	ready := make(chan error, 1)
	curWinHook.Store(h)
	go h.run(ready)
	if err := <-ready; err != nil {
		<-h.done
		curWinHook.CompareAndSwap(h, nil)
		return err
	}
	return nil
}

func (h *winHook) run(ready chan<- error) {
	runtime.LockOSThread()
	defer close(h.done)
	tid, _, _ := procGetCurrentThreadId.Call()
	h.tid = uint32(tid)
	var msg [48]byte
	// A thread has a message queue, which PostThreadMessageW needs, once it
	// asked for a message.
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, wmUser, wmUser, pmNoRemove)
	hook, _, err := procSetWindowsHookExW.Call(whKeyboardLL, hookCallback, 0, 0)
	if hook == 0 {
		ready <- fmt.Errorf("SetWindowsHookExW: %w", err)
		return
	}
	ready <- nil
	for {
		if got, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0); int32(got) <= 0 {
			break
		}
	}
	procUnhookWindowsHookEx.Call(hook)
}

func (h *winHook) stop() {
	procPostThreadMessageW.Call(uintptr(h.tid), wmQuit, 0, 0)
	<-h.done
	curWinHook.CompareAndSwap(h, nil)
}

func hookProc(code, wparam uintptr, kb *kbdLLHook) uintptr {
	if int32(code) == hcAction {
		if h := curWinHook.Load(); h != nil && h.handle(kb) {
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wparam, uintptr(unsafe.Pointer(kb)))
	return r
}

// handle reports whether the key is kept from the system: the host's own
// injected keys and every key while another window has the foreground
// pass untouched.
func (h *winHook) handle(kb *kbdLLHook) bool {
	if kb.flags&llkhfInjected != 0 && kb.extraInfo == input.InjectMarker {
		return false
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != h.hwnd {
		return false
	}
	e, ok := winTapEvent(kb, asyncChord())
	return ok && h.on(e)
}

// winTapEvent reads a low-level hook's key. Its VK tells the side of each
// modifier, as VK_LSHIFT, and the extended flag the keypad's keys from the
// others.
func winTapEvent(kb *kbdLLHook, chord mui.Modifiers) (tapEvent, bool) {
	usage, ok := input.HIDFromWindows(uint16(kb.vk), kb.flags&llkhfExtended != 0)
	if !ok {
		return tapEvent{}, false
	}
	return tapEvent{usage: usage, down: kb.flags&llkhfUp == 0, chord: chord}, true
}

// asyncChord is the Ctrl, Alt and Shift held before the key the hook has.
func asyncChord() mui.Modifiers {
	var m mui.Modifiers
	down := func(vk uintptr) bool {
		r, _, _ := procGetAsyncKeyState.Call(vk)
		return r&0x8000 != 0
	}
	if down(vkControl) {
		m |= mui.Ctrl
	}
	if down(vkMenu) {
		m |= mui.Alt
	}
	if down(vkShift) {
		m |= mui.Shift
	}
	return m
}
