package main

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/gpusurface"
)

const producerShaders = `
#include <metal_stdlib>
using namespace metal;
struct VOut { float4 pos [[position]]; float2 uv; };
vertex VOut vs(uint vid [[vertex_id]]) {
	float2 p = float2((vid << 1) & 2, vid & 2);
	VOut o;
	o.pos = float4(p * float2(2, -2) + float2(-1, 1), 0, 1);
	o.uv = p;
	return o;
}
fragment float4 p_rgb(VOut in [[stage_in]], constant uint &f [[buffer(0)]]) {
	float t = fract(float(f) / 240.0);
	return float4(fract(in.uv.x + t), in.uv.y, 0.5, 1);
}
fragment float p_luma(VOut in [[stage_in]], constant uint &f [[buffer(0)]]) {
	float t = fract(float(f) / 240.0);
	return 0.0627 + 0.8588 * fract(in.uv.x + t);
}
fragment float2 p_chroma(VOut in [[stage_in]], constant uint &f [[buffer(0)]]) {
	return 0.0627 + 0.8784 * float2(in.uv.y, 1 - in.uv.y);
}
`

const (
	mtlR8Unorm, mtlR16Unorm, mtlRG8Unorm, mtlRG16Unorm, mtlBGRA8Unorm = 10, 20, 30, 60, 80
	usageShaderReadRenderTarget                                       = 1 | 4
)

var (
	selCommandBuffer = objc.RegisterName("commandBuffer")
	selSetTexture    = objc.RegisterName("setTexture:")
	selRenderEncoder = objc.RegisterName("renderCommandEncoderWithDescriptor:")
	selSetPipeline   = objc.RegisterName("setRenderPipelineState:")
	selSetFragBytes  = objc.RegisterName("setFragmentBytes:length:atIndex:")
	selDraw          = objc.RegisterName("drawPrimitives:vertexStart:vertexCount:")
	selEndEncoding   = objc.RegisterName("endEncoding")
	selCommit        = objc.RegisterName("commit")
	selWaitCompleted = objc.RegisterName("waitUntilCompleted")
	selRelease       = objc.RegisterName("release")
)

// source stands for a hardware decoder: a pool of IOSurface-backed pixel
// buffers that the GPU draws each frame into, on the surface's queue, with
// no CPU access to their pixels.
type source struct {
	queue      uintptr
	pass, att  uintptr
	buffers    []uintptr    // CVPixelBufferRef, owned
	surfaces   []uintptr    // their IOSurfaceRef
	targets    [][2]uintptr // their planes as render targets
	pipes      [2]uintptr
	planes     int
	frameIndex uint32
}

func newSource(surf *gpusurface.Surface, format gpusurface.Format, w, h, n int) (*source, error) {
	cv, err := purego.Dlopen("/System/Library/Frameworks/CoreVideo.framework/CoreVideo", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return nil, err
	}
	sym := func(name string) uintptr {
		p, e := purego.Dlsym(cv, name)
		if e != nil && err == nil {
			err = e
		}
		return p
	}
	create := sym("CVPixelBufferCreate")
	getSurface := sym("CVPixelBufferGetIOSurface")
	ioKey := sym("kCVPixelBufferIOSurfacePropertiesKey")
	metalKey := sym("kCVPixelBufferMetalCompatibilityKey")
	if err != nil {
		return nil, err
	}
	pool, _, _ := purego.SyscallN(appkit.poolPush)
	defer purego.SyscallN(appkit.poolPop, pool)

	var fourcc uint32
	var planeFormats [2]uintptr
	var fragments [2]string
	switch format {
	case gpusurface.FormatBGRA8:
		fourcc, planeFormats, fragments = 0x42475241, [2]uintptr{mtlBGRA8Unorm}, [2]string{"p_rgb"}
	case gpusurface.FormatNV12:
		fourcc, planeFormats, fragments = 0x34323076, [2]uintptr{mtlR8Unorm, mtlRG8Unorm}, [2]string{"p_luma", "p_chroma"}
	case gpusurface.FormatP010:
		fourcc, planeFormats, fragments = 0x78343230, [2]uintptr{mtlR16Unorm, mtlRG16Unorm}, [2]string{"p_luma", "p_chroma"}
	}
	s := &source{queue: surf.Queue(), planes: 2}
	if format == gpusurface.FormatBGRA8 {
		s.planes = 1
	}
	device := surf.Device()

	keys := [2]uintptr{deref(ioKey), deref(metalKey)}
	values := [2]uintptr{send(class("NSDictionary"), "dictionary"), send(class("NSNumber"), "numberWithBool:", true)}
	attrs := send(class("NSDictionary"), "dictionaryWithObjects:forKeys:count:",
		uintptr(unsafe.Pointer(&values[0])), uintptr(unsafe.Pointer(&keys[0])), uintptr(2))
	for range n {
		var pb uintptr
		if r, _, _ := purego.SyscallN(create, 0, uintptr(w), uintptr(h), uintptr(fourcc), attrs, uintptr(unsafe.Pointer(&pb))); int32(r) != 0 || pb == 0 {
			s.close()
			return nil, fmt.Errorf("CVPixelBufferCreate %s: %d", format, int32(r))
		}
		io, _, _ := purego.SyscallN(getSurface, pb)
		s.buffers = append(s.buffers, pb)
		s.surfaces = append(s.surfaces, io)
		var t [2]uintptr
		for p := range s.planes {
			pw, ph := w, h
			if p == 1 {
				pw, ph = (w+1)/2, (h+1)/2
			}
			d := send(class("MTLTextureDescriptor"), "texture2DDescriptorWithPixelFormat:width:height:mipmapped:", planeFormats[p], uintptr(pw), uintptr(ph), false)
			send(d, "setUsage:", uintptr(usageShaderReadRenderTarget))
			t[p] = send(device, "newTextureWithDescriptor:iosurface:plane:", d, io, uintptr(p))
			if t[p] == 0 {
				s.close()
				return nil, fmt.Errorf("cannot wrap plane %d of a %s IOSurface", p, format)
			}
		}
		s.targets = append(s.targets, t)
	}

	var errObj uintptr
	src := send(class("NSString"), "stringWithUTF8String:", producerShaders+"\x00")
	lib := send(device, "newLibraryWithSource:options:error:", src, uintptr(0), uintptr(unsafe.Pointer(&errObj)))
	if lib == 0 {
		s.close()
		return nil, fmt.Errorf("cannot compile the producer's shaders")
	}
	defer send(lib, "release")
	for p := range s.planes {
		desc := send(send(class("MTLRenderPipelineDescriptor"), "alloc"), "init")
		send(desc, "setVertexFunction:", send(lib, "newFunctionWithName:", send(class("NSString"), "stringWithUTF8String:", "vs\x00")))
		send(desc, "setFragmentFunction:", send(lib, "newFunctionWithName:", send(class("NSString"), "stringWithUTF8String:", fragments[p]+"\x00")))
		send(send(send(desc, "colorAttachments"), "objectAtIndexedSubscript:", uintptr(0)), "setPixelFormat:", planeFormats[p])
		s.pipes[p] = send(device, "newRenderPipelineStateWithDescriptor:error:", desc, uintptr(unsafe.Pointer(&errObj)))
		send(desc, "release")
		if s.pipes[p] == 0 {
			s.close()
			return nil, fmt.Errorf("cannot make the producer's pipeline")
		}
	}
	s.pass = send(send(class("MTLRenderPassDescriptor"), "renderPassDescriptor"), "retain")
	s.att = send(send(s.pass, "colorAttachments"), "objectAtIndexedSubscript:", uintptr(0))
	send(s.att, "setLoadAction:", uintptr(0))
	send(s.att, "setStoreAction:", uintptr(1))
	return s, nil
}

// produce draws frame i into the next buffer of the pool on the GPU and
// returns its IOSurface.
func (s *source) produce(i int) uintptr {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	slot := i % len(s.surfaces)
	pool, _, _ := syscall6(appkit.poolPush, 0, 0, 0, 0, 0, 0)
	cb := msg(s.queue, selCommandBuffer)
	s.frameIndex = uint32(i)
	for p := range s.planes {
		msg1(s.att, selSetTexture, s.targets[slot][p])
		enc := msg1(cb, selRenderEncoder, s.pass)
		msg1(enc, selSetPipeline, s.pipes[p])
		msg3(enc, selSetFragBytes, uintptr(unsafe.Pointer(&s.frameIndex)), 4, 0)
		msg3(enc, selDraw, 3, 0, 3)
		msg(enc, selEndEncoding)
	}
	msg(cb, selCommit)
	syscall6(appkit.poolPop, pool, 0, 0, 0, 0, 0)
	return s.surfaces[slot]
}

func (s *source) close() {
	if s.queue != 0 {
		pool, _, _ := purego.SyscallN(appkit.poolPush)
		cb := msg(s.queue, selCommandBuffer)
		msg(cb, selCommit)
		msg(cb, selWaitCompleted)
		purego.SyscallN(appkit.poolPop, pool)
	}
	for _, t := range s.targets {
		for _, p := range t {
			if p != 0 {
				msg(p, selRelease)
			}
		}
	}
	for _, p := range s.pipes {
		if p != 0 {
			msg(p, selRelease)
		}
	}
	if s.pass != 0 {
		msg(s.pass, selRelease)
	}
	for _, b := range s.buffers {
		msg(b, selRelease)
	}
}

// deref reads the pointer at addr, a C global's address from Dlsym.
func deref(addr uintptr) uintptr { return **(**uintptr)(unsafe.Pointer(&addr)) }
