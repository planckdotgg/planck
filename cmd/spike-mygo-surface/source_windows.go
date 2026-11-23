package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo/gpusurface"
)

const (
	devCreateTexture2D        = 5
	devCreateRenderTargetView = 9
	ctxClearRenderTargetView  = 50
	ctxFlush                  = 111
	comRelease                = 2

	dxgiB8G8R8A8Unorm, dxgiNV12, dxgiP010 = 87, 103, 104
	dxgiR8Unorm, dxgiR8G8Unorm            = 61, 49
	dxgiR16Unorm, dxgiR16G16Unorm         = 56, 35
	bindShaderResourceRenderTarget        = 0x8 | 0x20
	rtvDimensionTexture2D                 = 4
)

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type rtvDesc struct {
	Format, ViewDimension uint32
	MipSlice              uint32
	_                     [2]uint32
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// call calls method i of a COM object, as the vendored gpusurface does. Setup only:
// it allocates (see fastCall).
//
//go:uintptrescapes
func call(obj uintptr, i int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(ptr(obj))
	fn := *(*uintptr)(unsafe.Add(ptr(vtbl), i*int(unsafe.Sizeof(uintptr(0)))))
	var a [16]uintptr
	a[0] = obj
	n := copy(a[1:], args)
	r, _, _ := syscall.SyscallN(fn, a[:n+1]...)
	return r
}

// source stands for a hardware decoder: a pool of textures on the
// surface's device that the GPU clears to another color each frame, with
// no CPU access to their pixels.
type source struct {
	ctx      uintptr
	textures []uintptr
	targets  [][2]uintptr
	planes   int
	color    [4]float32
}

func newSource(surf *gpusurface.Surface, format gpusurface.Format, w, h, n int) (*source, error) {
	var texFormat uint32
	var planeFormats [2]uint32
	switch format {
	case gpusurface.FormatBGRA8:
		texFormat = dxgiB8G8R8A8Unorm
	case gpusurface.FormatNV12:
		texFormat, planeFormats = dxgiNV12, [2]uint32{dxgiR8Unorm, dxgiR8G8Unorm}
	case gpusurface.FormatP010:
		texFormat, planeFormats = dxgiP010, [2]uint32{dxgiR16Unorm, dxgiR16G16Unorm}
	}
	s := &source{ctx: surf.Context(), planes: 2}
	if format == gpusurface.FormatBGRA8 {
		s.planes = 1
	}
	dev := surf.Device()
	td := texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: texFormat, SampleCount: 1,
		BindFlags: bindShaderResourceRenderTarget}
	for range n {
		var tex uintptr
		if hr := call(dev, devCreateTexture2D, uintptr(unsafe.Pointer(&td)), 0, uintptr(unsafe.Pointer(&tex))); int32(uint32(hr)) < 0 {
			s.close()
			return nil, fmt.Errorf("cannot create a %s texture: %#x", format, uint32(hr))
		}
		s.textures = append(s.textures, tex)
		var t [2]uintptr
		for p := range s.planes {
			var desc uintptr
			d := rtvDesc{Format: planeFormats[p], ViewDimension: rtvDimensionTexture2D}
			if planeFormats[p] != 0 {
				desc = uintptr(unsafe.Pointer(&d))
			}
			if hr := call(dev, devCreateRenderTargetView, tex, desc, uintptr(unsafe.Pointer(&t[p]))); int32(uint32(hr)) < 0 {
				s.targets = append(s.targets, t)
				s.close()
				return nil, fmt.Errorf("cannot draw into plane %d of a %s texture: %#x", p, format, uint32(hr))
			}
		}
		s.targets = append(s.targets, t)
	}
	return s, nil
}

// produce clears the next texture of the pool on the GPU to the color of
// frame i and returns it.
// fastCall is the vendored gpusurface's: call with no allocation, for
// pointers to the heap or C memory only.
func fastCall(obj uintptr, i int, a, b uintptr) uintptr {
	vtbl := *(*uintptr)(ptr(obj))
	fn := *(*uintptr)(unsafe.Add(ptr(vtbl), i*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, obj, a, b)
	return r
}

func (s *source) produce(i int) uintptr {
	slot := i % len(s.textures)
	v := float32(i%240) / 240
	for p := range s.planes {
		s.color = [4]float32{0.0627 + 0.8588*v, 0.5 - 0.3*v, 0.5 + 0.3*v, 1}
		fastCall(s.ctx, ctxClearRenderTargetView, s.targets[slot][p], uintptr(unsafe.Pointer(&s.color)))
	}
	return s.textures[slot]
}

func (s *source) close() {
	for _, t := range s.targets {
		for _, v := range t {
			if v != 0 {
				call(v, comRelease)
			}
		}
	}
	for _, t := range s.textures {
		call(t, comRelease)
	}
	if s.ctx != 0 {
		call(s.ctx, ctxFlush)
	}
}
