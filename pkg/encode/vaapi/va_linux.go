//go:build linux && !android && (amd64 || arm64)

package vaapi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// libva 2.20 constants (va.h, va_enc_hevc.h, va_vpp.h), checked with a C
// program built against its headers.
const (
	vaStatusSuccess = 0

	vaProfileNone          = ^uintptr(0) // -1
	vaProfileHEVCMain      = 17
	vaProfileHEVCMain10    = 18
	vaEntrypointEncSlice   = 6
	vaEntrypointEncSliceLP = 8
	vaEntrypointVideoProc  = 10

	vaConfigAttribRTFormat            = 0
	vaConfigAttribRateControl         = 5
	vaConfigAttribEncPackedHeaders    = 10
	vaConfigAttribEncMaxSlices        = 14
	vaConfigAttribPredictionDirection = 39
	vaConfigAttribEncHEVCFeatures     = 50
	vaConfigAttribEncHEVCBlockSizes   = 51
	vaAttribNotSupported              = 0x80000000

	vaRTFormatYUV420    = 0x1
	vaRTFormatYUV420_10 = 0x100
	vaRTFormatRGB32     = 0x20000

	vaRCCBR = 2
	vaRCVBR = 4

	vaPackedHeaderSequence = 1
	vaPackedHeaderSlice    = 4
	vaPackedTypeSequence   = 1
	vaPackedTypeSlice      = 3

	vaPredictionPrevious   = 1
	vaPredictionBiNotEmpty = 4

	vaEncCodedBufferType                 = 21
	vaEncSequenceParameterBufferType     = 22
	vaEncPictureParameterBufferType      = 23
	vaEncSliceParameterBufferType        = 24
	vaEncPackedHeaderParameterBufferType = 25
	vaEncPackedHeaderDataBufferType      = 26
	vaEncMiscParameterBufferType         = 27
	vaProcPipelineParameterBufferType    = 41

	vaMiscFrameRate   = 0
	vaMiscRateControl = 1
	vaMiscHRD         = 5

	vaSurfaceAttribPixelFormat = 1
	vaSurfaceAttribMemoryType  = 6
	vaSurfaceAttribExternalBuf = 7
	vaSurfaceAttribSettable    = 2
	vaGenericValueInteger      = 1
	vaGenericValuePointer      = 3
	vaMemTypeDRMPrime2         = 0x40000000

	vaProgressive = 1

	vaPictureHEVCInvalid = 0x1
	vaInvalidID          = 0xffffffff

	vaProcColorStandardBT709  = 2
	vaProcColorStandardBT2020 = 12
	vaSourceRangeReduced      = 1
	vaSourceRangeFull         = 2
)

// VA fourccs.
const (
	vaFourccNV12 = 0x3231564E
	vaFourccP010 = 0x30313050
	vaFourccBGRX = 0x58524742
	vaFourccBGRA = 0x41524742
	vaFourccRGBX = 0x58424752
	vaFourccRGBA = 0x41424752
)

var lib struct {
	once sync.Once
	err  error

	getDisplayDRM, initialize, terminate, queryVendor, errorStr, setInfoCallback uintptr
	maxEntrypoints, queryEntrypoints, getConfigAttributes                        uintptr
	createConfig, destroyConfig, createSurfaces, destroySurfaces                 uintptr
	createContext, destroyContext, createBuffer, destroyBuffer                   uintptr
	beginPicture, renderPicture, endPicture, syncSurface                         uintptr
	deriveImage, destroyImage, mapBuffer, unmapBuffer                            uintptr
}

func load() error {
	lib.once.Do(func() {
		va, err := purego.Dlopen("libva.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no libva (%v)", encode.ErrUnsupported, err)
			return
		}
		drm, err := purego.Dlopen("libva-drm.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no libva-drm (%v)", encode.ErrUnsupported, err)
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
			{va, &lib.maxEntrypoints, "vaMaxNumEntrypoints"},
			{va, &lib.queryEntrypoints, "vaQueryConfigEntrypoints"},
			{va, &lib.getConfigAttributes, "vaGetConfigAttributes"},
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
			{va, &lib.deriveImage, "vaDeriveImage"},
			{va, &lib.destroyImage, "vaDestroyImage"},
			{va, &lib.mapBuffer, "vaMapBuffer"},
			{va, &lib.unmapBuffer, "vaUnmapBuffer"},
		} {
			if *f.p, err = purego.Dlsym(f.h, f.name); err != nil {
				lib.err = fmt.Errorf("%w: libva lacks %s", encode.ErrUnsupported, f.name)
				return
			}
		}
		lib.setInfoCallback, _ = purego.Dlsym(va, "vaSetInfoCallback")
		useWSLDriver()
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
	n := 0
	for *(*byte)(ptr(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr(p)), n))
}

func status(what string, s int32) error {
	if s == vaStatusSuccess {
		return nil
	}
	r, _, _ := purego.SyscallN(lib.errorStr, uintptr(s))
	return fmt.Errorf("vaapi: %s: %s (%d)", what, cString(r), s)
}

func put8(b []byte, off int, v uint8)   { b[off] = v }
func put16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
func put32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }

// display is an initialized VA display on a DRM node.
type display struct {
	fd     int
	dpy    uintptr
	vendor string
	node   gpu.RenderNode
}

// openDisplay initializes libva on the render node of adapter (by LUID),
// or on the first that initializes when the LUID matches none.
func openDisplay(adapter gpu.Adapter) (*display, error) {
	if err := load(); err != nil {
		return nil, err
	}
	nodes, err := gpu.RenderNodes()
	if err != nil || len(nodes) == 0 {
		return nil, fmt.Errorf("%w: no DRM render node", encode.ErrUnsupported)
	}
	for i, n := range nodes {
		if n.Adapter.LUID == adapter.LUID {
			nodes[0], nodes[i] = nodes[i], nodes[0]
			break
		}
	}
	var errs []error
	for _, n := range nodes {
		d, err := initDisplay(n)
		if err == nil {
			return d, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", n.Path, err))
	}
	return nil, fmt.Errorf("%w: %v", encode.ErrUnsupported, errors.Join(errs...))
}

func initDisplay(n gpu.RenderNode) (*display, error) {
	fd, err := syscall.Open(n.Path, syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	d := &display{fd: fd, node: n}
	r, _, _ := purego.SyscallN(lib.getDisplayDRM, uintptr(fd))
	if d.dpy = r; d.dpy == 0 {
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

func (d *display) entrypoints(profile uintptr) map[int32]bool {
	n := call(lib.maxEntrypoints, d.dpy)
	if n <= 0 {
		return nil
	}
	list := make([]int32, n)
	var got int32
	if call(lib.queryEntrypoints, d.dpy, profile, uintptr(unsafe.Pointer(&list[0])), uintptr(unsafe.Pointer(&got))) != vaStatusSuccess {
		return nil
	}
	out := map[int32]bool{}
	for _, e := range list[:got] {
		out[e] = true
	}
	return out
}

// configAttrib is VAConfigAttrib.
type configAttrib struct{ typ, value uint32 }

// attribs reads the values of the attributes of types for profile and
// entrypoint; an unsupported one reads vaAttribNotSupported.
func (d *display) attribs(profile, entrypoint uintptr, types ...uint32) map[uint32]uint32 {
	list := make([]configAttrib, len(types))
	for i, t := range types {
		list[i].typ = t
	}
	out := map[uint32]uint32{}
	if call(lib.getConfigAttributes, d.dpy, profile, entrypoint, uintptr(unsafe.Pointer(&list[0])), uintptr(len(list))) != vaStatusSuccess {
		for _, t := range types {
			out[t] = vaAttribNotSupported
		}
		return out
	}
	for _, a := range list {
		out[a.typ] = a.value
	}
	return out
}

func (d *display) createConfig(profile, entrypoint uintptr, attribs []configAttrib) (uint32, error) {
	id := new(uint32)
	var p uintptr
	if len(attribs) > 0 {
		p = uintptr(unsafe.Pointer(&attribs[0]))
	}
	err := status("vaCreateConfig", call(lib.createConfig, d.dpy, profile, entrypoint, p, uintptr(len(attribs)), uintptr(unsafe.Pointer(id))))
	return *id, err
}

// surfaceAttrib is VASurfaceAttrib with an integer or pointer value.
type surfaceAttrib struct {
	typ, flags uint32
	valueType  uint32
	_          uint32
	value      uint64
}

// createSurfaces makes n surfaces of rt format holding fourcc.
func (d *display) createSurfaces(rt uint32, fourcc uint32, w, h, n int) ([]uint32, error) {
	ids := make([]uint32, n)
	attr := []surfaceAttrib{{typ: vaSurfaceAttribPixelFormat, flags: vaSurfaceAttribSettable, valueType: vaGenericValueInteger, value: uint64(fourcc)}}
	err := status("vaCreateSurfaces", call(lib.createSurfaces, d.dpy, uintptr(rt), uintptr(w), uintptr(h),
		uintptr(unsafe.Pointer(&ids[0])), uintptr(n), uintptr(unsafe.Pointer(&attr[0])), 1))
	return ids, err
}

// drmPrimeDescriptor is VADRMPRIMESurfaceDescriptor (312 bytes).
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

// importSurface makes a VA surface of a DMA-BUF's planes, with no copy.
func (d *display) importSurface(rt uint32, desc *drmPrimeDescriptor) (uint32, error) {
	id := new(uint32)
	attr := []surfaceAttrib{
		{typ: vaSurfaceAttribMemoryType, flags: vaSurfaceAttribSettable, valueType: vaGenericValueInteger, value: vaMemTypeDRMPrime2},
		{typ: vaSurfaceAttribExternalBuf, flags: vaSurfaceAttribSettable, valueType: vaGenericValuePointer, value: uint64(uintptr(unsafe.Pointer(desc)))},
	}
	err := status("vaCreateSurfaces (DMA-BUF)", call(lib.createSurfaces, d.dpy, uintptr(rt), uintptr(desc.width), uintptr(desc.height),
		uintptr(unsafe.Pointer(id)), 1, uintptr(unsafe.Pointer(&attr[0])), uintptr(len(attr))))
	return *id, err
}

func (d *display) destroySurfaces(ids ...uint32) {
	if len(ids) > 0 {
		call(lib.destroySurfaces, d.dpy, uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	}
}

func (d *display) createContext(config uint32, w, h int, targets []uint32) (uint32, error) {
	id := new(uint32)
	var p uintptr
	if len(targets) > 0 {
		p = uintptr(unsafe.Pointer(&targets[0]))
	}
	err := status("vaCreateContext", call(lib.createContext, d.dpy, uintptr(config), uintptr(w), uintptr(h), vaProgressive,
		p, uintptr(len(targets)), uintptr(unsafe.Pointer(id))))
	return *id, err
}

// buffer makes a VA buffer of data, which libva copies.
func (d *display) buffer(ctx, typ uint32, data []byte) (uint32, error) {
	id := new(uint32)
	err := status("vaCreateBuffer", call(lib.createBuffer, d.dpy, uintptr(ctx), uintptr(typ), uintptr(len(data)), 1,
		uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(id))))
	return *id, err
}

// vaImage is VAImage (120 bytes).
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

// upload copies planes, rows of rowBytes at pitches, into surface,
// heights giving each plane's rows.
func (d *display) upload(surface uint32, planes [4]unsafe.Pointer, pitches [4]uint32, rowBytes int, heights []int) error {
	img := new(vaImage)
	if err := status("vaDeriveImage", call(lib.deriveImage, d.dpy, uintptr(surface), uintptr(unsafe.Pointer(img)))); err != nil {
		return err
	}
	defer call(lib.destroyImage, d.dpy, uintptr(img.id))
	base := new(uintptr)
	if err := status("vaMapBuffer", call(lib.mapBuffer, d.dpy, uintptr(img.buf), uintptr(unsafe.Pointer(base)))); err != nil {
		return err
	}
	defer call(lib.unmapBuffer, d.dpy, uintptr(img.buf))
	for p, rows := range heights {
		if p >= int(img.numPlanes) {
			break
		}
		for y := range rows {
			src := unsafe.Slice((*byte)(unsafe.Add(planes[p], y*int(pitches[p]))), rowBytes)
			dst := unsafe.Slice((*byte)(ptr(*base+uintptr(img.offsets[p])+uintptr(y)*uintptr(img.pitches[p]))), rowBytes)
			copy(dst, src)
		}
	}
	return nil
}

// Probe reports what the VA-API driver of adapter's render node
// encodes: HEVC Main, and Main 10 where it has the profile.
func Probe(adapter gpu.Adapter) (encode.Caps, error) {
	d, err := openDisplay(adapter)
	if err != nil {
		return encode.Caps{}, err
	}
	defer d.close()
	encodes := func(profile uintptr) bool {
		eps := d.entrypoints(profile)
		return eps[vaEntrypointEncSlice] || eps[vaEntrypointEncSliceLP]
	}
	if !encodes(vaProfileHEVCMain) {
		return encode.Caps{}, fmt.Errorf("%w: %s encodes no HEVC", encode.ErrUnsupported, d.vendor)
	}
	chroma := []encode.Chroma{encode.NV12}
	if encodes(vaProfileHEVCMain10) {
		chroma = append(chroma, encode.P010)
	}
	return encode.Caps{Adapter: adapter, Codecs: []encode.Codec{encode.HEVC}, Chroma: map[encode.Codec][]encode.Chroma{encode.HEVC: chroma}, Engines: 1}, nil
}

// Adapters lists the render nodes whose VA-API driver encodes HEVC.
func Adapters() ([]gpu.Adapter, error) {
	if err := load(); err != nil {
		return nil, err
	}
	nodes, err := gpu.RenderNodes()
	if err != nil {
		return nil, err
	}
	var out []gpu.Adapter
	for _, n := range nodes {
		d, err := initDisplay(n)
		if err != nil {
			continue
		}
		eps := d.entrypoints(vaProfileHEVCMain)
		if eps[vaEntrypointEncSlice] || eps[vaEntrypointEncSliceLP] {
			a := n.Adapter
			if d.vendor != "" {
				a.Name = d.vendor
			}
			out = append(out, a)
		}
		d.close()
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no VA-API driver encodes HEVC here", encode.ErrUnsupported)
	}
	return out, nil
}

// useWSLDriver points libva at Mesa's d3d12 driver under WSL, where the
// render node's driver tells libva nothing.
func useWSLDriver() {
	if os.Getenv("LIBVA_DRIVER_NAME") != "" {
		return
	}
	if _, err := os.Stat("/dev/dxg"); err == nil {
		if m, _ := filepath.Glob("/usr/lib/*/dri/d3d12_drv_video.so"); len(m) > 0 {
			os.Setenv("LIBVA_DRIVER_NAME", "d3d12")
		}
	}
}
