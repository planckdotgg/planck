//go:build windows

package display

import (
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// List returns the displays attached to the desktop.
func List() ([]capture.Display, error) { return dxgi.Displays() }

// Open captures the display with this ID; empty opens the primary display.
// Closing the source frees its device.
func Open(id string, opts Options) (*Source, error) {
	src, err := dxgi.OpenDisplay(id, dxgi.Options{HDR: opts.HDR})
	if err != nil {
		return nil, err
	}
	all, err := dxgi.Displays()
	if err != nil {
		src.Close()
		return nil, err
	}
	var d capture.Display
	for _, x := range all {
		if x.ID == id || id == "" && x.Primary {
			d = x
		}
	}
	w, h := src.Size()
	d.Width, d.Height = w, h
	if r := src.Refresh(); r > 0 {
		d.Refresh = r
	}
	dev := src.Device()
	return &Source{Source: src, Display: d,
		Device: gpu.Device{Adapter: dev.Info, Handle: uintptr(unsafe.Pointer(dev.D3D.Object))}}, nil
}
