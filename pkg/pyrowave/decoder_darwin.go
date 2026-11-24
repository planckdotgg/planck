//go:build darwin && arm64

package pyrowave

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/planckdotgg/planck/internal/apple"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
)

func init() {
	decode.Register(decode.Backend{Name: "PyroWave Metal", Codec: encode.PyroWave,
		Open: func(t decode.Target, cfg decode.Config) (decode.Decoder, error) {
			d, err := OpenMetalDecoder(t.Device, t.Queue, cfg)
			if err != nil {
				return nil, err
			}
			return d, nil
		}})
}

// syscall6 calls a C function with integer arguments and allocates
// nothing, unlike purego.SyscallN and objc.Send.
//
//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var mtl struct {
	once                       sync.Once
	err                        error
	msgSend, poolPush, poolPop uintptr
	ioCreate, ioLock, ioUnlock uintptr
	ioBase, ioBytesPerRow      uintptr
	createDevice               uintptr
}

var (
	selCommandBuffer      = objc.RegisterName("commandBuffer")
	selComputeEncoder     = objc.RegisterName("computeCommandEncoder")
	selSetPipeline        = objc.RegisterName("setComputePipelineState:")
	selSetBuffer          = objc.RegisterName("setBuffer:offset:atIndex:")
	selSetBytes           = objc.RegisterName("setBytes:length:atIndex:")
	selSetTexture         = objc.RegisterName("setTexture:atIndex:")
	selDispatch           = objc.RegisterName("dispatchThreadgroups:threadsPerThreadgroup:")
	selEndEncoding        = objc.RegisterName("endEncoding")
	selCommit             = objc.RegisterName("commit")
	selWaitUntilCompleted = objc.RegisterName("waitUntilCompleted")
	selRetain             = objc.RegisterName("retain")
	selRelease            = objc.RegisterName("release")
	selContents           = objc.RegisterName("contents")
	selNewBuffer          = objc.RegisterName("newBufferWithLength:options:")
	selGPUStart           = objc.RegisterName("GPUStartTime")
	selGPUEnd             = objc.RegisterName("GPUEndTime")
	selNewCommandQueue    = objc.RegisterName("newCommandQueue")
)

const (
	storageShared  = 0 << 4
	storagePrivate = 2 << 4
	usageRead      = 1
	usageWrite     = 2
	formatBGRA8    = 80
	fourccBGRA     = 0x42475241
)

func loadMetal() error {
	mtl.once.Do(func() {
		open := func(path string) uintptr {
			h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
			if err != nil && mtl.err == nil {
				mtl.err = fmt.Errorf("pyrowave: %w", err)
			}
			return h
		}
		objcLib := open("/usr/lib/libobjc.A.dylib")
		metal := open("/System/Library/Frameworks/Metal.framework/Metal")
		if mtl.err != nil {
			return
		}
		sym := func(h uintptr, name string) uintptr {
			p, err := purego.Dlsym(h, name)
			if err != nil && mtl.err == nil {
				mtl.err = fmt.Errorf("pyrowave: %w", err)
			}
			return p
		}
		mtl.msgSend = sym(objcLib, "objc_msgSend")
		mtl.poolPush = sym(objcLib, "objc_autoreleasePoolPush")
		mtl.poolPop = sym(objcLib, "objc_autoreleasePoolPop")
		mtl.createDevice = sym(metal, "MTLCreateSystemDefaultDevice")
		mtl.ioCreate = apple.Sym(apple.IOSurface, "IOSurfaceCreate")
		mtl.ioLock = apple.Sym(apple.IOSurface, "IOSurfaceLock")
		mtl.ioUnlock = apple.Sym(apple.IOSurface, "IOSurfaceUnlock")
		mtl.ioBase = apple.Sym(apple.IOSurface, "IOSurfaceGetBaseAddress")
		mtl.ioBytesPerRow = apple.Sym(apple.IOSurface, "IOSurfaceGetBytesPerRow")
	})
	return mtl.err
}

func msg(obj uintptr, sel objc.SEL) uintptr {
	r, _, _ := syscall6(mtl.msgSend, obj, uintptr(sel), 0, 0, 0, 0)
	return r
}

func msg1(obj uintptr, sel objc.SEL, a uintptr) uintptr {
	r, _, _ := syscall6(mtl.msgSend, obj, uintptr(sel), a, 0, 0, 0)
	return r
}

func msg2(obj uintptr, sel objc.SEL, a, b uintptr) uintptr {
	r, _, _ := syscall6(mtl.msgSend, obj, uintptr(sel), a, b, 0, 0)
	return r
}

func msg3(obj uintptr, sel objc.SEL, a, b, c uintptr) uintptr {
	r, _, _ := syscall6(mtl.msgSend, obj, uintptr(sel), a, b, c, 0)
	return r
}

func send(obj uintptr, sel string, args ...any) uintptr {
	return uintptr(objc.ID(obj).Send(objc.RegisterName(sel), args...))
}

func class(name string) uintptr { return uintptr(objc.GetClass(name)) }

func mrelease(obj *uintptr) {
	if *obj != 0 {
		msg(*obj, selRelease)
		*obj = 0
	}
}

func nsString(s string) uintptr {
	b := append([]byte(s), 0)
	return send(class("NSString"), "stringWithUTF8String:", unsafe.Pointer(&b[0]))
}

func goString(ns uintptr) string {
	if ns == 0 {
		return ""
	}
	p := send(ns, "UTF8String")
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(ptrOf(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptrOf(p)), n))
}

// ptrOf turns an address owned by native code into a pointer.
func ptrOf(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// mtlSize is MTLSize. On arm64 a 24-byte struct argument goes by reference,
// so dispatch passes pointers to these.
type mtlSize struct{ W, H, D uint }

// MetalDecoder decodes on the presenting surface's Metal device into a pool
// of IOSurface-backed textures, on the surface's own command queue so the
// present that samples a picture runs after its decode with no fence. Bytes
// only go CPU to GPU: records are parsed straight into shared buffers.
type MetalDecoder struct {
	device, queue uintptr
	ownQueue      bool
	plan          *plan
	cfg           decode.Config
	parser        *Parser
	pipes         [4]uintptr // clear, dequant, idwt, yuv2rgb
	coef, bands   uintptr
	slots         [outputPool]metalSlot
	next          int
	f             *metalFrame
	maxFrame      int
}

type metalSlot struct {
	up, recs uintptr // shared buffers
	upMem    []byte
	recMem   []Rec
	surface  uintptr // IOSurfaceRef
	tex      uintptr
	cb       uintptr // the last command buffer that used the slot, retained
}

// metalFrame holds what per-frame calls point at, on the heap.
type metalFrame struct {
	p               params
	groups, threads mtlSize
}

const (
	pipeClear = iota
	pipeDequant
	pipeIDWT
	pipeYUV2RGB
)

// OpenMetalDecoder opens a decoder on an id<MTLDevice> and its
// id<MTLCommandQueue> (gpusurface's Device and Queue). A zero queue makes
// one of the decoder's own.
func OpenMetalDecoder(device, queue uintptr, cfg decode.Config) (*MetalDecoder, error) {
	if err := loadMetal(); err != nil {
		return nil, err
	}
	if cfg.ChunkLen < MinChunkLen {
		return nil, fmt.Errorf("pyrowave: chunk length %d below %d", cfg.ChunkLen, MinChunkLen)
	}
	if cfg.Chroma.TenBit() {
		return nil, fmt.Errorf("%w: 10-bit output on Metal needs a BGR10A2 surface format", decode.ErrUnsupported)
	}
	l, err := NewLayout(cfg.Width, cfg.Height, !cfg.Chroma.Full())
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := syscall6(mtl.poolPush, 0, 0, 0, 0, 0, 0)
	defer syscall6(mtl.poolPop, pool, 0, 0, 0, 0, 0)
	if device == 0 {
		r, _, _ := syscall6(mtl.createDevice, 0, 0, 0, 0, 0, 0)
		if r == 0 {
			return nil, errors.New("pyrowave: no Metal device")
		}
		device = r
	} else {
		msg(device, selRetain)
	}
	d := &MetalDecoder{device: device, queue: queue, plan: newPlan(l), cfg: cfg, parser: NewParser(l, cfg.ChunkLen), f: &metalFrame{}}
	if queue == 0 {
		d.queue = msg(device, selNewCommandQueue)
		d.ownQueue = true
	} else {
		msg(queue, selRetain)
	}
	if err := d.init(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *MetalDecoder) newBuffer(size int, storage uintptr) (uintptr, error) {
	b := msg2(d.device, selNewBuffer, uintptr((size+15)&^15), storage)
	if b == 0 {
		return 0, fmt.Errorf("pyrowave: Metal buffer of %d bytes", size)
	}
	return b, nil
}

func (d *MetalDecoder) init() error {
	var nsErr uintptr
	lib := send(d.device, "newLibraryWithSource:options:error:", nsString(shaderSource("msl/decode.metal")), uintptr(0), unsafe.Pointer(&nsErr))
	if lib == 0 {
		return fmt.Errorf("pyrowave: Metal shader compile: %s", goString(send(nsErr, "localizedDescription")))
	}
	defer mrelease(&lib)
	for i, name := range []string{"clear", "dequant", "idwt", "yuv2rgb"} {
		fn := send(lib, "newFunctionWithName:", nsString(name))
		if fn == 0 {
			return fmt.Errorf("pyrowave: Metal function %s missing", name)
		}
		nsErr = 0
		d.pipes[i] = send(d.device, "newComputePipelineStateWithFunction:error:", fn, unsafe.Pointer(&nsErr))
		mrelease(&fn)
		if d.pipes[i] == 0 {
			return fmt.Errorf("pyrowave: Metal pipeline %s: %s", name, goString(send(nsErr, "localizedDescription")))
		}
	}
	l := d.plan.L
	var err error
	if d.coef, err = d.newBuffer(4*int(d.plan.total), storagePrivate); err != nil {
		return err
	}
	if d.bands, err = d.newBuffer(4*len(d.plan.table), storageShared); err != nil {
		return err
	}
	copy(unsafe.Slice((*uint32)(ptrOf(msg(d.bands, selContents))), len(d.plan.table)), d.plan.table)
	d.maxFrame = maxSlices * maxShards * d.cfg.ChunkLen
	nrec := cap(d.parser.Recs)
	for i := range d.slots {
		s := &d.slots[i]
		if s.up, err = d.newBuffer(d.maxFrame, storageShared); err != nil {
			return err
		}
		s.upMem = unsafe.Slice((*byte)(ptrOf(msg(s.up, selContents))), d.maxFrame)
		if s.recs, err = d.newBuffer(16*nrec, storageShared); err != nil {
			return err
		}
		s.recMem = unsafe.Slice((*Rec)(ptrOf(msg(s.recs, selContents))), nrec)
		if s.surface, s.tex, err = d.newSurface(l.Width, l.Height); err != nil {
			return err
		}
	}
	return nil
}

func (d *MetalDecoder) newSurface(w, h int) (uintptr, uintptr, error) {
	key := func(name string) uintptr { return apple.Const(apple.IOSurface, name) }
	props := apple.Dict(
		key("kIOSurfaceWidth"), apple.Int(int64(w)),
		key("kIOSurfaceHeight"), apple.Int(int64(h)),
		key("kIOSurfaceBytesPerElement"), apple.Int(4),
		key("kIOSurfaceBytesPerRow"), apple.Int(int64((w*4+63)&^63)),
		key("kIOSurfacePixelFormat"), apple.Int(fourccBGRA),
	)
	defer apple.CFRelease(props)
	surf, _, _ := syscall6(mtl.ioCreate, props, 0, 0, 0, 0, 0)
	if surf == 0 {
		return 0, 0, errors.New("pyrowave: IOSurfaceCreate failed")
	}
	desc := send(class("MTLTextureDescriptor"), "texture2DDescriptorWithPixelFormat:width:height:mipmapped:", uintptr(formatBGRA8), uintptr(w), uintptr(h), false)
	send(desc, "setUsage:", uintptr(usageRead|usageWrite))
	tex := send(d.device, "newTextureWithDescriptor:iosurface:plane:", desc, surf, uintptr(0))
	if tex == 0 {
		apple.CFRelease(surf)
		return 0, 0, errors.New("pyrowave: IOSurface texture")
	}
	return surf, tex, nil
}

func (d *MetalDecoder) dispatch(enc uintptr, pipe int, gx, gy, threads int) {
	f := d.f
	msg1(enc, selSetPipeline, d.pipes[pipe])
	msg3(enc, selSetBytes, uintptr(unsafe.Pointer(&f.p)), unsafe.Sizeof(f.p), 4)
	f.groups = mtlSize{uint(max(gx, 1)), uint(max(gy, 1)), 1}
	f.threads = mtlSize{uint(threads), 1, 1}
	if threads == 256 && pipe != pipeClear {
		f.threads = mtlSize{16, 16, 1}
	}
	msg2(enc, selDispatch, uintptr(unsafe.Pointer(&f.groups)), uintptr(unsafe.Pointer(&f.threads)))
}

// Decode decodes one chunked frame (Reassembler.PartialFrame's output) and
// returns its IOSurface. It commits without waiting. Frame.Latency is not
// measured (0).
func (d *MetalDecoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	if len(au) > d.maxFrame {
		return decode.Frame{}, fmt.Errorf("pyrowave: %d-byte frame over the %d-byte limit", len(au), d.maxFrame)
	}
	s := &d.slots[d.next]
	d.next = (d.next + 1) % outputPool
	if s.cb != 0 {
		msg(s.cb, selWaitUntilCompleted)
		mrelease(&s.cb)
	}
	hdr, _, err := d.parser.Parse(au, s.upMem)
	if err != nil {
		return decode.Frame{}, err
	}
	nrec := copy(s.recMem, d.parser.Recs)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := syscall6(mtl.poolPush, 0, 0, 0, 0, 0, 0)
	defer syscall6(mtl.poolPop, pool, 0, 0, 0, 0, 0)
	cb := msg(d.queue, selCommandBuffer)
	enc := msg(cb, selComputeEncoder)
	msg3(enc, selSetBuffer, d.coef, 0, 0)
	msg3(enc, selSetBuffer, d.bands, 0, 1)
	msg3(enc, selSetBuffer, s.recs, 0, 2)
	msg3(enc, selSetBuffer, s.up, 0, 3)
	msg2(enc, selSetTexture, s.tex, 0)

	f, pl := d.f, d.plan
	f.p = pl.baseParams()
	f.p.Kr, f.p.Kb = matrixWeights(hdr.Matrix)
	n := (int(pl.coded) + 3) / 4
	gx, gy := groups((n + 255) / 256)
	f.p.GroupsX, f.p.OutOff[0] = uint32(gx), pl.coded
	d.dispatch(enc, pipeClear, gx, gy, 256)
	if nrec > 0 {
		gx, gy = groups(nrec)
		f.p.GroupsX, f.p.TotalBlocks = uint32(gx), uint32(nrec)
		d.dispatch(enc, pipeDequant, gx, gy, 128)
	}
	for i := len(pl.passes) - 1; i >= 0; i-- {
		ps := pl.passes[i]
		f.p.PlaneW, f.p.PlaneH = int32(ps.w), int32(ps.h)
		f.p.InOff, f.p.OutOff = ps.in, ps.out
		d.dispatch(enc, pipeIDWT, (ps.w+31)/32, (ps.h+31)/32, 256)
	}
	f.p.PlaneW, f.p.PlaneH = int32(pl.L.AlignedWidth), int32(pl.L.AlignedHeight)
	f.p.OutOff = [4]uint32{pl.planeOff[0], pl.planeOff[1], pl.planeOff[2], 0}
	d.dispatch(enc, pipeYUV2RGB, (pl.L.Width+15)/16, (pl.L.Height+15)/16, 256)
	msg(enc, selEndEncoding)
	s.cb = msg(cb, selRetain)
	msg(cb, selCommit)
	return decode.Frame{Surface: s.surface, Format: capture.FormatBGRA8, Width: pl.L.Width, Height: pl.L.Height, PTS: pts}, nil
}

// Wait blocks until the last decode has finished and returns its GPU time
// in seconds.
func (d *MetalDecoder) Wait() float64 {
	s := &d.slots[(d.next+outputPool-1)%outputPool]
	if s.cb == 0 {
		return 0
	}
	msg(s.cb, selWaitUntilCompleted)
	return objc.Send[float64](objc.ID(s.cb), selGPUEnd) - objc.Send[float64](objc.ID(s.cb), selGPUStart)
}

// Release is a no-op: output surfaces are reused round robin, three deep.
func (d *MetalDecoder) Release(decode.Frame) {}

func (d *MetalDecoder) Close() error {
	if d.device == 0 {
		return nil
	}
	for i := range d.slots {
		s := &d.slots[i]
		if s.cb != 0 {
			msg(s.cb, selWaitUntilCompleted)
			mrelease(&s.cb)
		}
		mrelease(&s.tex)
		if s.surface != 0 {
			apple.CFRelease(s.surface)
			s.surface = 0
		}
		mrelease(&s.up)
		mrelease(&s.recs)
	}
	for i := range d.pipes {
		mrelease(&d.pipes[i])
	}
	mrelease(&d.coef)
	mrelease(&d.bands)
	mrelease(&d.queue)
	mrelease(&d.device)
	return nil
}
