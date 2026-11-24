package main

import (
	"math"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/pkg/wire"
)

var (
	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
	procSetCursor          = user32.NewProc("SetCursor")
	procGetCursor          = user32.NewProc("GetCursor")
	procSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	procGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	procCallWindowProcW    = user32.NewProc("CallWindowProcW")
)

const (
	wmSetCursor = 0x0020
	wmNCDestroy = 0x0082
	htClient    = 1
	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC, -4
)

// winIconInfo is ICONINFO.
type winIconInfo struct {
	isIcon     int32
	hotX, hotY uint32
	mask       uintptr
	color      uintptr
}

// bitmapV4Header is the BITMAPINFOHEADER of a 32-bit top-down DIB.
type bitmapV4Header struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPerMeter, yPerMeter   int32
	clrUsed, clrImportant  uint32
}

// cursorKey is a shape at the DPI of the display it shows on.
type cursorKey struct {
	id  uint64
	dpi int
}

// winCursor sets HCURSORs made of the host's shapes, scaled from the
// host's DPI to the window's. MyGo sets its own cursor on every
// WM_SETCURSOR, which comes with every move of the mouse over the child
// window it draws in, so winCursor takes that message on the child, while
// it shows a cursor, and passes it on otherwise.
type winCursor struct {
	win   func() *mygo.Window
	cache cursorCache[cursorKey, uintptr]
	// hooked is the child window last routed through cursorProc.
	hooked uintptr
}

var (
	// hostCursorShowing says cursorProc keeps hostCursorHandle, 0 for
	// none, over the windows it hooks.
	hostCursorShowing atomic.Bool
	hostCursorHandle  atomic.Uintptr
	// cursorNext is the procedure of each window cursorProc took over.
	// Main thread.
	cursorNext     = map[uintptr]uintptr{}
	cursorProcOnce sync.Once
	cursorProcPtr  uintptr
)

func newHostCursor(win func() *mygo.Window) hostCursorSys {
	return &winCursor{win: win, cache: cursorCache[cursorKey, uintptr]{free: func(h uintptr) { procDestroyIcon.Call(h) }}}
}

func (c *winCursor) set(shape *wire.CursorShape) bool {
	w := c.win()
	if w == nil {
		return false
	}
	c.hook(w.NativeHandle())
	var want uintptr
	if shape != nil {
		dpi, _, _ := procGetDpiForWindow.Call(w.NativeHandle())
		if dpi == 0 {
			dpi = 96
		}
		var ok bool
		if want, ok = c.cache.get(cursorKey{shape.ID, int(dpi)}, func() (uintptr, bool) {
			h := newHCursor(shape, int(dpi))
			return h, h != 0
		}); !ok {
			return false
		}
	}
	hostCursorHandle.Store(want)
	hostCursorShowing.Store(true)
	if cur, _, _ := procGetCursor.Call(); cur == want {
		return false
	}
	procSetCursor.Call(want)
	return true
}

func (c *winCursor) unset() { hostCursorShowing.Store(false) }

func (c *winCursor) close() {
	hostCursorShowing.Store(false)
	for hwnd, next := range cursorNext {
		if cur, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlpWndProc); cur == cursorProcPtr {
			procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, next)
			delete(cursorNext, hwnd)
		}
	}
	c.hooked = 0
	c.cache.close()
}

// hook routes the messages of the child window MyGo draws in, in the
// window top, through cursorProc, once per window.
func (c *winCursor) hook(top uintptr) {
	class, _ := syscall.UTF16PtrFromString("MyGoSurface")
	child, _, _ := procFindWindowExW.Call(top, 0, uintptr(unsafe.Pointer(class)), 0)
	if child == 0 || child == c.hooked {
		return
	}
	c.hooked = child
	if _, ok := cursorNext[child]; ok {
		return
	}
	cursorProcOnce.Do(func() { cursorProcPtr = syscall.NewCallback(cursorProc) })
	if next, _, _ := procSetWindowLongPtrW.Call(child, gwlpWndProc, cursorProcPtr); next != 0 {
		cursorNext[child] = next
	}
}

// cursorProc runs on the main thread, as the procedure of the windows
// hook took over.
func cursorProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	next := cursorNext[hwnd]
	switch {
	case next == 0:
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	case msg == wmSetCursor && lparam&0xffff == htClient && hostCursorShowing.Load():
		procSetCursor.Call(hostCursorHandle.Load())
		return 1
	case msg == wmNCDestroy:
		procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, next)
		delete(cursorNext, hwnd)
	}
	r, _, _ := procCallWindowProcW.Call(next, hwnd, msg, wparam, lparam)
	return r
}

// newHCursor makes a cursor of shape at dpi, scaled from the host's DPI;
// 0 if Windows takes none of it. The bitmaps it makes it of are deleted
// whatever happens: the cursor keeps copies.
func newHCursor(shape *wire.CursorShape, dpi int) uintptr {
	w, h := int(shape.Width), int(shape.Height)
	px := shape.RGBA
	hx, hy := int(shape.HotX), int(shape.HotY)
	if k := cursorScale(shape, dpi); math.Abs(k-1) > 0.05 {
		nw, nh := max(int(float64(w)*k+0.5), 1), max(int(float64(h)*k+0.5), 1)
		px = scaleRGBA(px, w, h, nw, nh)
		hx, hy = min(int(float64(hx)*k), nw-1), min(int(float64(hy)*k), nh-1)
		w, h = nw, nh
	}
	hdr := bitmapV4Header{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	hdr.size = uint32(unsafe.Sizeof(hdr))
	var bits unsafe.Pointer
	color, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&hdr)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return 0
	}
	defer procDeleteObject.Call(color)
	dst := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < len(dst); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = px[i+2], px[i+1], px[i], px[i+3]
	}
	and := andMask(px, w, h)
	mask, _, _ := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1, uintptr(unsafe.Pointer(&and[0])))
	if mask == 0 {
		return 0
	}
	defer procDeleteObject.Call(mask)
	ii := winIconInfo{hotX: uint32(hx), hotY: uint32(hy), mask: mask, color: color}
	cur, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return cur
}

// andMask is a cursor's AND mask for RGBA pixels, rows of whole 16-bit
// words, set where a pixel is clear: Windows draws a cursor whose pixels
// all have alpha 0 by its mask, not its alpha, and a clear mask is black.
func andMask(px []byte, w, h int) []byte {
	stride := (w + 15) / 16 * 2
	and := make([]byte, stride*h)
	for y := range h {
		for x := range w {
			if px[(y*w+x)*4+3] == 0 {
				and[y*stride+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	return and
}
