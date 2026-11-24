package main

import (
	"errors"
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

// composeUnderUI: on Windows MyGo then draws through a topmost
// DirectComposition target, with alpha, and the video's target, not
// topmost, shows where the UI paints nothing.
const composeUnderUI = true

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procFindWindowExW   = user32.NewProc("FindWindowExW")
	procGetDpiForWindow = user32.NewProc("GetDpiForWindow")
)

// surfaceHost is a gpusurface composed under MyGo's target on the child
// window MyGo draws the UI in, as MyGo shows web views; the window keeps
// the mouse and the keyboard.
type surfaceHost struct {
	surf *gpusurface.Surface
}

// newSurfaceHost runs on the main thread.
func newSurfaceHost(w *mygo.Window) (*surfaceHost, error) {
	class, _ := syscall.UTF16PtrFromString("MyGoSurface")
	content, _, _ := procFindWindowExW.Call(w.NativeHandle(), 0, uintptr(unsafe.Pointer(class)), 0)
	if content == 0 {
		return nil, errors.New("no MyGo surface window to show the video in")
	}
	surf, err := gpusurface.New(gpusurface.Options{Window: content, Composite: true})
	if err != nil {
		return nil, err
	}
	return &surfaceHost{surf: surf}, nil
}

// fit has nothing to do: the presenting thread takes the client size.
func (h *surfaceHost) fit() {}

func (h *surfaceHost) resizeSurface(p *player.Player, _ *videoView) error { return p.Resize(0, 0) }

func (h *surfaceHost) close() { h.surf.Close() }

// windowPixels is the window's content size in pixels: its DIPs at the
// window's DPI.
func windowPixels(w *mygo.Window) (int, int) {
	cw, ch := w.ContentSize()
	dpi, _, _ := procGetDpiForWindow.Call(w.NativeHandle())
	if dpi == 0 {
		dpi = 96
	}
	return (cw*int(dpi) + 48) / 96, (ch*int(dpi) + 48) / 96
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
