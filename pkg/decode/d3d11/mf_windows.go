//go:build windows

package d3d11

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/watzon/planck/internal/win"
)

// Vtable slots, GUIDs and constants of Media Foundation, measured from the
// Windows SDK headers with MSVC x64.
const (
	attrGetUINT32          = 7
	attrGetUINT64          = 8
	attrGetGUID            = 10
	attrGetAllocatedString = 13
	attrSetUINT32          = 21
	attrSetUINT64          = 22
	attrSetGUID            = 24

	activateObject = 33

	mftGetOutputStreamInfo       = 7
	mftGetAttributes             = 8
	mftGetOutputStreamAttributes = 10
	mftGetOutputAvailableType    = 14
	mftSetInputType              = 15
	mftSetOutputType             = 16
	mftGetOutputCurrentType      = 18
	mftProcessMessage            = 23
	mftProcessInput              = 24
	mftProcessOutput             = 25

	sampleSetSampleTime    = 36
	sampleGetSampleTime    = 35
	sampleGetBufferByIndex = 40
	sampleAddBuffer        = 42

	bufLock             = 3
	bufUnlock           = 4
	bufSetCurrentLength = 6

	dxgiBufGetResource         = 3
	dxgiBufGetSubresourceIndex = 4

	mgrResetDevice = 7

	setMultithreadProtected = 5

	mfVersion           = 0x20070
	mfStartupLite       = 1
	enumSyncMFT         = 1
	enumSortFilter      = 0x40
	coinitMultithreaded = 0

	msgSetD3DManager  = 2
	msgBeginStreaming = 0x10000000
	msgStartOfStream  = 0x10000003
	msgCommandFlush   = 0
	msgCommandDrain   = 1

	interlaceProgressive  = 2
	bindDecoder           = 0x200
	bindShaderResource    = 0x8
	streamProvidesSamples = 0x100

	eNeedMoreInput  win.HRESULT = 0xc00d6d72
	eStreamChange   win.HRESULT = 0xc00d6d61
	eNotAccepting   win.HRESULT = 0xc00d36b5
	eNoMoreTypes    win.HRESULT = 0xc00d36b9
	eTypeNotSet     win.HRESULT = 0xc00d6d60
	rpcEChangedMode win.HRESULT = 0x80010106
)

func guid(d1 uint32, d2, d3 uint16, d4 ...byte) win.GUID {
	var g win.GUID
	g.Data1, g.Data2, g.Data3 = d1, d2, d3
	copy(g.Data4[:], d4)
	return g
}

func videoFormat(fourcc uint32) win.GUID {
	return guid(fourcc, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71)
}

var (
	categoryVideoDecoder = guid(0xd6c02d4b, 0x6833, 0x45b4, 0x97, 0x1a, 0x05, 0xa4, 0xb0, 0x4b, 0xab, 0x91)
	mediaTypeVideo       = guid(0x73646976, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71)
	formatHEVC           = videoFormat(0x43564548)
	formatAV1            = videoFormat(0x31305641)
	formatNV12           = videoFormat(0x3231564e)
	formatP010           = videoFormat(0x30313050)

	mtMajorType          = guid(0x48eba18e, 0xf8c9, 0x4687, 0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f)
	mtSubtype            = guid(0xf7e34c9a, 0x42e8, 0x4714, 0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5)
	mtFrameSize          = guid(0x1652c33d, 0xd6b2, 0x4012, 0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d)
	mtFrameRate          = guid(0xc459a2e8, 0x3d2c, 0x4e44, 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0)
	mtInterlaceMode      = guid(0xe2724bb8, 0xe676, 0x4806, 0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd)
	mtMinDisplayAperture = guid(0xd7388766, 0x18fe, 0x48c6, 0xa1, 0x77, 0xee, 0x89, 0x48, 0x67, 0xc8, 0xc4)

	attrLowLatency     = guid(0x9c27891a, 0xed7a, 0x40e1, 0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee)
	attrD3D11Aware     = guid(0x206b4fc8, 0xfcf9, 0x4c51, 0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0)
	attrD3D11BindFlags = guid(0xeacf97ad, 0x065c, 0x4408, 0xbe, 0xe3, 0xfd, 0xcb, 0xfd, 0x12, 0x8b, 0xe2)
	attrFriendlyName   = guid(0x314ffbae, 0x5b41, 0x4c95, 0x9c, 0x19, 0x4e, 0x7d, 0x58, 0x6f, 0xac, 0xe3)

	iidTransform   = guid(0xbf94c121, 0x5b05, 0x4e6f, 0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d)
	iidDXGIBuffer  = guid(0xe7174cfa, 0x1c9e, 0x48b1, 0x88, 0x66, 0x62, 0x62, 0x26, 0xbf, 0xc2, 0x58)
	iidMultithread = guid(0x9b7e4e00, 0x342c, 0x4106, 0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0)
)

var (
	mfplat                    = syscall.NewLazyDLL("mfplat.dll")
	procMFStartup             = mfplat.NewProc("MFStartup")
	procMFTEnumEx             = mfplat.NewProc("MFTEnumEx")
	procMFCreateDXGIDeviceMgr = mfplat.NewProc("MFCreateDXGIDeviceManager")
	procMFCreateSample        = mfplat.NewProc("MFCreateSample")
	procMFCreateMemoryBuffer  = mfplat.NewProc("MFCreateMemoryBuffer")
	procMFCreateMediaType     = mfplat.NewProc("MFCreateMediaType")
	ole32                     = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx        = ole32.NewProc("CoInitializeEx")
	procCoTaskMemFree         = ole32.NewProc("CoTaskMemFree")

	startOnce sync.Once
	startErr  error
)

// startup starts Media Foundation once per process. COM's multithreaded
// apartment, entered once, covers every thread that has not chosen another.
func startup() error {
	startOnce.Do(func() {
		if r, _, _ := procCoInitializeEx.Call(0, coinitMultithreaded); win.HRESULT(r).Failed() && win.HRESULT(r) != rpcEChangedMode {
			startErr = fmt.Errorf("d3d11: CoInitializeEx: %w", win.HRESULT(r))
			return
		}
		if err := procMFStartup.Find(); err != nil {
			startErr = fmt.Errorf("d3d11: Media Foundation: %w", err)
			return
		}
		if r, _, _ := procMFStartup.Call(mfVersion, mfStartupLite); win.HRESULT(r).Failed() {
			startErr = fmt.Errorf("d3d11: MFStartup: %w", win.HRESULT(r))
		}
	})
	return startErr
}

// obj is a COM interface pointer held as an address: Media Foundation
// objects are not Go memory.
type obj uintptr

func (o obj) vt(i int) uintptr {
	return *(*uintptr)(unsafe.Add(win.Ptr(*(*uintptr)(win.Ptr(uintptr(o)))), i*8))
}

func (o obj) call(i int, args ...uintptr) win.HRESULT {
	a := make([]uintptr, 0, 8)
	a = append(a, uintptr(o))
	a = append(a, args...)
	r, _, _ := syscall.SyscallN(o.vt(i), a...)
	return win.HRESULT(r)
}

// call3 is call with no allocation, for the per-frame path. Pointers passed
// must be to the heap or to C memory.
func (o obj) call3(i int, a, b, c uintptr) win.HRESULT {
	r, _, _ := syscall.SyscallN(o.vt(i), uintptr(o), a, b, c)
	return win.HRESULT(r)
}

func (o *obj) release() {
	if *o != 0 {
		o.call3(2, 0, 0, 0)
		*o = 0
	}
}

func (o obj) queryInterface(iid *win.GUID) (obj, error) {
	var out obj
	if hr := o.call3(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)), 0); hr.Failed() {
		return 0, hr
	}
	return out, nil
}

func setGUID(attrs obj, key, v *win.GUID) error {
	if hr := attrs.call3(attrSetGUID, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(v)), 0); hr.Failed() {
		return hr
	}
	return nil
}

func setUINT32(attrs obj, key *win.GUID, v uint32) error {
	if hr := attrs.call3(attrSetUINT32, uintptr(unsafe.Pointer(key)), uintptr(v), 0); hr.Failed() {
		return hr
	}
	return nil
}

func setUINT64(attrs obj, key *win.GUID, v uint64) error {
	if hr := attrs.call3(attrSetUINT64, uintptr(unsafe.Pointer(key)), uintptr(v), 0); hr.Failed() {
		return hr
	}
	return nil
}

func getUINT32(attrs obj, key *win.GUID) (uint32, bool) {
	var v uint32
	return v, !attrs.call3(attrGetUINT32, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)), 0).Failed()
}

func getUINT64(attrs obj, key *win.GUID) (uint64, bool) {
	var v uint64
	return v, !attrs.call3(attrGetUINT64, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)), 0).Failed()
}

func getGUID(attrs obj, key *win.GUID) (win.GUID, bool) {
	var v win.GUID
	return v, !attrs.call3(attrGetGUID, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)), 0).Failed()
}

func getString(attrs obj, key *win.GUID) string {
	var p uintptr
	var n uint32
	if attrs.call3(attrGetAllocatedString, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n))).Failed() {
		return ""
	}
	defer procCoTaskMemFree.Call(p)
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(win.Ptr(p)), n))
}

// mft is one registered decoder transform.
type mft struct {
	name     string
	activate obj
}

// enumDecoders lists the synchronous decoder transforms that take subtype,
// best first. The caller releases each activate.
func enumDecoders(subtype win.GUID) ([]mft, error) {
	if err := startup(); err != nil {
		return nil, err
	}
	in := [2]win.GUID{mediaTypeVideo, subtype}
	var acts uintptr
	var n uint32
	if r, _, _ := procMFTEnumEx.Call(uintptr(unsafe.Pointer(&categoryVideoDecoder)), enumSyncMFT|enumSortFilter,
		uintptr(unsafe.Pointer(&in[0])), 0, uintptr(unsafe.Pointer(&acts)), uintptr(unsafe.Pointer(&n))); win.HRESULT(r).Failed() {
		return nil, fmt.Errorf("d3d11: MFTEnumEx: %w", win.HRESULT(r))
	}
	if n == 0 {
		return nil, nil
	}
	defer procCoTaskMemFree.Call(acts)
	list := unsafe.Slice((*obj)(win.Ptr(acts)), n)
	out := make([]mft, 0, n)
	for _, a := range list {
		out = append(out, mft{name: getString(a, &attrFriendlyName), activate: a})
	}
	return out, nil
}
