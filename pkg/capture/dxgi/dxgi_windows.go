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
	adapter gpu.Adapter
	format  capture.Format
	width   int
	height  int
	refresh float64

	info *win.OutduplFrameInfo
	res  **win.Object
	tex  *win.Object
	held bool
}

// Open duplicates output on device, which must be a D3D11 device of the
// adapter that owns the output. hdr asks for 10-bit or FP16 surfaces through
// IDXGIOutput5::DuplicateOutput1.
func Open(device win.Device, adapter gpu.Adapter, output win.Output, hdr bool) (*Source, error) {
	var formats []uint32
	if hdr {
		formats = []uint32{win.DXGI_FORMAT_R10G10B10A2_UNORM, win.DXGI_FORMAT_R16G16B16A16_FLOAT, win.DXGI_FORMAT_B8G8R8A8_UNORM}
	}
	dup, err := output.Duplicate(device.Object, formats)
	if err != nil {
		return nil, fmt.Errorf("dxgi: duplicate output on %s: %w", adapter.Name, err)
	}
	d := dup.Desc()
	s := &Source{
		dup: dup, adapter: adapter,
		width: int(d.ModeDesc.Width), height: int(d.ModeDesc.Height),
		info: new(win.OutduplFrameInfo), res: new(*win.Object),
	}
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
	return s, nil
}

func (s *Source) Size() (int, int)       { return s.width, s.height }
func (s *Source) Refresh() float64       { return s.refresh }
func (s *Source) Format() capture.Format { return s.format }

// Next returns the next desktop image. A frame with Presented == 0 carries
// only a cursor update.
func (s *Source) Next(timeout time.Duration) (capture.Frame, error) {
	if s.held {
		if err := s.Release(); err != nil {
			return capture.Frame{}, err
		}
	}
	err := s.dup.AcquireNextFrame(uint32(timeout/time.Millisecond), s.info, s.res)
	acquired := clock.Now()
	switch {
	case errors.Is(err, win.DXGI_ERROR_WAIT_TIMEOUT):
		return capture.Frame{}, capture.ErrTimeout
	case errors.Is(err, win.DXGI_ERROR_ACCESS_LOST):
		return capture.Frame{}, capture.ErrAccessLost
	case err != nil:
		return capture.Frame{}, fmt.Errorf("dxgi: acquire: %w", err)
	}
	s.held = true
	tex, err := (*s.res).QueryInterface(&win.IID_ID3D11Texture2D)
	if err != nil {
		return capture.Frame{}, err
	}
	s.tex = tex
	f := capture.Frame{
		Surface:  uintptr(unsafe.Pointer(tex)),
		Adapter:  s.adapter.LUID,
		Format:   s.format,
		Width:    s.width,
		Height:   s.height,
		Acquired: acquired,
	}
	if s.info.LastPresentTime != 0 {
		f.Presented = clock.ToDuration(s.info.LastPresentTime)
	}
	return f, nil
}

// AccumulatedFrames is how many desktop presents the last frame folded in.
func (s *Source) AccumulatedFrames() int { return int(s.info.AccumulatedFrames) }

func (s *Source) Release() error {
	if !s.held {
		return nil
	}
	s.tex.Release()
	(*s.res).Release()
	s.tex, *s.res = nil, nil
	s.held = false
	return s.dup.ReleaseFrame()
}

func (s *Source) Close() error {
	s.Release()
	s.dup.Release()
	return nil
}

var _ capture.Source = (*Source)(nil)
