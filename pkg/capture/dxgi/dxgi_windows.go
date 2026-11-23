//go:build windows

// Package dxgi captures a Windows display with DXGI Desktop Duplication. The
// frame stays on the GPU that owns the output; nothing is copied to memory.
package dxgi

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/watzon/planck/internal/clock"
	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/gpu"
)

type Source struct {
	dup     win.Duplication
	device  win.Device
	output  win.Output
	owned   *Device
	adapter gpu.Adapter
	hdr     bool
	format  capture.Format
	width   int
	height  int
	refresh float64

	// Out parameters of the per-frame COM calls live on the heap so the
	// calls do not allocate.
	info *win.OutduplFrameInfo
	res  **win.Object
	tex  **win.Object
	held bool
}

// Options configure OpenDisplay.
type Options struct {
	// HDR asks for 10-bit or FP16 surfaces through
	// IDXGIOutput5::DuplicateOutput1 when the desktop is in HDR.
	HDR bool
}

// OpenDisplay duplicates the display with this ID (`\\.\DISPLAY1`; empty
// for the primary display) on a new D3D11 device of the adapter that drives
// it. The source owns the device; encoders take it from Device.
func OpenDisplay(id string, opts Options) (*Source, error) {
	out, adapter, err := findOutput(id)
	if err != nil {
		return nil, err
	}
	defer out.Release()
	dev, err := OpenDevice(adapter.LUID)
	if err != nil {
		return nil, err
	}
	s, err := Open(dev.D3D, dev.Info, out, opts.HDR)
	if err != nil {
		dev.Close()
		return nil, err
	}
	s.owned = dev
	return s, nil
}

// Open duplicates output on device, which must be a D3D11 device of the
// adapter that owns the output. hdr asks for 10-bit or FP16 surfaces through
// IDXGIOutput5::DuplicateOutput1.
func Open(device win.Device, adapter gpu.Adapter, output win.Output, hdr bool) (*Source, error) {
	s := &Source{
		device: device, output: output, adapter: adapter, hdr: hdr,
		info: new(win.OutduplFrameInfo), res: new(*win.Object), tex: new(*win.Object),
	}
	if err := s.duplicate(); err != nil {
		return nil, err
	}
	addRef(output.Object)
	return s, nil
}

func (s *Source) duplicate() error {
	var formats []uint32
	if s.hdr {
		formats = []uint32{win.DXGI_FORMAT_R10G10B10A2_UNORM, win.DXGI_FORMAT_R16G16B16A16_FLOAT, win.DXGI_FORMAT_B8G8R8A8_UNORM}
	}
	dup, err := s.output.Duplicate(s.device.Object, formats)
	if err != nil {
		return fmt.Errorf("dxgi: duplicate output on %s: %w", s.adapter.Name, err)
	}
	d := dup.Desc()
	s.dup = dup
	s.width, s.height = int(d.ModeDesc.Width), int(d.ModeDesc.Height)
	s.refresh = 0
	if d.ModeDesc.RefreshDen != 0 {
		s.refresh = float64(d.ModeDesc.RefreshNum) / float64(d.ModeDesc.RefreshDen)
	}
	switch d.ModeDesc.Format {
	case win.DXGI_FORMAT_R10G10B10A2_UNORM:
		s.format = capture.FormatRGB10A2
	case win.DXGI_FORMAT_R16G16B16A16_FLOAT:
		s.format = capture.FormatRGBA16F
	default:
		s.format = capture.FormatBGRA8
	}
	return nil
}

func (s *Source) Size() (int, int)       { return s.width, s.height }
func (s *Source) Refresh() float64       { return s.refresh }
func (s *Source) Format() capture.Format { return s.format }
func (s *Source) Adapter() gpu.Adapter   { return s.adapter }

// Device is the device OpenDisplay made, or nil for a source made by Open.
func (s *Source) Device() *Device { return s.owned }

// Next waits up to timeout for the compositor's next desktop image. Updates
// that move only the cursor are skipped. Presented is the compositor's
// present time and Acquired the time Next got the frame, both on the QPC
// clock (internal/clock).
func (s *Source) Next(timeout time.Duration) (capture.Frame, error) {
	if s.held {
		if err := s.Release(); err != nil {
			return capture.Frame{}, err
		}
	}
	deadline := clock.Now() + timeout
	for {
		left := deadline - clock.Now()
		if left < 0 {
			left = 0
		}
		r := vcall(s.dup.Object, 8, uintptr(left/time.Millisecond), uintptr(unsafe.Pointer(s.info)), uintptr(unsafe.Pointer(s.res)))
		acquired := clock.Now()
		switch hr := win.HRESULT(r); {
		case hr == win.DXGI_ERROR_WAIT_TIMEOUT:
			return capture.Frame{}, capture.ErrTimeout
		case hr == win.DXGI_ERROR_ACCESS_LOST:
			return capture.Frame{}, capture.ErrAccessLost
		case hr.Failed():
			return capture.Frame{}, fmt.Errorf("dxgi: acquire: %w", hr)
		}
		s.held = true
		if s.info.LastPresentTime == 0 {
			// Cursor only: no new desktop image.
			if err := s.Release(); err != nil {
				return capture.Frame{}, err
			}
			if acquired >= deadline {
				return capture.Frame{}, capture.ErrTimeout
			}
			continue
		}
		if hr := win.HRESULT(vcall(*s.res, 0, uintptr(unsafe.Pointer(&win.IID_ID3D11Texture2D)), uintptr(unsafe.Pointer(s.tex)), 0)); hr.Failed() {
			return capture.Frame{}, fmt.Errorf("dxgi: desktop image is not a texture: %w", hr)
		}
		return capture.Frame{
			Surface:   uintptr(unsafe.Pointer(*s.tex)),
			Adapter:   s.adapter.LUID,
			Format:    s.format,
			Width:     s.width,
			Height:    s.height,
			Presented: clock.ToDuration(s.info.LastPresentTime),
			Acquired:  acquired,
		}, nil
	}
}

// AccumulatedFrames is how many desktop presents the last frame folded in.
func (s *Source) AccumulatedFrames() int { return int(s.info.AccumulatedFrames) }

func (s *Source) Release() error {
	if !s.held {
		return nil
	}
	if *s.tex != nil {
		(*s.tex).Release()
		*s.tex = nil
	}
	(*s.res).Release()
	*s.res = nil
	s.held = false
	return s.dup.ReleaseFrame()
}

// Recover reopens duplication after Next returned capture.ErrAccessLost: a
// mode change, a full-screen switch, or the secure desktop (UAC, lock
// screen), during which duplication fails until the user returns. It retries
// every interval until it succeeds or timeout passes. Size, Format and
// Refresh reflect the new mode afterwards.
func (s *Source) Recover(timeout, interval time.Duration) error {
	s.Release()
	if s.dup.Object != nil {
		s.dup.Release()
		s.dup.Object = nil
	}
	deadline := time.Now().Add(timeout)
	for {
		err := s.duplicate()
		if err == nil {
			return nil
		}
		if !retryable(err) || time.Now().Add(interval).After(deadline) {
			return fmt.Errorf("%w: %w", capture.ErrAccessLost, err)
		}
		time.Sleep(interval)
	}
}

// retryable reports the duplication errors that clear by themselves: the
// secure desktop (E_ACCESSDENIED), a mode change in progress, or too many
// duplications of this output at once.
func retryable(err error) bool {
	var hr win.HRESULT
	if !errors.As(err, &hr) {
		return false
	}
	switch hr {
	case win.DXGI_ERROR_ACCESS_LOST, win.DXGI_ERROR_UNSUPPORTED, win.DXGI_ERROR_NOT_CURRENTLY_AVAILABLE,
		eAccessDenied, dxgiErrorSessionDisconnected:
		return true
	}
	return false
}

const (
	eAccessDenied                win.HRESULT = 0x80070005
	dxgiErrorSessionDisconnected win.HRESULT = 0x887a0028
)

func (s *Source) Close() error {
	s.Release()
	if s.dup.Object != nil {
		s.dup.Release()
		s.dup.Object = nil
	}
	if s.output.Object != nil {
		s.output.Release()
		s.output.Object = nil
	}
	s.owned.Close()
	return nil
}

var _ capture.Source = (*Source)(nil)
