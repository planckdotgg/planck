//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

var (
	d3d11DLL              = syscall.NewLazyDLL("d3d11.dll")
	procD3D11CreateDevice = d3d11DLL.NewProc("D3D11CreateDevice")
)

var IID_ID3D11Device1 = NewGUID(0xa04bfb29, 0x08ef, 0x43d6, [8]byte{0xa4, 0x9c, 0xa9, 0xbd, 0xbd, 0xcb, 0xe6, 0x86})

const (
	DXGI_FORMAT_R10G10B10A2_UNORM  = 24
	DXGI_FORMAT_R16G16B16A16_FLOAT = 10
	DXGI_FORMAT_B8G8R8A8_UNORM     = 87
	DXGI_FORMAT_AYUV               = 100
	DXGI_FORMAT_NV12               = 103
	DXGI_FORMAT_P010               = 104
	DXGI_FORMAT_Y410               = 108

	D3D11_USAGE_DEFAULT = 0
	D3D11_USAGE_STAGING = 3

	D3D11_BIND_SHADER_RESOURCE = 0x8
	D3D11_BIND_RENDER_TARGET   = 0x20

	D3D11_CPU_ACCESS_WRITE = 0x10000
	D3D11_CPU_ACCESS_READ  = 0x20000

	D3D11_RESOURCE_MISC_SHARED            = 0x2
	D3D11_RESOURCE_MISC_SHARED_KEYEDMUTEX = 0x100
	D3D11_RESOURCE_MISC_SHARED_NTHANDLE   = 0x800

	D3D11_MAP_READ  = 1
	D3D11_MAP_WRITE = 2

	D3D11_CREATE_DEVICE_BGRA_SUPPORT  = 0x20
	D3D11_CREATE_DEVICE_VIDEO_SUPPORT = 0x800

	D3D11_QUERY_EVENT              = 0
	D3D11_QUERY_TIMESTAMP          = 2
	D3D11_QUERY_TIMESTAMP_DISJOINT = 3

	DXGI_SHARED_RESOURCE_READ  = 0x80000000
	DXGI_SHARED_RESOURCE_WRITE = 1
)

type Texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type MappedSubresource struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

type Box struct{ Left, Top, Front, Right, Bottom, Back uint32 }

type Device struct{ *Object }
type Context struct{ *Object }
type Texture struct{ *Object }
type Query struct{ *Object }

// CreateDevice makes a hardware D3D11 device on the given adapter.
func CreateDevice(a Adapter) (Device, Context, error) {
	if err := procD3D11CreateDevice.Find(); err != nil {
		return Device{}, Context{}, err
	}
	levels := [...]uint32{0xc100, 0xb100, 0xb000} // 12.1, 11.1, 11.0
	var dev, ctx *Object
	var got uint32
	r, _, _ := procD3D11CreateDevice.Call(
		uintptr(unsafe.Pointer(a.Object)), 0, 0,
		D3D11_CREATE_DEVICE_BGRA_SUPPORT|D3D11_CREATE_DEVICE_VIDEO_SUPPORT,
		uintptr(unsafe.Pointer(&levels[0])), uintptr(len(levels)), 7,
		uintptr(unsafe.Pointer(&dev)), uintptr(unsafe.Pointer(&got)), uintptr(unsafe.Pointer(&ctx)))
	if err := hr(r); err != nil {
		return Device{}, Context{}, err
	}
	return Device{dev}, Context{ctx}, nil
}

func (d Device) CreateTexture2D(desc *Texture2DDesc) (Texture, error) {
	var t *Object
	if err := hr(d.call3(5, uintptr(unsafe.Pointer(desc)), 0, uintptr(unsafe.Pointer(&t)))); err != nil {
		return Texture{}, err
	}
	return Texture{t}, nil
}

// CreateTexture2DInit creates a texture with initial pixel data for mip 0.
func (d Device) CreateTexture2DInit(desc *Texture2DDesc, data []byte, rowPitch uint32) (Texture, error) {
	init := struct {
		Mem        uintptr
		Pitch      uint32
		SlicePitch uint32
	}{uintptr(unsafe.Pointer(&data[0])), rowPitch, 0}
	var t *Object
	if err := hr(d.call3(5, uintptr(unsafe.Pointer(desc)), uintptr(unsafe.Pointer(&init)), uintptr(unsafe.Pointer(&t)))); err != nil {
		return Texture{}, err
	}
	return Texture{t}, nil
}

func (d Device) CreateQuery(kind uint32) (Query, error) {
	desc := [2]uint32{kind, 0}
	var q *Object
	if err := hr(d.call2(24, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&q)))); err != nil {
		return Query{}, err
	}
	return Query{q}, nil
}

// OpenSharedResource1 opens an NT handle made by IDXGIResource1::CreateSharedHandle.
func (d Device) OpenSharedResource1(handle uintptr) (Texture, error) {
	d1, err := d.QueryInterface(&IID_ID3D11Device1)
	if err != nil {
		return Texture{}, err
	}
	defer d1.Release()
	var t *Object
	if err := hr(d1.call3(48, handle, uintptr(unsafe.Pointer(&IID_ID3D11Texture2D)), uintptr(unsafe.Pointer(&t)))); err != nil {
		return Texture{}, err
	}
	return Texture{t}, nil
}

// OpenSharedResource opens a legacy (non-NT) shared handle.
func (d Device) OpenSharedResource(handle uintptr) (Texture, error) {
	var t *Object
	if err := hr(d.call3(28, handle, uintptr(unsafe.Pointer(&IID_ID3D11Texture2D)), uintptr(unsafe.Pointer(&t)))); err != nil {
		return Texture{}, err
	}
	return Texture{t}, nil
}

func (t Texture) Desc() Texture2DDesc {
	var d Texture2DDesc
	t.call1(10, uintptr(unsafe.Pointer(&d)))
	return d
}

// CreateSharedHandle exports the texture as an NT handle (needs MISC_SHARED_NTHANDLE).
func (t Texture) CreateSharedHandle() (uintptr, error) {
	r1, err := t.QueryInterface(&IID_IDXGIResource1)
	if err != nil {
		return 0, err
	}
	defer r1.Release()
	var h uintptr
	err = hr(r1.call(13, 0, DXGI_SHARED_RESOURCE_READ|DXGI_SHARED_RESOURCE_WRITE, 0, uintptr(unsafe.Pointer(&h))))
	return h, err
}

// SharedHandle returns the legacy shared handle (needs MISC_SHARED).
func (t Texture) SharedHandle() (uintptr, error) {
	r, err := t.QueryInterface(&IID_IDXGIResource)
	if err != nil {
		return 0, err
	}
	defer r.Release()
	var h uintptr
	err = hr(r.call1(8, uintptr(unsafe.Pointer(&h))))
	return h, err
}

func (c Context) CopyResource(dst, src *Object) {
	c.call2(47, uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(src)))
}

func (c Context) CopySubresourceRegion(dst *Object, x, y uint32, src *Object, box *Box) {
	c.call(46, uintptr(unsafe.Pointer(dst)), 0, uintptr(x), uintptr(y), 0,
		uintptr(unsafe.Pointer(src)), 0, uintptr(unsafe.Pointer(box)))
}

func (c Context) Map(res *Object, mapType uint32, out *MappedSubresource) error {
	return hr(c.call(14, uintptr(unsafe.Pointer(res)), 0, uintptr(mapType), 0, uintptr(unsafe.Pointer(out))))
}

func (c Context) Unmap(res *Object) { c.call2(15, uintptr(unsafe.Pointer(res)), 0) }

func (c Context) Flush() { c.call0(111) }

func (c Context) End(q Query) { c.call1(28, uintptr(unsafe.Pointer(q.Object))) }

func (c Context) Begin(q Query) { c.call1(27, uintptr(unsafe.Pointer(q.Object))) }

// GetData returns S_FALSE (1) while the query is pending.
func (c Context) GetData(q Query, out unsafe.Pointer, size uint32) uintptr {
	return c.call(29, uintptr(unsafe.Pointer(q.Object)), uintptr(out), uintptr(size), 0)
}

// WaitIdle spins on an event query until the GPU has run everything submitted.
func (c Context) WaitIdle(q Query) {
	c.End(q)
	var done int32
	for c.GetData(q, unsafe.Pointer(&done), 4) != 0 {
	}
}
