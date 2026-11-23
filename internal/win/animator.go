//go:build windows

package win

import (
	"runtime"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	dwmapi               = syscall.NewLazyDLL("dwmapi.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procFillRect         = user32.NewProc("FillRect")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procDwmFlush         = dwmapi.NewProc("DwmFlush")
	procGdiFlush         = gdi32.NewProc("GdiFlush")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      [2]int32
	Private uint32
}

var wndProc = syscall.NewCallback(func(hwnd, m, w, l uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, m, w, l)
	return r
})

// Animate shows a small non-activating window at (x, y) that changes color
// once per desktop composition until stop is closed, so Desktop Duplication
// has a new image every refresh. It never takes focus or moves the cursor.
func Animate(x, y, size int32, stop <-chan struct{}) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	name, _ := syscall.UTF16PtrFromString("PlanckSpikeAnimator")
	wc := wndClassEx{WndProc: wndProc, ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const (
		wsPopup          = 0x80000000
		wsExTopmost      = 0x8
		wsExToolWindow   = 0x80
		wsExNoActivate   = 0x08000000
		swShowNoActivate = 4
	)
	hwnd, _, err := procCreateWindowExW.Call(wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(name)), 0, wsPopup, uintptr(x), uintptr(y), uintptr(size), uintptr(size), 0, 0, 0, 0)
	if hwnd == 0 {
		return err
	}
	defer procDestroyWindow.Call(hwnd)
	procShowWindow.Call(hwnd, swShowNoActivate)
	var m msg
	rect := [4]int32{0, 0, size, size}
	for i := 0; ; i++ {
		select {
		case <-stop:
			return nil
		default:
		}
		for {
			r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
			if r == 0 {
				break
			}
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		dc, _, _ := procGetDC.Call(hwnd)
		color := uintptr(i*37%256) | uintptr(i*91%256)<<8 | uintptr(i*13%256)<<16
		brush, _, _ := procCreateSolidBrush.Call(color)
		procFillRect.Call(dc, uintptr(unsafe.Pointer(&rect)), brush)
		procDeleteObject.Call(brush)
		procReleaseDC.Call(hwnd, dc)
		procGdiFlush.Call()
		procDwmFlush.Call()
	}
}
