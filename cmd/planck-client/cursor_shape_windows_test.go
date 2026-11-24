package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/wire"
)

var (
	procGetIconInfo     = user32.NewProc("GetIconInfo")
	procGetObjectW      = gdi32.NewProc("GetObjectW")
	procGetGuiResources = user32.NewProc("GetGuiResources")
	procSendMessageW    = user32.NewProc("SendMessageW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procGetCurrentProc  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
)

func guiObjects(t *testing.T) (gdi, user uintptr) {
	p, _, _ := procGetCurrentProc.Call()
	gdi, _, _ = procGetGuiResources.Call(p, 0)  // GR_GDIOBJECTS
	user, _, _ = procGetGuiResources.Call(p, 1) // GR_USEROBJECTS
	return gdi, user
}

func testRGBA(w, h int) []byte {
	px := make([]byte, w*h*4)
	for i := range w * h {
		px[i*4+3] = 255
	}
	return px
}

// TestHCursorSizeAndHotspot: a 32 x 32 cursor of a 100% host shows at 48
// pixels on a 150% display, its hotspot with it, and making and
// destroying cursors leaks no GDI or USER objects.
func TestHCursorSizeAndHotspot(t *testing.T) {
	s := &wire.CursorShape{ID: 1, Width: 32, Height: 32, HotX: 10, HotY: 20, DPI: 96, RGBA: testRGBA(32, 32)}
	for _, c := range []struct{ dpi, side, hx, hy int }{{96, 32, 10, 20}, {144, 48, 15, 30}, {192, 64, 20, 40}} {
		h := newHCursor(s, c.dpi)
		if h == 0 {
			t.Fatalf("no cursor at %d DPI", c.dpi)
		}
		var ii winIconInfo
		if r, _, err := procGetIconInfo.Call(h, uintptr(unsafe.Pointer(&ii))); r == 0 {
			t.Fatal(err)
		}
		var bm struct {
			typ, width, height, widthBytes int32
			planes, bitsPixel              uint16
			bits                           uintptr
		}
		procGetObjectW.Call(ii.color, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm)))
		procDeleteObject.Call(ii.color)
		procDeleteObject.Call(ii.mask)
		procDestroyIcon.Call(h)
		if int(bm.width) != c.side || int(bm.height) != c.side || int(ii.hotX) != c.hx || int(ii.hotY) != c.hy || bm.bitsPixel != 32 {
			t.Errorf("%d DPI: %dx%d %dbpp hot %d,%d; want %d, hot %d,%d", c.dpi, bm.width, bm.height, bm.bitsPixel, ii.hotX, ii.hotY, c.side, c.hx, c.hy)
		}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	round := func() {
		cache := cursorCache[cursorKey, uintptr]{free: func(h uintptr) { procDestroyIcon.Call(h) }}
		for i := range 500 {
			cache.get(cursorKey{uint64(i), 144}, func() (uintptr, bool) { h := newHCursor(s, 144); return h, h != 0 })
		}
		cache.close()
	}
	round()
	gdi, user := guiObjects(t)
	round()
	round()
	if g, u := guiObjects(t); g > gdi || u > user {
		t.Errorf("GDI objects %d to %d, USER objects %d to %d over 1000 cursors", gdi, g, user, u)
	}
}

// TestCursorProcTakesSetCursor: on a window like MyGo's, the hook answers
// WM_SETCURSOR over the client area while the host's cursor shows, passes
// every other message and every WM_SETCURSOR otherwise to the window's own
// procedure, and lets go of the window as it is destroyed.
func TestCursorProcTakesSetCursor(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var seen []uintptr
	var child uintptr
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if hwnd == child {
			seen = append(seen, msg)
		}
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	})
	reg := func(name string) *uint16 {
		cls, _ := syscall.UTF16PtrFromString(name)
		wc := struct {
			size, style                   uint32
			wndProc                       uintptr
			clsExtra, wndExtra            int32
			instance, icon, cursor, brush uintptr
			menuName, className           *uint16
			iconSm                        uintptr
		}{wndProc: proc, className: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		return cls
	}
	top, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(reg("PlanckCursorTestTop"))), 0, 0, 0, 0, 100, 100, 0, 0, 0, 0)
	if top == 0 {
		t.Fatal(err)
	}
	const wsChild = 0x40000000
	child, _, err = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(reg("MyGoSurface"))), 0, wsChild, 0, 0, 100, 100, top, 0, 0, 0)
	if child == 0 {
		t.Fatal(err)
	}
	c := &winCursor{cache: cursorCache[cursorKey, uintptr]{free: func(h uintptr) { procDestroyIcon.Call(h) }}}
	c.hook(top)
	if _, ok := cursorNext[child]; !ok {
		t.Fatal("the MyGo child was not hooked")
	}
	const wmMouseMove = 0x0200
	setCursor := func() uintptr {
		r, _, _ := procSendMessageW.Call(child, wmSetCursor, child, htClient|wmMouseMove<<16)
		return r
	}
	hostCursorHandle.Store(0)
	hostCursorShowing.Store(true)
	seen = nil
	if r := setCursor(); r != 1 || len(seen) != 0 {
		t.Fatalf("showing: returned %d, the window saw %#x", r, seen)
	}
	hostCursorShowing.Store(false)
	setCursor()
	if len(seen) != 1 || seen[0] != wmSetCursor {
		t.Fatalf("not showing: the window saw %#x", seen)
	}
	procDestroyWindow.Call(top)
	if _, ok := cursorNext[child]; ok {
		t.Fatal("a destroyed window stayed hooked")
	}
	c.close()
}
