//go:build darwin && !ios

package gpusurface

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Every call on the hot path goes through syscall6 (ccall_darwin.go) with
// integer arguments, which allocates nothing; objc.Send and functions made with
// purego.RegisterFunc box their arguments and are kept to setup and to the
// presented handler, which runs on Core Animation's thread.

const (
	pixelFormatR8Unorm     = 10
	pixelFormatR16Unorm    = 20
	pixelFormatRG8Unorm    = 30
	pixelFormatRG16Unorm   = 60
	pixelFormatBGRA8Unorm  = 80
	textureUsageShaderRead = 1
	loadActionDontCare     = 0
	storeActionStore       = 1
	primitiveTriangle      = 3

	// IOSurface pixel formats (CoreVideo's four-character codes).
	fourccBGRA = 0x42475241 // 'BGRA'
	fourcc420v = 0x34323076 // '420v', NV12 video range
	fourccx420 = 0x78343230 // 'x420', P010 video range

	viewWidthSizable   = 2           // NSViewWidthSizable
	viewHeightSizable  = 16          // NSViewHeightSizable
	layerWidthSizable  = 2           // kCALayerWidthSizable
	layerHeightSizable = 16          // kCALayerHeightSizable
	windowBelow        = ^uintptr(0) // NSWindowBelow, -1
)

type cgSize struct{ W, H float64 }
type cgRect struct{ X, Y, W, H float64 }

var lib struct {
	once sync.Once
	err  error

	msgSend, poolPush, poolPop, machAbsoluteTime uintptr
	ioWidth, ioHeight, ioFormat                  uintptr
	ioPlaneWidth, ioPlaneHeight                  uintptr
	numer, denom                                 uint64

	createDevice func() uintptr
	msgSetSize   func(obj uintptr, sel objc.SEL, s cgSize)
	msgRect      func(obj uintptr, sel objc.SEL) cgRect
	msgSetRect   func(obj uintptr, sel objc.SEL, r cgRect)
	msgFloat     func(obj uintptr, sel objc.SEL) float64
	msgSetFloat  func(obj uintptr, sel objc.SEL, v float64)
	msgInitRect  func(obj uintptr, sel objc.SEL, r cgRect) uintptr

	presented objc.Block
}

var (
	selNextDrawable     = objc.RegisterName("nextDrawable")
	selCommandBuffer    = objc.RegisterName("commandBuffer")
	selTexture          = objc.RegisterName("texture")
	selSetTexture       = objc.RegisterName("setTexture:")
	selRenderEncoder    = objc.RegisterName("renderCommandEncoderWithDescriptor:")
	selSetPipeline      = objc.RegisterName("setRenderPipelineState:")
	selSetFragmentTex   = objc.RegisterName("setFragmentTexture:atIndex:")
	selSetFragmentBytes = objc.RegisterName("setFragmentBytes:length:atIndex:")
	selDraw             = objc.RegisterName("drawPrimitives:vertexStart:vertexCount:")
	selEndEncoding      = objc.RegisterName("endEncoding")
	selDrawableID       = objc.RegisterName("drawableID")
	selAddPresented     = objc.RegisterName("addPresentedHandler:")
	selPresentDrawable  = objc.RegisterName("presentDrawable:")
	selCommit           = objc.RegisterName("commit")
	selPresentedTime    = objc.RegisterName("presentedTime")
	selLayer            = objc.RegisterName("layer")
	selRelease          = objc.RegisterName("release")
	selNewTexture       = objc.RegisterName("newTextureWithDescriptor:iosurface:plane:")
	selSetUsage         = objc.RegisterName("setUsage:")
)

func load() error {
	lib.once.Do(func() {
		open := func(path string) uintptr {
			h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
			if err != nil && lib.err == nil {
				lib.err = fmt.Errorf("gpusurface: %w", err)
			}
			return h
		}
		objcLib := open("/usr/lib/libobjc.A.dylib")
		metal := open("/System/Library/Frameworks/Metal.framework/Metal")
		open("/System/Library/Frameworks/QuartzCore.framework/QuartzCore")
		io := open("/System/Library/Frameworks/IOSurface.framework/IOSurface")
		system := open("/usr/lib/libSystem.B.dylib")
		if lib.err != nil {
			return
		}
		sym := func(h uintptr, name string) uintptr {
			p, err := purego.Dlsym(h, name)
			if err != nil && lib.err == nil {
				lib.err = fmt.Errorf("gpusurface: %w", err)
			}
			return p
		}
		lib.msgSend = sym(objcLib, "objc_msgSend")
		lib.poolPush = sym(objcLib, "objc_autoreleasePoolPush")
		lib.poolPop = sym(objcLib, "objc_autoreleasePoolPop")
		lib.ioWidth = sym(io, "IOSurfaceGetWidth")
		lib.ioHeight = sym(io, "IOSurfaceGetHeight")
		lib.ioFormat = sym(io, "IOSurfaceGetPixelFormat")
		lib.ioPlaneWidth = sym(io, "IOSurfaceGetWidthOfPlane")
		lib.ioPlaneHeight = sym(io, "IOSurfaceGetHeightOfPlane")
		lib.machAbsoluteTime = sym(system, "mach_absolute_time")
		timebase := sym(system, "mach_timebase_info")
		create := sym(metal, "MTLCreateSystemDefaultDevice")
		if lib.err != nil {
			return
		}
		var tb struct{ numer, denom uint32 }
		purego.SyscallN(timebase, uintptr(unsafe.Pointer(&tb)))
		lib.numer, lib.denom = uint64(tb.numer), uint64(tb.denom)
		purego.RegisterFunc(&lib.createDevice, create)
		purego.RegisterFunc(&lib.msgSetSize, lib.msgSend)
		purego.RegisterFunc(&lib.msgRect, lib.msgSend)
		purego.RegisterFunc(&lib.msgSetRect, lib.msgSend)
		purego.RegisterFunc(&lib.msgFloat, lib.msgSend)
		purego.RegisterFunc(&lib.msgSetFloat, lib.msgSend)
		purego.RegisterFunc(&lib.msgInitRect, lib.msgSend)
		// One block for every drawable of every surface: purego callbacks
		// are never freed.
		lib.presented = objc.NewBlock(onPresented)
	})
	return lib.err
}

// Now returns the time in nanoseconds of the clock Timing uses: the host's
// mach time, which Core Animation's presentedTime counts in seconds.
func Now() int64 {
	t, _, _ := syscall6(lib.machAbsoluteTime, 0, 0, 0, 0, 0, 0)
	return int64(uint64(t) * lib.numer / lib.denom)
}

func msg(obj uintptr, sel objc.SEL) uintptr {
	r, _, _ := syscall6(lib.msgSend, obj, uintptr(sel), 0, 0, 0, 0)
	return r
}

func msg1(obj uintptr, sel objc.SEL, a uintptr) uintptr {
	r, _, _ := syscall6(lib.msgSend, obj, uintptr(sel), a, 0, 0, 0)
	return r
}

func msg2(obj uintptr, sel objc.SEL, a, b uintptr) uintptr {
	r, _, _ := syscall6(lib.msgSend, obj, uintptr(sel), a, b, 0, 0)
	return r
}

func msg3(obj uintptr, sel objc.SEL, a, b, c uintptr) uintptr {
	r, _, _ := syscall6(lib.msgSend, obj, uintptr(sel), a, b, c, 0)
	return r
}

func send(obj uintptr, sel string, args ...any) uintptr {
	return uintptr(objc.ID(obj).Send(objc.RegisterName(sel), args...))
}

func class(name string) uintptr { return uintptr(objc.GetClass(name)) }

func release(obj *uintptr) {
	if *obj != 0 {
		msg(*obj, selRelease)
		*obj = 0
	}
}

// yuvParams converts the video range samples of a frame to full range: Y'
// = (y - YOffset) * YScale, Cb, Cr = (c - COffset) * CScale.
type yuvParams struct{ YOffset, YScale, COffset, CScale float32 }

var (
	params8  = yuvParams{16.0 / 255, 255.0 / 219, 128.0 / 255, 255.0 / 224}
	params10 = yuvParams{64 * 64 / 65535.0, 65535.0 / (876 * 64), 512 * 64 / 65535.0, 65535.0 / (896 * 64)}
)

// frameTextures is a frame of the pool wrapped as textures: its BGRA, or
// its luma and chroma.
type frameTextures struct {
	surface uintptr
	tex     [2]uintptr
	format  Format
	params  *yuvParams
}

// wrapCache bounds the frames held wrapped: a decoder's pool is smaller.
const wrapCache = 16

// pendingRing bounds the drawables between Present and their presented
// handler.
const pendingRing = 64

type pending struct {
	drawableID int64 // atomic
	index      int64 // atomic: the Timing's index, or -1
	tag        int64 // atomic
	submitted  int64 // atomic
}

// Surface presents frames into a window.
type Surface struct {
	device, queue        uintptr
	layer                uintptr
	host                 uintptr // the view holding layer, with Composite
	pass, attachment     uintptr
	rgbPipeline, yuvPipe uintptr
	cache                [wrapCache]frameTextures
	nextSlot             int
	pending              [pendingRing]pending
	timings              timings
	onPresented          atomic.Pointer[func(tag, submitted, presented int64)]
	width, height        int
	scale                float64
}

var surfaces sync.Map // layer → *Surface, for the presented handler

const shaderSource = `
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
constexpr sampler smp(filter::linear, address::clamp_to_edge);
fragment float4 fs_rgb(VOut in [[stage_in]], texture2d<float> t [[texture(0)]]) {
	return float4(t.sample(smp, in.uv).rgb, 1);
}
struct YUVParams { float yOffset, yScale, cOffset, cScale; };
fragment float4 fs_yuv(VOut in [[stage_in]], texture2d<float> y [[texture(0)]], texture2d<float> c [[texture(1)]],
		constant YUVParams &p [[buffer(0)]]) {
	float l = (y.sample(smp, in.uv).r - p.yOffset) * p.yScale;
	float2 uv = (c.sample(smp, in.uv).rg - p.cOffset) * p.cScale;
	// BT.709
	return float4(l + 1.5748 * uv.y, l - 0.1873 * uv.x - 0.4681 * uv.y, l + 1.8556 * uv.x, 1);
}
`

// New makes a surface filling opts.Window, an NSWindow*, on the main
// thread. Present may then be called from one other thread.
func New(opts Options) (s *Surface, err error) {
	if err := load(); err != nil {
		return nil, err
	}
	if opts.Window == 0 {
		return nil, errors.New("gpusurface: no window")
	}
	s = &Surface{}
	s.timings.recs = make([]Timing, opts.Timings)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(lib.poolPush)
	defer purego.SyscallN(lib.poolPop, pool)
	defer func() {
		if err != nil {
			s.Close()
			s = nil
		}
	}()
	if s.device = lib.createDevice(); s.device == 0 {
		return nil, errors.New("gpusurface: no Metal device")
	}
	s.queue = send(s.device, "newCommandQueue")
	if err = s.makePipelines(); err != nil {
		return nil, err
	}
	s.pass = send(send(class("MTLRenderPassDescriptor"), "renderPassDescriptor"), "retain")
	s.attachment = send(send(s.pass, "colorAttachments"), "objectAtIndexedSubscript:", uintptr(0))
	send(s.attachment, "setLoadAction:", uintptr(loadActionDontCare))
	send(s.attachment, "setStoreAction:", uintptr(storeActionStore))

	view := send(opts.Window, "contentView")
	scale := lib.msgFloat(opts.Window, objc.RegisterName("backingScaleFactor"))
	bounds := lib.msgRect(view, objc.RegisterName("bounds"))
	var parent uintptr
	if opts.Composite {
		// A view of its own at the back of the content view, as MyGo puts
		// web views under its surface view: AppKit keeps the sublayers of
		// a view's layer in its subviews' order, and moves a layer added
		// beside them.
		s.host = lib.msgInitRect(send(class("NSView"), "alloc"), objc.RegisterName("initWithFrame:"), bounds)
		send(s.host, "setAutoresizingMask:", uintptr(viewWidthSizable|viewHeightSizable))
		send(s.host, "setWantsLayer:", true)
		send(view, "addSubview:positioned:relativeTo:", s.host, windowBelow, uintptr(0))
		parent = send(s.host, "layer")
	} else {
		send(view, "setWantsLayer:", true)
		parent = send(view, "layer")
	}
	bounds.X, bounds.Y = 0, 0

	layer := send(send(class("CAMetalLayer"), "alloc"), "init")
	send(layer, "setDevice:", s.device)
	send(layer, "setPixelFormat:", uintptr(pixelFormatBGRA8Unorm))
	send(layer, "setFramebufferOnly:", true)
	// Two drawables: one on screen, one drawn. A third would only queue a
	// frame behind the one shown, a refresh of latency.
	send(layer, "setMaximumDrawableCount:", uintptr(2))
	// Drawables present once drawn, not at the next refresh.
	send(layer, "setDisplaySyncEnabled:", false)
	send(layer, "setPresentsWithTransaction:", false)
	send(layer, "setOpaque:", true)
	if opts.Composite {
		// The host view follows the window, and the layer it: the frame
		// changes on the main thread, the drawable size in Resize.
		send(layer, "setAutoresizingMask:", uintptr(layerWidthSizable|layerHeightSizable))
	}
	lib.msgSetRect(layer, objc.RegisterName("setFrame:"), bounds)
	lib.msgSetFloat(layer, objc.RegisterName("setContentsScale:"), scale)
	s.scale = scale
	s.width, s.height = int(bounds.W*scale), int(bounds.H*scale)
	lib.msgSetSize(layer, objc.RegisterName("setDrawableSize:"), cgSize{float64(s.width), float64(s.height)})
	send(parent, "addSublayer:", layer)
	send(class("CATransaction"), "flush")
	s.layer = layer
	surfaces.Store(layer, s)
	return s, nil
}

func (s *Surface) makePipelines() error {
	var errObj uintptr
	src := send(send(class("NSString"), "alloc"), "initWithUTF8String:", shaderSource+"\x00")
	defer release(&src)
	libObj := send(s.device, "newLibraryWithSource:options:error:", src, uintptr(0), uintptr(unsafe.Pointer(&errObj)))
	if libObj == 0 {
		return fmt.Errorf("gpusurface: cannot compile the shaders: %s", describe(errObj))
	}
	defer release(&libObj)
	pipeline := func(fs string) (uintptr, error) {
		desc := send(send(class("MTLRenderPipelineDescriptor"), "alloc"), "init")
		defer release(&desc)
		vfn := send(libObj, "newFunctionWithName:", nsString("vs"))
		ffn := send(libObj, "newFunctionWithName:", nsString(fs))
		defer release(&vfn)
		defer release(&ffn)
		send(desc, "setVertexFunction:", vfn)
		send(desc, "setFragmentFunction:", ffn)
		att := send(send(desc, "colorAttachments"), "objectAtIndexedSubscript:", uintptr(0))
		send(att, "setPixelFormat:", uintptr(pixelFormatBGRA8Unorm))
		var e uintptr
		p := send(s.device, "newRenderPipelineStateWithDescriptor:error:", desc, uintptr(unsafe.Pointer(&e)))
		if p == 0 {
			return 0, fmt.Errorf("gpusurface: cannot make the %s pipeline: %s", fs, describe(e))
		}
		return p, nil
	}
	var err error
	if s.rgbPipeline, err = pipeline("fs_rgb"); err != nil {
		return err
	}
	s.yuvPipe, err = pipeline("fs_yuv")
	return err
}

func nsString(s string) uintptr {
	return send(class("NSString"), "stringWithUTF8String:", s+"\x00")
}

func describe(err uintptr) string {
	if err == 0 {
		return "unknown error"
	}
	return objc.Send[string](objc.ID(send(err, "localizedDescription")), objc.RegisterName("UTF8String"))
}

// Device returns the surface's MTLDevice, and Queue its MTLCommandQueue:
// a producer drawing frames on the GPU commits on the queue, so that the
// surface reads them after its writes.
func (s *Surface) Device() uintptr { return s.device }
func (s *Surface) Queue() uintptr  { return s.queue }

// Size returns the size of the drawables in pixels.
func (s *Surface) Size() (w, h int) { return s.width, s.height }

// Resize makes the layer w x h pixels, as after the window's content view
// changed size. Call it from the thread that presents, between presents.
func (s *Surface) Resize(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("gpusurface: size %dx%d", w, h)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(lib.poolPush)
	defer purego.SyscallN(lib.poolPop, pool)
	if s.host == 0 {
		lib.msgSetRect(s.layer, objc.RegisterName("setFrame:"), cgRect{W: float64(w) / s.scale, H: float64(h) / s.scale})
	}
	lib.msgSetSize(s.layer, objc.RegisterName("setDrawableSize:"), cgSize{float64(w), float64(h)})
	send(class("CATransaction"), "flush")
	s.width, s.height = w, h
	return nil
}

// errNoDrawable is Present's error when the layer gave no drawable in time.
var errNoDrawable = errors.New("gpusurface: no drawable")

// OnPresented sets f to be called, on Core Animation's thread, once for
// each frame given to PresentTagged after it: with the frame's tag, the
// time Present was called and the time it reached the screen (0 when a
// newer frame replaced it first), on the clock Now reads. Set it before
// the first present.
func (s *Surface) OnPresented(f func(tag, submitted, presented int64)) {
	s.onPresented.Store(&f)
}

// Present draws the frame in surface, an IOSurfaceRef in BGRA ('BGRA'),
// NV12 ('420v') or P010 ('x420'), and presents it at once.
func (s *Surface) Present(surface uintptr) error { return s.PresentTagged(surface, 0) }

// PresentTagged is Present with a tag the OnPresented handler receives
// back for this frame.
func (s *Surface) PresentTagged(surface uintptr, tag int64) error {
	submitted := Now()
	// The autorelease pool belongs to the thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	f := s.wrap(surface)
	if f == nil {
		return ErrFormat
	}
	pool, _, _ := syscall6(lib.poolPush, 0, 0, 0, 0, 0, 0)
	d := msg(s.layer, selNextDrawable)
	if d == 0 {
		syscall6(lib.poolPop, pool, 0, 0, 0, 0, 0)
		return errNoDrawable
	}
	cb := msg(s.queue, selCommandBuffer)
	msg1(s.attachment, selSetTexture, msg(d, selTexture))
	enc := msg1(cb, selRenderEncoder, s.pass)
	if f.format == FormatBGRA8 {
		msg1(enc, selSetPipeline, s.rgbPipeline)
		msg2(enc, selSetFragmentTex, f.tex[0], 0)
	} else {
		msg1(enc, selSetPipeline, s.yuvPipe)
		msg2(enc, selSetFragmentTex, f.tex[0], 0)
		msg2(enc, selSetFragmentTex, f.tex[1], 1)
		msg3(enc, selSetFragmentBytes, uintptr(unsafe.Pointer(f.params)), unsafe.Sizeof(*f.params), 0)
	}
	msg3(enc, selDraw, primitiveTriangle, 0, 3)
	msg(enc, selEndEncoding)
	i := s.timings.start(submitted)
	if i >= 0 || s.onPresented.Load() != nil {
		id := int64(msg(d, selDrawableID))
		p := &s.pending[id%pendingRing]
		atomic.StoreInt64(&p.index, int64(i))
		atomic.StoreInt64(&p.tag, tag)
		atomic.StoreInt64(&p.submitted, submitted)
		atomic.StoreInt64(&p.drawableID, id)
		msg1(d, selAddPresented, uintptr(lib.presented))
	}
	msg1(cb, selPresentDrawable, d)
	msg(cb, selCommit)
	syscall6(lib.poolPop, pool, 0, 0, 0, 0, 0)
	if i >= 0 {
		s.timings.recs[i].Encoded = Now()
	}
	return nil
}

// onPresented is the presented handler of the drawables of every surface,
// on Core Animation's thread.
func onPresented(_ objc.Block, d objc.ID) {
	layer := msg(uintptr(d), selLayer)
	v, ok := surfaces.Load(layer)
	if !ok {
		return
	}
	s := v.(*Surface)
	id := int64(msg(uintptr(d), selDrawableID))
	p := &s.pending[id%pendingRing]
	if atomic.LoadInt64(&p.drawableID) != id {
		return
	}
	at := objc.Send[float64](d, selPresentedTime)
	var presented int64
	if at > 0 {
		presented = int64(at * 1e9)
	}
	s.timings.resolve(int(atomic.LoadInt64(&p.index)), presented)
	if f := s.onPresented.Load(); f != nil {
		(*f)(atomic.LoadInt64(&p.tag), atomic.LoadInt64(&p.submitted), presented)
	}
}

// wrap returns the textures of the frame in surface, wrapping it on its
// first present.
func (s *Surface) wrap(surface uintptr) *frameTextures {
	for i := range s.cache {
		if s.cache[i].surface == surface && surface != 0 {
			return &s.cache[i]
		}
	}
	if surface == 0 {
		return nil
	}
	fourcc, _, _ := purego.SyscallN(lib.ioFormat, surface)
	var f frameTextures
	switch fourcc {
	case fourccBGRA:
		f.format = FormatBGRA8
		f.tex[0] = s.planeTexture(surface, -1, pixelFormatBGRA8Unorm)
	case fourcc420v:
		f.format, f.params = FormatNV12, &params8
		f.tex[0] = s.planeTexture(surface, 0, pixelFormatR8Unorm)
		f.tex[1] = s.planeTexture(surface, 1, pixelFormatRG8Unorm)
	case fourccx420:
		f.format, f.params = FormatP010, &params10
		f.tex[0] = s.planeTexture(surface, 0, pixelFormatR16Unorm)
		f.tex[1] = s.planeTexture(surface, 1, pixelFormatRG16Unorm)
	default:
		return nil
	}
	if f.tex[0] == 0 || (f.format != FormatBGRA8 && f.tex[1] == 0) {
		release(&f.tex[0])
		release(&f.tex[1])
		return nil
	}
	f.surface = surface
	slot := &s.cache[s.nextSlot]
	s.nextSlot = (s.nextSlot + 1) % wrapCache
	// Command buffers in flight retain the textures they read.
	release(&slot.tex[0])
	release(&slot.tex[1])
	*slot = f
	return slot
}

// planeTexture wraps a plane of surface, or the whole of it for plane -1,
// as a texture of the device, with no copy.
func (s *Surface) planeTexture(surface uintptr, plane int, format uintptr) uintptr {
	var w, h uintptr
	if plane < 0 {
		w, _, _ = purego.SyscallN(lib.ioWidth, surface)
		h, _, _ = purego.SyscallN(lib.ioHeight, surface)
		plane = 0
	} else {
		w, _, _ = purego.SyscallN(lib.ioPlaneWidth, surface, uintptr(plane))
		h, _, _ = purego.SyscallN(lib.ioPlaneHeight, surface, uintptr(plane))
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool, _, _ := purego.SyscallN(lib.poolPush)
	defer purego.SyscallN(lib.poolPop, pool)
	d := send(class("MTLTextureDescriptor"), "texture2DDescriptorWithPixelFormat:width:height:mipmapped:", format, w, h, false)
	msg1(d, selSetUsage, textureUsageShaderRead)
	return msg3(s.device, selNewTexture, d, surface, uintptr(plane))
}

// done reports whether every frame recorded has its Presented.
func (t *timings) done() bool { return t.resolved.Load() >= t.next.Load() }

// Close waits for the frames presented to reach the screen, a second at
// most, and frees the surface.
func (s *Surface) Close() {
	deadline := time.Now().Add(time.Second)
	for !s.timings.done() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.layer != 0 {
		surfaces.Delete(s.layer)
		send(s.layer, "removeFromSuperlayer")
	}
	if s.host != 0 {
		send(s.host, "removeFromSuperview")
	}
	for i := range s.cache {
		release(&s.cache[i].tex[0])
		release(&s.cache[i].tex[1])
	}
	for _, p := range []*uintptr{&s.host, &s.layer, &s.pass, &s.rgbPipeline, &s.yuvPipe, &s.queue, &s.device} {
		release(p)
	}
}
