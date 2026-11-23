// Package capture defines the screen capture contract shared by the platform
// backends: DXGI Desktop Duplication on Windows, ScreenCaptureKit on macOS,
// and PipeWire or KMS on Linux.
package capture

import (
	"errors"
	"time"

	"github.com/watzon/planck/pkg/gpu"
)

// Format is the pixel layout of a captured surface.
type Format uint8

const (
	FormatBGRA8   Format = iota + 1 // 8-bit SDR desktop
	FormatRGB10A2                   // 10-bit SDR or HDR10 (PQ) desktop
	FormatRGBA16F                   // scRGB HDR desktop
	FormatNV12                      // capture already converted to 4:2:0 8-bit
	FormatP010                      // capture already converted to 4:2:0 10-bit
)

func (f Format) String() string {
	switch f {
	case FormatBGRA8:
		return "BGRA8"
	case FormatRGB10A2:
		return "RGB10A2"
	case FormatRGBA16F:
		return "RGBA16F"
	case FormatNV12:
		return "NV12"
	case FormatP010:
		return "P010"
	}
	return "unknown"
}

// Frame is a GPU surface owned by the source until Release is called on it.
// Surface is the platform handle: an ID3D11Texture2D* on Windows, a
// CVPixelBufferRef (IOSurface backed) on macOS.
type Frame struct {
	Surface uintptr
	Adapter gpu.LUID
	Format  Format
	Width   int
	Height  int
	// Presented is when the compositor showed this image, on the monotonic
	// clock of the platform (QPC on Windows, mach_absolute_time on macOS).
	Presented time.Duration
	// Acquired is when the source handed the frame to the caller, same clock.
	Acquired time.Duration
	// Dirty lists changed rectangles when the backend reports them; nil means
	// the whole frame may have changed.
	Dirty []Rect
}

type Rect struct{ X, Y, W, H int }

// ErrTimeout means no new image arrived in the wait window: the screen did not
// change. It is not a failure.
var ErrTimeout = errors.New("capture: no new frame")

// ErrAccessLost means the source must be reopened (mode change, secure desktop,
// fullscreen switch).
var ErrAccessLost = errors.New("capture: access lost")

// Source produces frames from one display.
type Source interface {
	// Next blocks for up to timeout for a new frame. The returned frame stays
	// valid until Release.
	Next(timeout time.Duration) (Frame, error)
	Release() error
	Close() error
}

// Display describes one capturable output.
type Display struct {
	ID      string
	Name    string
	Adapter gpu.Adapter
	Width   int
	Height  int
	Refresh float64
	HDR     bool
	Primary bool
}
