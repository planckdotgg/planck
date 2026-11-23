//go:build darwin

package display

import (
	"strconv"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/sck"
	"github.com/watzon/planck/pkg/gpu"
)

const (
	pixelBGRA = 0x42475241 // 'BGRA'
	pixel420v = 0x34323076 // '420v'
)

// List returns the displays ScreenCaptureKit can capture.
func List() ([]capture.Display, error) { return sck.Displays() }

// Open captures the display with this ID (a CGDirectDisplayID in decimal);
// empty opens the main display.
func Open(id string, opts Options) (*Source, error) {
	all, err := sck.Displays()
	if err != nil {
		return nil, err
	}
	var d capture.Display
	for _, x := range all {
		if x.ID == id || id == "" && x.Primary {
			d = x
		}
	}
	if d.ID == "" {
		return nil, sck.ErrNoDisplay
	}
	n, _ := strconv.ParseUint(d.ID, 10, 32)
	w, h, fps := opts.Width, opts.Height, opts.FPS
	if w == 0 || h == 0 {
		w, h = d.Width, d.Height
	}
	if fps == 0 {
		fps = max(int(d.Refresh), 60)
	}
	format := uint32(pixelBGRA)
	if opts.NV12 {
		format = pixel420v
	}
	src, err := sck.OpenDisplay(uint32(n), w, h, fps, format)
	if err != nil {
		return nil, err
	}
	d.Width, d.Height = w, h
	return &Source{Source: src, Display: d, Device: gpu.Device{Adapter: d.Adapter}}, nil
}
