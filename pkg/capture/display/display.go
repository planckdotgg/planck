// Package display lists the capturable displays of this machine and opens
// one with the platform's capture backend: DXGI Desktop Duplication on
// Windows, ScreenCaptureKit on macOS, and MIT-SHM or the desktop portal's
// PipeWire stream on Linux.
package display

import (
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Options configure Open.
type Options struct {
	// HDR captures 10-bit or FP16 surfaces when the display is in HDR
	// (Windows).
	HDR bool
	// Width, Height and FPS set the stream size and rate where the backend
	// scales (macOS); FPS is also the rate X11 capture polls at. Zero
	// takes the display's own.
	Width, Height, FPS int
	// NV12 asks for 4:2:0 frames where the backend converts (macOS); BGRA
	// otherwise.
	NV12 bool
}

// Source is an open display.
type Source struct {
	capture.Source
	Display capture.Display
	// Device is the GPU device the frames live on; encoders open on it for
	// zero-copy.
	Device gpu.Device
}
