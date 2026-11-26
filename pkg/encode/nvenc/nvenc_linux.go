//go:build linux && !android && (amd64 || arm64)

package nvenc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// asyncEvents: Linux NVENC has no completion events; nvEncLockBitstream
// waits for the picture.
const asyncEvents = false

const (
	deviceTypeCUDA        = 1
	resourceCUDADevicePtr = 1

	cudaMemoryHost   = 1
	cudaMemoryDevice = 2
	memcpy2DSize     = 128
)

var lib struct {
	once sync.Once
	err  error

	getMax, create uintptr

	cuInit, cuDeviceGetCount, cuDeviceGet, cuDeviceGetName, cuDeviceGetPCIBusID uintptr
	cuDeviceTotalMem, cuCtxCreate, cuCtxDestroy, cuCtxPush, cuCtxPop            uintptr
	cuMemAllocPitch, cuMemFree, cuMemcpy2D                                      uintptr
}

func load() error {
	lib.once.Do(func() {
		cuda, err := purego.Dlopen("libcuda.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no NVIDIA driver (%v)", encode.ErrUnsupported, err)
			return
		}
		nv, err := purego.Dlopen("libnvidia-encode.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no NVENC library (%v)", encode.ErrUnsupported, err)
			return
		}
		for _, f := range []struct {
			h    uintptr
			p    *uintptr
			name string
		}{
			{nv, &lib.getMax, "NvEncodeAPIGetMaxSupportedVersion"},
			{nv, &lib.create, "NvEncodeAPICreateInstance"},
			{cuda, &lib.cuInit, "cuInit"},
			{cuda, &lib.cuDeviceGetCount, "cuDeviceGetCount"},
			{cuda, &lib.cuDeviceGet, "cuDeviceGet"},
			{cuda, &lib.cuDeviceGetName, "cuDeviceGetName"},
			{cuda, &lib.cuDeviceGetPCIBusID, "cuDeviceGetPCIBusId"},
			{cuda, &lib.cuDeviceTotalMem, "cuDeviceTotalMem_v2"},
			{cuda, &lib.cuCtxCreate, "cuCtxCreate_v2"},
			{cuda, &lib.cuCtxDestroy, "cuCtxDestroy_v2"},
			{cuda, &lib.cuCtxPush, "cuCtxPushCurrent_v2"},
			{cuda, &lib.cuCtxPop, "cuCtxPopCurrent_v2"},
			{cuda, &lib.cuMemAllocPitch, "cuMemAllocPitch_v2"},
			{cuda, &lib.cuMemFree, "cuMemFree_v2"},
			{cuda, &lib.cuMemcpy2D, "cuMemcpy2D_v2"},
		} {
			if *f.p, err = purego.Dlsym(f.h, f.name); err != nil {
				lib.err = fmt.Errorf("%w: %v", encode.ErrUnsupported, err)
				return
			}
		}
		if r := cuda32(ccall(lib.cuInit, 0)); r != 0 {
			lib.err = fmt.Errorf("%w: cuInit: CUDA error %d", encode.ErrUnsupported, r)
		}
	})
	return lib.err
}

func entryPoints() (getMax, create uintptr, err error) {
	if err := load(); err != nil {
		return 0, 0, err
	}
	return lib.getMax, lib.create, nil
}

func cuda32(r uintptr) int32 { return int32(r) }

//go:uintptrescapes
func ccall(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return uintptr(uint32(r))
}

// ccall2 is ccall for the per-frame path. Pointers passed must be to the
// heap.
func ccall2(fn, a, b uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, a, b)
	return uintptr(uint32(r))
}

// guidArgs passes a GUID by value: the System V and AArch64 calling
// conventions put a struct of 16 bytes of integers in two registers.
func guidArgs(g *guid) []uintptr {
	w := (*[2]uint64)(unsafe.Pointer(g))
	return []uintptr{uintptr(w[0]), uintptr(w[1])}
}

func yieldThread() { syscall.Syscall(syscall.SYS_SCHED_YIELD, 0, 0, 0) }

func cudaErr(what string, r uintptr) error {
	if r == 0 {
		return nil
	}
	return fmt.Errorf("nvenc: %s: CUDA error %d", what, int32(r))
}

// cudaDevice is one CUDA device as an adapter.
type cudaDevice struct {
	ordinal int32
	adapter gpu.Adapter
}

var (
	devicesOnce sync.Once
	devices     []cudaDevice
	devicesErr  error
)

// Adapters lists the CUDA devices NVENC can encode on. An adapter's LUID
// is its DRM render node's device number where the GPU has one, as
// pkg/gpu names Linux adapters; under WSL, where it has none, it is the
// CUDA ordinal plus one.
func Adapters() ([]gpu.Adapter, error) {
	devicesOnce.Do(func() {
		if devicesErr = load(); devicesErr != nil {
			return
		}
		var n int32
		if devicesErr = cudaErr("cuDeviceGetCount", ccall(lib.cuDeviceGetCount, uintptr(unsafe.Pointer(&n)))); devicesErr != nil {
			return
		}
		nodes, _ := gpu.RenderNodes()
		for i := int32(0); i < n; i++ {
			var dev int32
			if ccall(lib.cuDeviceGet, uintptr(unsafe.Pointer(&dev)), uintptr(i)) != 0 {
				continue
			}
			name := make([]byte, 256)
			ccall(lib.cuDeviceGetName, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), uintptr(dev))
			bus := make([]byte, 32)
			ccall(lib.cuDeviceGetPCIBusID, uintptr(unsafe.Pointer(&bus[0])), uintptr(len(bus)), uintptr(dev))
			var mem uint64
			ccall(lib.cuDeviceTotalMem, uintptr(unsafe.Pointer(&mem)), uintptr(dev))
			a := gpu.Adapter{LUID: gpu.LUID(i) + 1, Vendor: gpu.VendorNVIDIA, Name: cBytes(name), VRAMBytes: mem}
			if node, ok := nodeForBus(nodes, cBytes(bus)); ok {
				a.LUID, a.DeviceID = node.Adapter.LUID, node.Adapter.DeviceID
			}
			devices = append(devices, cudaDevice{ordinal: dev, adapter: a})
		}
		if len(devices) == 0 {
			devicesErr = fmt.Errorf("%w: no CUDA device", encode.ErrUnsupported)
		}
	})
	out := make([]gpu.Adapter, len(devices))
	for i, d := range devices {
		out[i] = d.adapter
	}
	return out, devicesErr
}

// nodeForBus finds the render node of the PCI device at bus
// ("0000:01:00.0"), whose sysfs device link ends in that address.
func nodeForBus(nodes []gpu.RenderNode, bus string) (gpu.RenderNode, bool) {
	for _, n := range nodes {
		link, err := os.Readlink(filepath.Join("/sys/class/drm", filepath.Base(n.Path), "device"))
		if err == nil && bus != "" && strings.EqualFold(filepath.Base(link), bus) {
			return n, true
		}
	}
	return gpu.RenderNode{}, false
}

func cBytes(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// ordinal is the CUDA device of adapter: the one with its LUID, or the
// first.
func ordinal(adapter gpu.Adapter) (int32, error) {
	if _, err := Adapters(); err != nil {
		return 0, err
	}
	for _, d := range devices {
		if d.adapter.LUID == adapter.LUID {
			return d.ordinal, nil
		}
	}
	return devices[0].ordinal, nil
}

// platform is the encoder's CUDA context and the device buffer frames are
// uploaded to.
type platform struct {
	event uintptr // no events on Linux; always 0

	ctx    uintptr
	devPtr uint64
	pitch  uintptr
	rows   int
	key    uintptr
	popped uintptr
	copy2D [memcpy2DSize]byte
	maps   map[uint64]mapping
}

// mapping is a DMA-BUF frame mapped for reading, by its pool serial.
type mapping struct {
	mem []byte
	fd  int32
}

func (p *platform) close() {
	for _, m := range p.maps {
		syscall.Munmap(m.mem)
	}
	p.maps = nil
	if p.ctx == 0 {
		return
	}
	if p.devPtr != 0 {
		ccall(lib.cuCtxPush, p.ctx)
		ccall(lib.cuMemFree, uintptr(p.devPtr))
		var old uintptr
		ccall(lib.cuCtxPop, uintptr(unsafe.Pointer(&old)))
		p.devPtr = 0
	}
	ccall(lib.cuCtxDestroy, p.ctx)
	p.ctx = 0
}

func newContext(adapter gpu.Adapter) (uintptr, error) {
	dev, err := ordinal(adapter)
	if err != nil {
		return 0, err
	}
	var ctx uintptr
	if err := cudaErr("cuCtxCreate", ccall(lib.cuCtxCreate, uintptr(unsafe.Pointer(&ctx)), 0, uintptr(dev))); err != nil {
		return 0, err
	}
	// cuCtxCreate leaves the context current on this thread; Encode
	// pushes it on whichever thread it runs.
	var old uintptr
	ccall(lib.cuCtxPop, uintptr(unsafe.Pointer(&old)))
	return ctx, nil
}

// Probe reports what NVENC offers on adapter's CUDA device.
func Probe(adapter gpu.Adapter) (encode.Caps, error) {
	ctx, err := newContext(adapter)
	if err != nil {
		return encode.Caps{}, err
	}
	defer ccall(lib.cuCtxDestroy, ctx)
	return probe(deviceTypeCUDA, ctx, adapter)
}

// Open starts an encoder on adapter's CUDA device. Encode takes Linux
// frames (capture.DMABuf surfaces) with their planes in memory, or in a
// linear DMA-BUF it maps, and uploads each to the GPU.
func Open(adapter gpu.Adapter, cfg encode.Config, opts Options) (*Encoder, error) {
	if err := load(); err != nil {
		return nil, err
	}
	ctx, err := newContext(adapter)
	if err != nil {
		return nil, err
	}
	e, err := openEncoder(deviceTypeCUDA, ctx, adapter, cfg, opts, platform{ctx: ctx})
	if err != nil {
		if e == nil {
			ccall(lib.cuCtxDestroy, ctx)
		}
		return nil, err
	}
	return e, nil
}

func (e *Encoder) startEvents() error { return nil }
func (e *Encoder) stopEvents()        {}
func (e *Encoder) waitDone() error    { return nil }

// linuxFormat is the NVENC buffer format of a DMABuf and the bytes per
// pixel of its first plane, with the rows of its planes together.
func linuxFormat(d *capture.DMABuf) (format uint32, bpp int, rows func(h int) int, err error) {
	switch d.Fourcc {
	case capture.FourccXRGB8888, capture.FourccARGB8888:
		return bufARGB, 4, func(h int) int { return h }, nil
	case capture.FourccXBGR8888, capture.FourccABGR8888:
		return bufABGR, 4, func(h int) int { return h }, nil
	case capture.FourccNV12:
		return bufNV12, 1, func(h int) int { return h + h/2 }, nil
	case capture.FourccP010:
		return bufYUV42010, 2, func(h int) int { return h + h/2 }, nil
	}
	return 0, 0, nil, fmt.Errorf("%w: DRM format %#x", encode.ErrUnsupported, d.Fourcc)
}

// input copies the frame into the encoder's device buffer, which is
// registered with NVENC once.
func (e *Encoder) input(f capture.Frame) (registration, error) {
	d := capture.DMABufOf(f.Surface)
	if d == nil {
		return registration{}, fmt.Errorf("nvenc: frame has no picture")
	}
	format, bpp, rows, err := linuxFormat(d)
	if err != nil {
		return registration{}, err
	}
	planes, err := e.plat.cpuPlanes(d)
	if err != nil {
		return registration{}, err
	}
	p := &e.plat
	if r, ok := e.registered[p.key]; ok && r.format == format && p.rows == rows(f.Height) {
		return r, p.upload(planes, d, f.Width*bpp, f.Height)
	}
	if r, ok := e.registered[p.key]; ok {
		ccall(e.api.fn(fnUnregisterResource), e.enc, r.ptr)
		delete(e.registered, p.key)
	}
	if err := p.alloc(f.Width*bpp, rows(f.Height)); err != nil {
		return registration{}, err
	}
	r, err := e.registerResource(resourceCUDADevicePtr, uintptr(p.devPtr), f.Width, f.Height, uint32(p.pitch), format)
	if err != nil {
		return registration{}, err
	}
	p.key = uintptr(p.devPtr)
	e.registered[p.key] = r
	return r, p.upload(planes, d, f.Width*bpp, f.Height)
}

func (p *platform) alloc(rowBytes, rows int) error {
	ccall(lib.cuCtxPush, p.ctx)
	defer func() {
		var old uintptr
		ccall(lib.cuCtxPop, uintptr(unsafe.Pointer(&old)))
	}()
	if p.devPtr != 0 {
		ccall(lib.cuMemFree, uintptr(p.devPtr))
		p.devPtr = 0
	}
	if err := cudaErr("cuMemAllocPitch", ccall(lib.cuMemAllocPitch, uintptr(unsafe.Pointer(&p.devPtr)),
		uintptr(unsafe.Pointer(&p.pitch)), uintptr(rowBytes), uintptr(rows), 16)); err != nil {
		return err
	}
	p.rows = rows
	return nil
}

// upload copies the planes, rowBytes wide, into the device buffer: the
// second plane, when there is one, starts at row h.
func (p *platform) upload(planes [4]unsafe.Pointer, d *capture.DMABuf, rowBytes, h int) error {
	ccall2(lib.cuCtxPush, p.ctx, 0)
	defer ccall2(lib.cuCtxPop, uintptr(unsafe.Pointer(&p.popped)), 0)
	heights := [2]int{h, h / 2}
	dstRow := 0
	for i := 0; i < max(d.Planes, 1) && i < 2; i++ {
		c := p.copy2D[:]
		clear(c)
		put32(c, 16, cudaMemoryHost)
		put64(c, 24, uint64(uintptr(planes[i])))
		put64(c, 48, uint64(d.Pitches[i]))
		put64(c, 64, uint64(dstRow))
		put32(c, 72, cudaMemoryDevice)
		put64(c, 88, p.devPtr)
		put64(c, 104, uint64(p.pitch))
		put64(c, 112, uint64(rowBytes))
		put64(c, 120, uint64(heights[i]))
		if err := cudaErr("cuMemcpy2D", ccall2(lib.cuMemcpy2D, uintptr(unsafe.Pointer(&c[0])), 0)); err != nil {
			return err
		}
		dstRow += heights[i]
	}
	return nil
}

// cpuPlanes finds the frame's planes in memory: its CPU planes, or its
// DMA-BUF mapped. Only a linear buffer reads right through a mapping.
func (p *platform) cpuPlanes(d *capture.DMABuf) ([4]unsafe.Pointer, error) {
	var out [4]unsafe.Pointer
	if d.HasCPU() {
		return d.CPU, nil
	}
	if d.Planes == 0 || d.FDs[0] <= 0 {
		return out, fmt.Errorf("nvenc: frame has neither memory nor DMA-BUF planes")
	}
	if d.Modifier != capture.ModifierLinear && d.Modifier != capture.ModifierInvalid {
		return out, fmt.Errorf("%w: tiled DMA-BUF (modifier %#x) needs a GPU import", encode.ErrUnsupported, d.Modifier)
	}
	if p.maps == nil {
		p.maps = map[uint64]mapping{}
	}
	m, ok := p.maps[d.Serial]
	if !ok || m.fd != d.FDs[0] {
		if ok {
			syscall.Munmap(m.mem)
		}
		size, err := syscall.Seek(int(d.FDs[0]), 0, 2)
		if err != nil || size <= 0 {
			return out, fmt.Errorf("nvenc: DMA-BUF size: %v", err)
		}
		mem, err := syscall.Mmap(int(d.FDs[0]), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
		if err != nil {
			return out, fmt.Errorf("nvenc: map DMA-BUF: %w", err)
		}
		m = mapping{mem: mem, fd: d.FDs[0]}
		p.maps[d.Serial] = m
	}
	for i := 0; i < d.Planes; i++ {
		out[i] = unsafe.Pointer(&m.mem[d.Offsets[i]])
	}
	return out, nil
}
