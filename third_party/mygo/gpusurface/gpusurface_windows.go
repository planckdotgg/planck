//go:build windows

package gpusurface

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo/internal/gpu/d3d11/device"
)

const (
	release = 2
	// ID3D11Device
	devCreateBuffer             = 3
	devCreateShaderResourceView = 7
	devCreateRenderTargetView   = 9
	devCreateVertexShader       = 12
	devCreatePixelShader        = 15
	devCreateSamplerState       = 23
	// ID3D11DeviceContext
	ctxPSSetShaderResources   = 8
	ctxPSSetShader            = 9
	ctxPSSetSamplers          = 10
	ctxVSSetShader            = 11
	ctxDraw                   = 13
	ctxPSSetConstantBuffers   = 16
	ctxIASetInputLayout       = 17
	ctxIASetPrimitiveTopology = 24
	ctxOMSetRenderTargets     = 33
	ctxRSSetViewports         = 44
	ctxFlush                  = 111
	// ID3D11Texture2D
	texGetDesc = 10
	// DXGI
	dxgiDevGetAdapter      = 7
	adapterGetDesc         = 8
	dxgiDevSetMaxLatency   = 12
	dxgiGetParent          = 6
	factoryMakeWindowAssoc = 8
	factoryCreateSwapChain = 15 // CreateSwapChainForHwnd
	factory5CheckFeature   = 28
	scPresent              = 8
	scGetBuffer            = 9

	formatB8G8R8A8Unorm = 87
	formatR8Unorm       = 61
	formatR8G8Unorm     = 49
	formatR16Unorm      = 56
	formatR16G16Unorm   = 35
	formatNV12          = 103
	formatP010          = 104

	swapEffectFlipDiscard = 4
	usageRenderTarget     = 0x20
	swapChainTearing      = 0x800 // DXGI_SWAP_CHAIN_FLAG_ALLOW_TEARING
	presentTearing        = 0x200 // DXGI_PRESENT_ALLOW_TEARING
	mwaNoAltEnter         = 2
	topologyTriangleList  = 4
	dimensionTexture2D    = 4
	bindConstantBuffer    = 0x4
	usageImmutable        = 1

	// swapChainBuffers: one on screen, one queued for DWM and one drawn,
	// so that a frame replaces the one queued rather than wait for the
	// screen to give its buffer back (as MyGo's uncapped renderer).
	swapChainBuffers = 3
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	iidIDXGIDevice     = guid{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	iidIDXGIDevice1    = guid{0x77db970f, 0x6276, 0x48ba, [8]byte{0xba, 0x28, 0x07, 0x01, 0x43, 0xb4, 0x39, 0x2c}}
	iidIDXGIFactory2   = guid{0x50c83a1c, 0xe072, 0x4c48, [8]byte{0x87, 0xb0, 0x36, 0x30, 0xfa, 0x36, 0xa6, 0xd0}}
	iidIDXGIFactory5   = guid{0x7632e1f5, 0xee65, 0x4dca, [8]byte{0x87, 0xfd, 0x84, 0xcd, 0x75, 0xf8, 0x83, 0x8d}}
	iidID3D11Texture2D = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

var (
	kernel32       = syscall.NewLazyDLL(device.SystemDir() + `\kernel32.dll`)
	procQPC        = kernel32.NewProc("QueryPerformanceCounter")
	procQPF        = kernel32.NewProc("QueryPerformanceFrequency")
	procD3DCompile = syscall.NewLazyDLL(device.SystemDir() + `\d3dcompiler_47.dll`).NewProc("D3DCompile")
	qpcFrequency   int64
)

func init() { procQPF.Call(uintptr(unsafe.Pointer(&qpcFrequency))) }

// Now returns the time in nanoseconds of the clock Timing uses: the
// performance counter.
func Now() int64 {
	var t int64
	syscall.SyscallN(procQPC.Addr(), uintptr(unsafe.Pointer(&t)))
	return qpcToNs(t)
}

func qpcToNs(t int64) int64 {
	return t/qpcFrequency*1e9 + t%qpcFrequency*1e9/qpcFrequency
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// call calls method i of a COM object; the pointers passed as uintptr stay
// valid until it returns.
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

// fastCall is call for the hot path, with up to five arguments: call's
// variadic arguments, which go:uintptrescapes moves to the heap, cost an
// allocation a call. The pointers passed must not point into a goroutine
// stack, which may move before the call: only to the heap or C memory.
func fastCall(obj uintptr, i int, a, b, c, d, e uintptr) uintptr {
	vtbl := *(*uintptr)(ptr(obj))
	fn := *(*uintptr)(unsafe.Add(ptr(vtbl), i*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, obj, a, b, c, d, e)
	return r
}

func failed(hr uintptr) bool { return int32(uint32(hr)) < 0 }

func free(obj *uintptr) {
	if *obj != 0 {
		call(*obj, release)
		*obj = 0
	}
}

type swapChainDesc1 struct {
	Width, Height, Format          uint32
	Stereo                         int32
	SampleCount, SampleQuality     uint32
	BufferUsage, BufferCount       uint32
	Scaling, SwapEffect, AlphaMode uint32
	Flags                          uint32
}

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type srvDesc struct {
	Format, ViewDimension uint32
	MostDetailedMip       uint32
	MipLevels             uint32
	_                     [2]uint32
}

type bufferDesc struct {
	ByteWidth, Usage, BindFlags, CPUAccessFlags, MiscFlags, StructureByteStride uint32
}

type subresourceData struct {
	SysMem                   uintptr
	SysMemPitch, SysMemSlice uint32
}

type samplerDesc struct {
	Filter                       uint32
	AddressU, AddressV, AddressW uint32
	MipLODBias                   float32
	MaxAnisotropy                uint32
	ComparisonFunc               uint32
	BorderColor                  [4]float32
	MinLOD, MaxLOD               float32
}

type viewport struct{ X, Y, W, H, MinDepth, MaxDepth float32 }

// yuvParams converts the video range samples of a frame to full range: Y'
// = (y - YOffset) * YScale, Cb, Cr = (c - COffset) * CScale.
type yuvParams struct{ YOffset, YScale, COffset, CScale float32 }

var (
	params8  = yuvParams{16.0 / 255, 255.0 / 219, 128.0 / 255, 255.0 / 224}
	params10 = yuvParams{64 * 64 / 65535.0, 65535.0 / (876 * 64), 512 * 64 / 65535.0, 65535.0 / (896 * 64)}
)

const shaderSource = `
struct VOut { float4 pos : SV_Position; float2 uv : TEXCOORD0; };
VOut vs(uint id : SV_VertexID) {
	float2 p = float2((id << 1) & 2, id & 2);
	VOut o;
	o.pos = float4(p * float2(2, -2) + float2(-1, 1), 0, 1);
	o.uv = p;
	return o;
}
Texture2D<float4> t0 : register(t0);
Texture2D<float4> t1 : register(t1);
SamplerState smp : register(s0);
cbuffer YUVParams : register(b0) { float yOffset, yScale, cOffset, cScale; };
float4 ps_rgb(VOut i) : SV_Target { return float4(t0.Sample(smp, i.uv).rgb, 1); }
float4 ps_yuv(VOut i) : SV_Target {
	float l = (t0.Sample(smp, i.uv).r - yOffset) * yScale;
	float2 c = (t1.Sample(smp, i.uv).rg - cOffset) * cScale;
	// BT.709
	return float4(l + 1.5748 * c.y, l - 0.1873 * c.x - 0.4681 * c.y, l + 1.8556 * c.x, 1);
}
`

// frameViews is a frame of the pool with views of it: its BGRA, or its
// luma and chroma.
type frameViews struct {
	tex    uintptr
	srv    [2]uintptr
	format Format
	params uintptr // constant buffer
}

const wrapCache = 16

// Surface presents frames into a window.
type Surface struct {
	hwnd                 uintptr
	device, ctx, factory uintptr
	swapChain, rtv       uintptr
	vs, psRGB, psYUV     uintptr
	sampler              uintptr
	params8, params10    uintptr
	vp                   viewport
	tearing              bool
	adapterName          string
	width, height        int

	cache    [wrapCache]frameViews
	nextSlot int
	noViews  [2]uintptr

	// timings has no Presented: DXGI's frame statistics, read after each
	// present, gave times before the frame was submitted, composed by DWM,
	// in a multiplane overlay and across adapters alike.
	timings timings
}

// New makes a surface with a swap chain of its own on opts.Window, an
// HWND, which has none.
func New(opts Options) (s *Surface, err error) {
	if opts.Window == 0 {
		return nil, errors.New("gpusurface: no window")
	}
	s = &Surface{hwnd: opts.Window}
	s.timings.recs = make([]Timing, opts.Timings)
	defer func() {
		if err != nil {
			s.Close()
			s = nil
		}
	}()
	if opts.Adapter == "" {
		s.device, s.ctx, err = device.NewOnGPU()
	} else {
		s.device, s.ctx, err = newDeviceOn(opts.Adapter)
	}
	if err != nil {
		return nil, err
	}
	var dxgiDev, adapter uintptr
	if failed(call(s.device, 0, uintptr(unsafe.Pointer(&iidIDXGIDevice)), uintptr(unsafe.Pointer(&dxgiDev)))) {
		return nil, errors.New("gpusurface: no DXGI device")
	}
	defer free(&dxgiDev)
	var dxgiDev1 uintptr
	if !failed(call(s.device, 0, uintptr(unsafe.Pointer(&iidIDXGIDevice1)), uintptr(unsafe.Pointer(&dxgiDev1)))) {
		// The CPU queues one frame ahead of the GPU at most.
		call(dxgiDev1, dxgiDevSetMaxLatency, 1)
		free(&dxgiDev1)
	}
	if failed(call(dxgiDev, dxgiDevGetAdapter, uintptr(unsafe.Pointer(&adapter)))) {
		return nil, errors.New("gpusurface: no adapter")
	}
	defer free(&adapter)
	var ad adapterDesc1 // DXGI_ADAPTER_DESC is its start
	call(adapter, adapterGetDesc, uintptr(unsafe.Pointer(&ad)))
	s.adapterName = syscall.UTF16ToString(ad.Description[:])
	if failed(call(adapter, dxgiGetParent, uintptr(unsafe.Pointer(&iidIDXGIFactory2)), uintptr(unsafe.Pointer(&s.factory)))) {
		return nil, errors.New("gpusurface: DXGI 1.2 is required")
	}
	s.tearing = tearingSupported(s.factory)
	desc := swapChainDesc1{Format: formatB8G8R8A8Unorm, SampleCount: 1, BufferUsage: usageRenderTarget,
		BufferCount: swapChainBuffers, SwapEffect: swapEffectFlipDiscard}
	if s.tearing {
		desc.Flags = swapChainTearing
	}
	if hr := call(s.factory, factoryCreateSwapChain, s.device, s.hwnd, uintptr(unsafe.Pointer(&desc)), 0, 0, uintptr(unsafe.Pointer(&s.swapChain))); failed(hr) {
		return nil, fmt.Errorf("gpusurface: cannot create the swap chain: %#x", uint32(hr))
	}
	call(s.factory, factoryMakeWindowAssoc, s.hwnd, mwaNoAltEnter)
	var back uintptr
	if hr := call(s.swapChain, scGetBuffer, 0, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&back))); failed(hr) {
		return nil, fmt.Errorf("gpusurface: no back buffer: %#x", uint32(hr))
	}
	var bd texture2DDesc
	call(back, texGetDesc, uintptr(unsafe.Pointer(&bd)))
	hr := call(s.device, devCreateRenderTargetView, back, 0, uintptr(unsafe.Pointer(&s.rtv)))
	free(&back)
	if failed(hr) {
		return nil, fmt.Errorf("gpusurface: cannot view the back buffer: %#x", uint32(hr))
	}
	s.width, s.height = int(bd.Width), int(bd.Height)
	s.vp = viewport{W: float32(bd.Width), H: float32(bd.Height), MaxDepth: 1}
	if err = s.makeShaders(); err != nil {
		return nil, err
	}
	return s, nil
}

// tearingSupported reports whether the system shows frames presented with
// tearing (DXGI 1.5).
func tearingSupported(factory uintptr) bool {
	var f5 uintptr
	if failed(call(factory, 0, uintptr(unsafe.Pointer(&iidIDXGIFactory5)), uintptr(unsafe.Pointer(&f5)))) {
		return false
	}
	defer free(&f5)
	var allow int32
	if failed(call(f5, factory5CheckFeature, 0, uintptr(unsafe.Pointer(&allow)), unsafe.Sizeof(allow))) {
		return false
	}
	return allow != 0
}

func (s *Surface) makeShaders() error {
	for _, sh := range []struct {
		entry, target string
		vtbl          int
		out           *uintptr
	}{{"vs", "vs_4_0", devCreateVertexShader, &s.vs}, {"ps_rgb", "ps_4_0", devCreatePixelShader, &s.psRGB}, {"ps_yuv", "ps_4_0", devCreatePixelShader, &s.psYUV}} {
		code, err := compile(shaderSource, sh.entry, sh.target)
		if err != nil {
			return err
		}
		if failed(call(s.device, sh.vtbl, uintptr(unsafe.Pointer(&code[0])), uintptr(len(code)), 0, uintptr(unsafe.Pointer(sh.out)))) {
			return fmt.Errorf("gpusurface: cannot create the %s shader", sh.entry)
		}
	}
	sd := samplerDesc{Filter: 0x15, AddressU: 3, AddressV: 3, AddressW: 3, MaxLOD: 3.402823466e+38}
	if failed(call(s.device, devCreateSamplerState, uintptr(unsafe.Pointer(&sd)), uintptr(unsafe.Pointer(&s.sampler)))) {
		return errors.New("gpusurface: cannot create the sampler")
	}
	for _, p := range []struct {
		v   *yuvParams
		out *uintptr
	}{{&params8, &s.params8}, {&params10, &s.params10}} {
		bd := bufferDesc{ByteWidth: uint32(unsafe.Sizeof(*p.v)), Usage: usageImmutable, BindFlags: bindConstantBuffer}
		data := subresourceData{SysMem: uintptr(unsafe.Pointer(p.v))}
		if failed(call(s.device, devCreateBuffer, uintptr(unsafe.Pointer(&bd)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(p.out)))) {
			return errors.New("gpusurface: cannot create the constant buffer")
		}
	}
	return nil
}

// compile compiles the function entry of source for target with the
// system's d3dcompiler_47.dll, as MyGo's renderer does.
func compile(source, entry, target string) ([]byte, error) {
	if err := procD3DCompile.Find(); err != nil {
		return nil, fmt.Errorf("gpusurface: no shader compiler: %w", err)
	}
	src := []byte(source)
	e, _ := syscall.BytePtrFromString(entry)
	t, _ := syscall.BytePtrFromString(target)
	const optimize3 = 1 << 15
	var blob, errs uintptr
	hr, _, _ := procD3DCompile.Call(uintptr(unsafe.Pointer(&src[0])), uintptr(len(src)), 0, 0, 0,
		uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(t)), optimize3, 0, uintptr(unsafe.Pointer(&blob)), uintptr(unsafe.Pointer(&errs)))
	if errs != 0 {
		defer free(&errs)
	}
	if failed(hr) || blob == 0 {
		msg := "unknown error"
		if errs != 0 {
			msg = string(blobBytes(errs))
		}
		return nil, fmt.Errorf("gpusurface: cannot compile the %s shader: %s", entry, msg)
	}
	defer free(&blob)
	return append([]byte(nil), blobBytes(blob)...), nil
}

func blobBytes(blob uintptr) []byte {
	const blobGetBufferPointer, blobGetBufferSize = 3, 4
	p, n := call(blob, blobGetBufferPointer), call(blob, blobGetBufferSize)
	return unsafe.Slice((*byte)(ptr(p)), n)
}

// Device returns the surface's ID3D11Device, and Context its immediate
// context: a producer makes its frames on the device and draws them on the
// context, from the thread that presents them.
func (s *Surface) Device() uintptr  { return s.device }
func (s *Surface) Context() uintptr { return s.ctx }

// Size returns the size of the swap chain's buffers in pixels.
func (s *Surface) Size() (w, h int) { return s.width, s.height }

// SwapChain returns the surface's IDXGISwapChain1, as for its HDR metadata.
func (s *Surface) SwapChain() uintptr { return s.swapChain }

// Tearing reports whether frames present with tearing.
func (s *Surface) Tearing() bool { return s.tearing }

// Present draws the frame in texture, an ID3D11Texture2D of the surface's
// device in BGRA, NV12 or P010 that shaders can read, and presents it at
// once.
func (s *Surface) Present(texture uintptr) error {
	submitted := Now()
	f := s.wrap(texture)
	if f == nil {
		return ErrFormat
	}
	// Every pointer passed is into s, on the heap (fastCall).
	ctx := s.ctx
	fastCall(ctx, ctxOMSetRenderTargets, 1, uintptr(unsafe.Pointer(&s.rtv)), 0, 0, 0)
	fastCall(ctx, ctxRSSetViewports, 1, uintptr(unsafe.Pointer(&s.vp)), 0, 0, 0)
	fastCall(ctx, ctxIASetPrimitiveTopology, topologyTriangleList, 0, 0, 0, 0)
	fastCall(ctx, ctxIASetInputLayout, 0, 0, 0, 0, 0)
	fastCall(ctx, ctxVSSetShader, s.vs, 0, 0, 0, 0)
	if f.format == FormatBGRA8 {
		fastCall(ctx, ctxPSSetShader, s.psRGB, 0, 0, 0, 0)
	} else {
		fastCall(ctx, ctxPSSetShader, s.psYUV, 0, 0, 0, 0)
		fastCall(ctx, ctxPSSetConstantBuffers, 0, 1, uintptr(unsafe.Pointer(&f.params)), 0, 0)
	}
	fastCall(ctx, ctxPSSetShaderResources, 0, 2, uintptr(unsafe.Pointer(&f.srv[0])), 0, 0)
	fastCall(ctx, ctxPSSetSamplers, 0, 1, uintptr(unsafe.Pointer(&s.sampler)), 0, 0)
	fastCall(ctx, ctxDraw, 3, 0, 0, 0, 0)
	// The producer may draw into the frame next: it is no longer bound.
	fastCall(ctx, ctxPSSetShaderResources, 0, 2, uintptr(unsafe.Pointer(&s.noViews[0])), 0, 0)
	var flags uintptr
	if s.tearing {
		flags = presentTearing
	}
	if hr := fastCall(s.swapChain, scPresent, 0, flags, 0, 0, 0); failed(hr) {
		return fmt.Errorf("gpusurface: present failed: %#x", uint32(hr))
	}
	if i := s.timings.start(submitted); i >= 0 {
		s.timings.recs[i].Encoded = Now()
		s.timings.resolve(i, 0)
	}
	return nil
}

func (s *Surface) wrap(texture uintptr) *frameViews {
	for i := range s.cache {
		if s.cache[i].tex == texture && texture != 0 {
			return &s.cache[i]
		}
	}
	if texture == 0 {
		return nil
	}
	var td texture2DDesc
	call(texture, texGetDesc, uintptr(unsafe.Pointer(&td)))
	var f frameViews
	var formats [2]uint32
	switch td.Format {
	case formatB8G8R8A8Unorm:
		f.format, formats = FormatBGRA8, [2]uint32{formatB8G8R8A8Unorm}
	case formatNV12:
		f.format, formats, f.params = FormatNV12, [2]uint32{formatR8Unorm, formatR8G8Unorm}, s.params8
	case formatP010:
		f.format, formats, f.params = FormatP010, [2]uint32{formatR16Unorm, formatR16G16Unorm}, s.params10
	default:
		return nil
	}
	for i, format := range formats {
		if format == 0 {
			continue
		}
		d := srvDesc{Format: format, ViewDimension: dimensionTexture2D, MipLevels: 1}
		if failed(call(s.device, devCreateShaderResourceView, texture, uintptr(unsafe.Pointer(&d)), uintptr(unsafe.Pointer(&f.srv[i])))) {
			free(&f.srv[0])
			free(&f.srv[1])
			return nil
		}
	}
	f.tex = texture
	slot := &s.cache[s.nextSlot]
	s.nextSlot = (s.nextSlot + 1) % wrapCache
	free(&slot.srv[0])
	free(&slot.srv[1])
	*slot = f
	return slot
}

// Close frees the surface.
func (s *Surface) Close() {
	if s.ctx != 0 {
		call(s.ctx, ctxFlush)
	}
	for i := range s.cache {
		free(&s.cache[i].srv[0])
		free(&s.cache[i].srv[1])
	}
	for _, p := range []*uintptr{&s.params10, &s.params8, &s.sampler, &s.psYUV, &s.psRGB, &s.vs, &s.rtv, &s.swapChain, &s.factory, &s.ctx, &s.device} {
		free(p)
	}
}
