//go:build linux && !android && (amd64 || arm64)

package vaapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// libva constants (va.h, va_drmcommon.h, libva 2.x).
const (
	vaStatusSuccess = 0

	vaProfileHEVCMain   = 17
	vaProfileHEVCMain10 = 18
	vaEntrypointVLD     = 1

	vaConfigAttribRTFormat = 0
	vaRTFormatYUV420       = 0x1
	vaRTFormatYUV420_10    = 0x100

	vaSurfaceAttribPixelFormat = 1
	vaSurfaceAttribSettable    = 2
	vaGenericValueInteger      = 1

	vaFourccNV12 = 0x3231564E
	vaFourccP010 = 0x30313050

	vaProgressive = 1

	vaPictureParameterBufferType = 0
	vaIQMatrixBufferType         = 1
	vaSliceParameterBufferType   = 4
	vaSliceDataBufferType        = 5

	vaMemTypeDRMPrime2     = 0x40000000
	vaExportReadOnly       = 0x1
	vaExportComposedLayers = 0x8

	vaInvalidID = 0xffffffff
)

var lib struct {
	once sync.Once
	err  error

	getDisplayDRM, initialize, terminate, queryVendor, errorStr                  uintptr
	maxProfiles, queryProfiles, createConfig, destroyConfig                      uintptr
	createSurfaces, destroySurfaces, createContext, destroyContext               uintptr
	createBuffer, destroyBuffer, beginPicture, renderPicture, endPicture         uintptr
	syncSurface, exportSurface, deriveImage, createImage, getImage, destroyImage uintptr
	mapBuffer, unmapBuffer, setInfoCallback                                      uintptr
}

func load() error {
	lib.once.Do(func() {
		va, err := purego.Dlopen("libva.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no libva (%v)", decode.ErrUnsupported, err)
			return
		}
		drm, err := purego.Dlopen("libva-drm.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no libva-drm (%v)", decode.ErrUnsupported, err)
			return
		}
		for _, f := range []struct {
			h    uintptr
			p    *uintptr
			name string
		}{
			{drm, &lib.getDisplayDRM, "vaGetDisplayDRM"},
			{va, &lib.initialize, "vaInitialize"},
			{va, &lib.terminate, "vaTerminate"},
			{va, &lib.queryVendor, "vaQueryVendorString"},
			{va, &lib.errorStr, "vaErrorStr"},
			{va, &lib.maxProfiles, "vaMaxNumProfiles"},
			{va, &lib.queryProfiles, "vaQueryConfigProfiles"},
			{va, &lib.createConfig, "vaCreateConfig"},
			{va, &lib.destroyConfig, "vaDestroyConfig"},
			{va, &lib.createSurfaces, "vaCreateSurfaces"},
			{va, &lib.destroySurfaces, "vaDestroySurfaces"},
			{va, &lib.createContext, "vaCreateContext"},
			{va, &lib.destroyContext, "vaDestroyContext"},
			{va, &lib.createBuffer, "vaCreateBuffer"},
			{va, &lib.destroyBuffer, "vaDestroyBuffer"},
			{va, &lib.beginPicture, "vaBeginPicture"},
			{va, &lib.renderPicture, "vaRenderPicture"},
			{va, &lib.endPicture, "vaEndPicture"},
			{va, &lib.syncSurface, "vaSyncSurface"},
			{va, &lib.exportSurface, "vaExportSurfaceHandle"},
			{va, &lib.deriveImage, "vaDeriveImage"},
			{va, &lib.createImage, "vaCreateImage"},
			{va, &lib.getImage, "vaGetImage"},
			{va, &lib.destroyImage, "vaDestroyImage"},
			{va, &lib.mapBuffer, "vaMapBuffer"},
			{va, &lib.unmapBuffer, "vaUnmapBuffer"},
		} {
			if *f.p, err = purego.Dlsym(f.h, f.name); err != nil {
				lib.err = fmt.Errorf("%w: libva lacks %s (it needs 2.1 or later)", decode.ErrUnsupported, f.name)
				return
			}
		}
		lib.setInfoCallback, _ = purego.Dlsym(va, "vaSetInfoCallback")
	})
	return lib.err
}

// call calls C; go:uintptrescapes keeps a pointer argument valid when the
// goroutine's stack moves before the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) int32 {
	r, _, _ := purego.SyscallN(fn, args...)
	return int32(r)
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

func cString(p uintptr) string {
	if p == 0 {
		return ""
	}
	var b []byte
	for ; *(*byte)(ptr(p)) != 0; p++ {
		b = append(b, *(*byte)(ptr(p)))
	}
	return string(b)
}

func status(what string, s int32) error {
	if s == vaStatusSuccess {
		return nil
	}
	r, _, _ := purego.SyscallN(lib.errorStr, uintptr(s))
	return fmt.Errorf("vaapi: %s: %s (%d)", what, cString(r), s)
}

// display is an initialized VA display on a render node.
type display struct {
	fd     int
	dpy    uintptr
	vendor string
	node   gpu.RenderNode
}

// openDisplay opens the first render node whose driver initializes,
// preferring vendor's. Under WSL, where the render node's driver tells
// libva nothing, it asks for Mesa's d3d12 driver.
func openDisplay(vendor gpu.Vendor) (*display, error) {
	if err := load(); err != nil {
		return nil, err
	}
	nodes, err := gpu.RenderNodes()
	if err != nil || len(nodes) == 0 {
		return nil, fmt.Errorf("%w: no DRM render node", decode.ErrUnsupported)
	}
	if os.Getenv("LIBVA_DRIVER_NAME") == "" {
		if _, err := os.Stat("/dev/dxg"); err == nil {
			os.Setenv("LIBVA_DRIVER_NAME", "d3d12")
		}
	}
	if vendor != gpu.VendorUnknown {
		for i, n := range nodes {
			if n.Adapter.Vendor == vendor {
				nodes[0], nodes[i] = nodes[i], nodes[0]
				break
			}
		}
	}
	// Mesa's d3d12 driver under WSL initializes on the primary node only.
	cards, _ := filepath.Glob("/dev/dri/card*")
	for _, c := range cards {
		nodes = append(nodes, gpu.RenderNode{Path: c})
	}
	var errs []error
	for _, n := range nodes {
		d, err := initDisplay(n)
		if err == nil {
			return d, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", n.Path, err))
	}
	return nil, fmt.Errorf("%w: %v", decode.ErrUnsupported, errors.Join(errs...))
}

func initDisplay(n gpu.RenderNode) (*display, error) {
	fd, err := syscall.Open(n.Path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	d := &display{fd: fd, node: n}
	r, _, _ := purego.SyscallN(lib.getDisplayDRM, uintptr(fd))
	d.dpy = r
	if d.dpy == 0 {
		syscall.Close(fd)
		return nil, errors.New("vaGetDisplayDRM failed")
	}
	if lib.setInfoCallback != 0 {
		purego.SyscallN(lib.setInfoCallback, d.dpy, 0, 0)
	}
	var major, minor int32
	if err := status("vaInitialize", call(lib.initialize, d.dpy, uintptr(unsafe.Pointer(&major)), uintptr(unsafe.Pointer(&minor)))); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	v, _, _ := purego.SyscallN(lib.queryVendor, d.dpy)
	d.vendor = cString(v)
	return d, nil
}

func (d *display) profiles() map[int32]bool {
	n := call(lib.maxProfiles, d.dpy)
	if n <= 0 {
		return nil
	}
	list := make([]int32, n)
	var got int32
	if call(lib.queryProfiles, d.dpy, uintptr(unsafe.Pointer(&list[0])), uintptr(unsafe.Pointer(&got))) != vaStatusSuccess {
		return nil
	}
	out := map[int32]bool{}
	for _, p := range list[:got] {
		out[p] = true
	}
	return out
}

func (d *display) close() {
	if d.dpy != 0 {
		call(lib.terminate, d.dpy)
		d.dpy = 0
	}
	if d.fd > 0 {
		syscall.Close(d.fd)
		d.fd = 0
	}
}

// buffer makes a VA buffer of data, which libva copies.
func (d *display) buffer(ctx uint32, typ uint32, data unsafe.Pointer, size int) (uint32, error) {
	var id uint32
	err := status("vaCreateBuffer", call(lib.createBuffer, d.dpy, uintptr(ctx), uintptr(typ), uintptr(size), 1, uintptr(data), uintptr(unsafe.Pointer(&id))))
	return id, err
}

// surfaceAttrib is VASurfaceAttrib with an integer value.
type surfaceAttrib struct {
	typ, flags uint32
	valueType  uint32
	_          uint32
	value      int64
}

// drmPrimeDescriptor is VADRMPRIMESurfaceDescriptor.
type drmPrimeDescriptor struct {
	fourcc, width, height, numObjects uint32
	objects                           [4]struct {
		fd       int32
		size     uint32
		modifier uint64
	}
	numLayers uint32
	layers    [4]struct {
		drmFormat, numPlanes       uint32
		objectIndex, offset, pitch [4]uint32
	}
	_ uint32
}

// vaImage is VAImage.
type vaImage struct {
	id                    uint32
	format                [12]uint32 // VAImageFormat
	buf                   uint32
	width, height         uint16
	dataSize, numPlanes   uint32
	pitches, offsets      [3]uint32
	numPalette, entrySize int32
	componentOrder        [4]int8
	_                     [4]uint32
}
