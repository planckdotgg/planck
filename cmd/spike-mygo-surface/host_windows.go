package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo/gpusurface"
)

// The window's messages are taken on the thread that made it, the main
// one.
func init() { runtime.LockOSThread() }

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procShowWindow             = user32.NewProc("ShowWindow")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW     = user32.NewProc("PostThreadMessageW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId     = kernel32.NewProc("GetCurrentThreadId")
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type winMsg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

var wndProc = syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
})

// host is a borderless window that never activates: it shows the frames
// without taking focus.
type host struct {
	hwnd   uintptr
	thread uintptr
}

func newHost(w, h, x, y int) (*host, error) {
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	procSetProcessDpiAwareness.Call(perMonitorAwareV2)
	instance, _, _ := procGetModuleHandleW.Call(0)
	name, _ := syscall.UTF16PtrFromString("PlanckSpikeSurface")
	wc := wndClassEx{WndProc: wndProc, Instance: instance, ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const wsPopup, wsExNoActivate, wsExToolWindow = 0x80000000, 0x08000000, 0x80
	hwnd, _, err := procCreateWindowExW.Call(wsExNoActivate|wsExToolWindow, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)),
		wsPopup, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, instance, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("cannot make a window: %v", err)
	}
	const swShowNoActivate = 4
	procShowWindow.Call(hwnd, swShowNoActivate)
	thread, _, _ := procGetCurrentThreadId.Call()
	return &host{hwnd: hwnd, thread: thread}, nil
}

func (h *host) handle() uintptr { return h.hwnd }

// run takes the window's messages on the main thread until stop.
func (h *host) run() {
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// stop ends run, from another thread.
func (h *host) stop() {
	const wmQuit = 0x12
	procPostThreadMessageW.Call(h.thread, wmQuit, 0, 0)
}

func (h *host) close() { procDestroyWindow.Call(h.hwnd) }

func presentMode(s *gpusurface.Surface) string {
	adapter := s.AdapterName()
	if adapter == "" {
		adapter = "default"
	}
	return fmt.Sprintf("flip-discard swap chain, sync interval 0, tearing %v, adapter %s", s.Tearing(), adapter)
}

var iidIDXGISwapChainMedia = [16]byte{0x0b, 0xb9, 0x95, 0xdd, 0x5f, 0xf0, 0x6a, 0x4f, 0xbd, 0x65, 0x25, 0xbf, 0xb2, 0x64, 0xbd, 0x84}

// composition tells how DWM showed the last frame, from the swap chain's
// media statistics (DXGI_FRAME_PRESENTATION_MODE).
func composition(s *gpusurface.Surface) string {
	var media uintptr
	if hr := call(s.SwapChain(), 0, uintptr(unsafe.Pointer(&iidIDXGISwapChainMedia)), uintptr(unsafe.Pointer(&media))); int32(uint32(hr)) < 0 {
		return fmt.Sprintf("unknown (no IDXGISwapChainMedia: %#x)", uint32(hr))
	}
	defer call(media, comRelease)
	var st struct {
		PresentCount, PresentRefreshCount, SyncRefreshCount uint32
		SyncQPCTime, SyncGPUTime                            int64
		CompositionMode, ApprovedPresentDuration            uint32
	}
	const getFrameStatisticsMedia, statisticsDisjoint = 3, 0x887a000b
	// The first read after statistics went unread reports them disjoint.
	hr := call(media, getFrameStatisticsMedia, uintptr(unsafe.Pointer(&st)))
	if uint32(hr) == statisticsDisjoint {
		hr = call(media, getFrameStatisticsMedia, uintptr(unsafe.Pointer(&st)))
	}
	if int32(uint32(hr)) < 0 {
		return fmt.Sprintf("unknown (GetFrameStatisticsMedia: %#x)", uint32(hr))
	}
	modes := []string{"composed by DWM", "overlay (multiplane)", "none (independent flip)", "composition failure"}
	if int(st.CompositionMode) < len(modes) {
		return modes[st.CompositionMode]
	}
	return fmt.Sprint(st.CompositionMode)
}
