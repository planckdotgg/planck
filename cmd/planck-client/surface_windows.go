package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/player"
)

// composeUnderUI: on Windows MyGo then draws through DirectComposition,
// on a target above the window's child windows, with alpha, so the video's
// child window shows where the UI paints nothing.
const composeUnderUI = true

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procMoveWindow       = user32.NewProc("MoveWindow")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type rect struct{ Left, Top, Right, Bottom int32 }

const (
	wmNCHitTest   = 0x0084
	htTransparent = ^uintptr(0) // HTTRANSPARENT (-1)
)

// The video's window lets the mouse through to the window under it, the
// UI's, which forwards it to the host.
var videoWndProc = syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmNCHitTest {
		return htTransparent
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
})

var registerClass = sync.OnceValues(func() (*uint16, error) {
	instance, _, _ := procGetModuleHandleW.Call(0)
	name, _ := syscall.UTF16PtrFromString("PlanckVideo")
	wc := wndClassEx{WndProc: videoWndProc, Instance: instance, ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return nil, fmt.Errorf("register the video window class: %v", err)
	}
	return name, nil
})

// surfaceHost is a gpusurface on a child window filling the client area,
// under MyGo's DirectComposition target: the surface needs a window with
// no swap chain of its own.
type surfaceHost struct {
	parent, child uintptr
	surf          *gpusurface.Surface
}

// newSurfaceHost runs on the main thread, which then takes the child
// window's messages with the parent's.
func newSurfaceHost(w *mygo.Window) (*surfaceHost, error) {
	class, err := registerClass()
	if err != nil {
		return nil, err
	}
	parent := w.NativeHandle()
	var r rect
	procGetClientRect.Call(parent, uintptr(unsafe.Pointer(&r)))
	const wsChild, wsVisible, wsDisabled, wsClipSiblings = 0x40000000, 0x10000000, 0x08000000, 0x04000000
	instance, _, _ := procGetModuleHandleW.Call(0)
	child, _, cerr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), 0, wsChild|wsVisible|wsDisabled|wsClipSiblings,
		0, 0, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), parent, 0, instance, 0)
	if child == 0 {
		return nil, fmt.Errorf("create the video window: %v", cerr)
	}
	surf, err := gpusurface.New(gpusurface.Options{Window: child})
	if err != nil {
		procDestroyWindow.Call(child)
		return nil, err
	}
	return &surfaceHost{parent: parent, child: child, surf: surf}, nil
}

// fit sizes the child window to the client area, on the main thread.
func (h *surfaceHost) fit() {
	var r rect
	procGetClientRect.Call(h.parent, uintptr(unsafe.Pointer(&r)))
	procMoveWindow.Call(h.child, 0, 0, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0)
}

// resizeSurface takes the child window's size, which fit set.
func (h *surfaceHost) resizeSurface(p *player.Player, _ *videoView) error { return p.Resize(0, 0) }

func (h *surfaceHost) close() {
	h.surf.Close()
	procDestroyWindow.Call(h.child)
}

var errNoEncoder = errors.New("no hardware encoder")

// localEncoders lists the GPUs with an encoder backend, by name.
func localEncoders() []ui.EncoderInfo {
	all, err := dxgi.Adapters()
	if err != nil {
		return nil
	}
	var out []ui.EncoderInfo
	seen := map[string]bool{}
	for _, a := range all {
		api, err := encoderAPI(a.Vendor)
		if err != nil || seen[a.Name] {
			continue
		}
		if _, ok := encode.BackendFor(a.Vendor); !ok {
			continue
		}
		seen[a.Name] = true
		out = append(out, ui.EncoderInfo{GPU: a.Name, API: api})
	}
	return out
}

func encoderAPI(v gpu.Vendor) (string, error) {
	switch v {
	case gpu.VendorNVIDIA:
		return "NVENC", nil
	case gpu.VendorAMD:
		return "AMF", nil
	case gpu.VendorIntel:
		return "Quick Sync", nil
	}
	return "", errNoEncoder
}
