//go:build windows

package win

import (
	"errors"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/gpu"
)

var (
	dxgiDLL                = syscall.NewLazyDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
)

var (
	IID_IDXGIFactory1   = NewGUID(0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87})
	IID_IDXGIOutput1    = NewGUID(0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc})
	IID_IDXGIOutput5    = NewGUID(0x80a07424, 0xab52, 0x42eb, [8]byte{0x83, 0x3c, 0x0c, 0x42, 0xfd, 0x28, 0x2d, 0x98})
	IID_IDXGIOutput6    = NewGUID(0x068346e8, 0xaaec, 0x4b84, [8]byte{0xad, 0xd7, 0x13, 0x7f, 0x51, 0x3f, 0x77, 0xa1})
	IID_IDXGIResource   = NewGUID(0x035f3ab4, 0x482e, 0x4e50, [8]byte{0xb4, 0x1f, 0x8a, 0x7f, 0x8b, 0xd8, 0x96, 0x0b})
	IID_IDXGIResource1  = NewGUID(0x30961379, 0x4609, 0x4a41, [8]byte{0x99, 0x8e, 0x54, 0xfe, 0x56, 0x7e, 0xe0, 0xc1})
	IID_IDXGIKeyedMutex = NewGUID(0x9d8e1289, 0xd7b3, 0x465f, [8]byte{0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d})
	IID_ID3D11Texture2D = NewGUID(0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c})
)

const (
	DXGI_ERROR_NOT_FOUND               HRESULT = 0x887a0002
	DXGI_ERROR_WAIT_TIMEOUT            HRESULT = 0x887a0027
	DXGI_ERROR_ACCESS_LOST             HRESULT = 0x887a0026
	DXGI_ERROR_UNSUPPORTED             HRESULT = 0x887a0004
	DXGI_ERROR_NOT_CURRENTLY_AVAILABLE HRESULT = 0x887a0022
)

type LUIDPair struct {
	Low  uint32
	High int32
}

func (l LUIDPair) LUID() gpu.LUID { return gpu.LUID(uint64(uint32(l.High))<<32 | uint64(l.Low)) }

type AdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           LUIDPair
	Flags                 uint32
}

type Rect32 struct{ Left, Top, Right, Bottom int32 }

type OutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates Rect32
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type OutputDesc1 struct {
	DeviceName            [32]uint16
	DesktopCoordinates    Rect32
	AttachedToDesktop     int32
	Rotation              uint32
	Monitor               uintptr
	BitsPerColor          uint32
	ColorSpace            uint32
	RedPrimary            [2]float32
	GreenPrimary          [2]float32
	BluePrimary           [2]float32
	WhitePoint            [2]float32
	MinLuminance          float32
	MaxLuminance          float32
	MaxFullFrameLuminance float32
}

type Factory struct{ *Object }
type Adapter struct{ *Object }
type Output struct{ *Object }

func CreateFactory() (Factory, error) {
	if err := procCreateDXGIFactory1.Find(); err != nil {
		return Factory{}, err
	}
	var f *Object
	r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&IID_IDXGIFactory1)), uintptr(unsafe.Pointer(&f)))
	if err := hr(r); err != nil {
		return Factory{}, err
	}
	return Factory{f}, nil
}

// EnumAdapters1 returns DXGI_ERROR_NOT_FOUND past the last adapter.
func (f Factory) EnumAdapters1(i int) (Adapter, error) {
	var a *Object
	if err := hr(f.call2(12, uintptr(i), uintptr(unsafe.Pointer(&a)))); err != nil {
		return Adapter{}, err
	}
	return Adapter{a}, nil
}

func (f Factory) Adapters() ([]Adapter, error) {
	var out []Adapter
	for i := 0; ; i++ {
		a, err := f.EnumAdapters1(i)
		if errors.Is(err, DXGI_ERROR_NOT_FOUND) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, a)
	}
}

func (a Adapter) Desc() (AdapterDesc1, error) {
	var d AdapterDesc1
	err := hr(a.call1(10, uintptr(unsafe.Pointer(&d))))
	return d, err
}

const dxgiAdapterFlagSoftware = 2

func (a Adapter) Info() (gpu.Adapter, error) {
	d, err := a.Desc()
	if err != nil {
		return gpu.Adapter{}, err
	}
	return gpu.Adapter{
		LUID:      d.AdapterLuid.LUID(),
		Vendor:    gpu.Vendor(d.VendorID),
		DeviceID:  d.DeviceID,
		Name:      utf16(d.Description[:]),
		VRAMBytes: uint64(d.DedicatedVideoMemory),
		Software:  d.Flags&dxgiAdapterFlagSoftware != 0,
	}, nil
}

func (a Adapter) EnumOutputs(i int) (Output, error) {
	var o *Object
	if err := hr(a.call2(7, uintptr(i), uintptr(unsafe.Pointer(&o)))); err != nil {
		return Output{}, err
	}
	return Output{o}, nil
}

func (a Adapter) Outputs() ([]Output, error) {
	var out []Output
	for i := 0; ; i++ {
		o, err := a.EnumOutputs(i)
		if errors.Is(err, DXGI_ERROR_NOT_FOUND) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, o)
	}
}

func (o Output) Desc() (OutputDesc, error) {
	var d OutputDesc
	err := hr(o.call1(7, uintptr(unsafe.Pointer(&d))))
	return d, err
}

// Desc1 reports HDR color space and bits per color (IDXGIOutput6).
func (o Output) Desc1() (OutputDesc1, error) {
	var d OutputDesc1
	o6, err := o.QueryInterface(&IID_IDXGIOutput6)
	if err != nil {
		return d, err
	}
	defer o6.Release()
	err = hr(o6.call1(27, uintptr(unsafe.Pointer(&d))))
	return d, err
}

func (d OutputDesc) Name() string { return utf16(d.DeviceName[:]) }

// Duplication wraps IDXGIOutputDuplication.
type Duplication struct{ *Object }

type OutduplDesc struct {
	ModeDesc struct {
		Width            uint32
		Height           uint32
		RefreshNum       uint32
		RefreshDen       uint32
		Format           uint32
		ScanlineOrdering uint32
		Scaling          uint32
	}
	Rotation                   uint32
	DesktopImageInSystemMemory int32
}

type OutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           struct {
		X, Y    int32
		Visible int32
	}
	TotalMetadataBufferSize uint32
	PointerShapeBufferSize  uint32
}

// Duplicate opens Desktop Duplication on this output. formats non-empty uses
// IDXGIOutput5::DuplicateOutput1 so HDR desktops can be captured as 10-bit or
// FP16; otherwise IDXGIOutput1::DuplicateOutput (BGRA8 only).
func (o Output) Duplicate(device *Object, formats []uint32) (Duplication, error) {
	var d *Object
	if len(formats) > 0 {
		o5, err := o.QueryInterface(&IID_IDXGIOutput5)
		if err != nil {
			return Duplication{}, err
		}
		defer o5.Release()
		err = hr(o5.call(26, uintptr(unsafe.Pointer(device)), 0, uintptr(len(formats)),
			uintptr(unsafe.Pointer(&formats[0])), uintptr(unsafe.Pointer(&d))))
		if err != nil {
			return Duplication{}, err
		}
		return Duplication{d}, nil
	}
	o1, err := o.QueryInterface(&IID_IDXGIOutput1)
	if err != nil {
		return Duplication{}, err
	}
	defer o1.Release()
	if err := hr(o1.call2(22, uintptr(unsafe.Pointer(device)), uintptr(unsafe.Pointer(&d)))); err != nil {
		return Duplication{}, err
	}
	return Duplication{d}, nil
}

func (d Duplication) Desc() OutduplDesc {
	var desc OutduplDesc
	d.call1(7, uintptr(unsafe.Pointer(&desc)))
	return desc
}

// AcquireNextFrame returns the desktop image as an IDXGIResource. Callers
// must pass heap pointers for info and res (they are written by the call).
func (d Duplication) AcquireNextFrame(timeoutMs uint32, info *OutduplFrameInfo, res **Object) error {
	return hr(d.call3(8, uintptr(timeoutMs), uintptr(unsafe.Pointer(info)), uintptr(unsafe.Pointer(res))))
}

func (d Duplication) ReleaseFrame() error { return hr(d.call0(14)) }
