//go:build windows

package pyrowave

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Minimal Direct3D 11 compute bindings, called through syscall with no
// cgo. Setup goes through call, which keeps pointer arguments alive and in
// place; per-frame calls use the fixed-arity callN with pointers to heap
// memory only, so nothing allocates and no stack move can strand them.

type hresult uint32

func (h hresult) Error() string { return fmt.Sprintf("HRESULT 0x%08x", uint32(h)) }

func hr(r uintptr) error {
	if int32(r) < 0 {
		return hresult(r)
	}
	return nil
}

//go:uintptrescapes
func call(obj uintptr, i int, args ...uintptr) uintptr {
	all := make([]uintptr, 0, 12)
	all = append(all, obj)
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(comFn(obj, i), all...)
	return r
}

func comFn(obj uintptr, i int) uintptr {
	tbl := *(*uintptr)(ptr(obj))
	return *(*uintptr)(ptr(tbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

// ptr turns an address owned by native code into a pointer; that memory is
// not Go managed.
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func call0(obj uintptr, i int) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj)
	return r
}

func call1(obj uintptr, i int, a uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a)
	return r
}

func call2(obj uintptr, i int, a, b uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b)
	return r
}

func call3(obj uintptr, i int, a, b, c uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b, c)
	return r
}

func call4(obj uintptr, i int, a, b, c, d uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b, c, d)
	return r
}

func call5(obj uintptr, i int, a, b, c, d, e uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b, c, d, e)
	return r
}

func call6(obj uintptr, i int, a, b, c, d, e, f uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b, c, d, e, f)
	return r
}

func call8(obj uintptr, i int, a, b, c, d, e, f, g, h uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comFn(obj, i), obj, a, b, c, d, e, f, g, h)
	return r
}

func release(p *uintptr) {
	if *p != 0 {
		call0(*p, 2)
		*p = 0
	}
}

func addRef(p uintptr) { call0(p, 1) }

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

var (
	iidID3D10Multithread = guid{0x9b7e4e00, 0x342c, 0x4106, [8]byte{0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0}}
)

func queryInterface(obj uintptr, iid *guid) (uintptr, error) {
	var out uintptr
	if err := hr(call(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))); err != nil {
		return 0, err
	}
	return out, nil
}

// ID3D11Device and ID3D11DeviceContext vtable slots.
const (
	devCreateBuffer              = 3
	devCreateTexture2D           = 5
	devCreateShaderResourceView  = 7
	devCreateUnorderedAccessView = 8
	devCreateComputeShader       = 18
	devCreateQuery               = 24
	devGetImmediateContext       = 40

	ctxMap                           = 14
	ctxUnmap                         = 15
	ctxEnd                           = 28
	ctxGetData                       = 29
	ctxDispatch                      = 41
	ctxCopySubresourceRegion         = 46
	ctxCopyResource                  = 47
	ctxUpdateSubresource             = 48
	ctxClearUnorderedAccessViewUint  = 51
	ctxCSSetShaderResources          = 67
	ctxCSSetUnorderedAccessViews     = 68
	ctxCSSetShader                   = 69
	ctxCSSetConstantBuffers          = 71
	ctxFlush                         = 111
	texGetDesc                       = 10
	multithreadSetMultithreadProtect = 5
)

const (
	usageDefault = 0
	usageStaging = 3

	bindConstantBuffer  = 0x4
	bindShaderResource  = 0x8
	bindUnorderedAccess = 0x80

	cpuAccessRead = 0x20000

	miscBufferAllowRawViews = 0x20

	formatR32Typeless      = 39
	formatB8G8R8A8Unorm    = 87
	formatR10G10B10A2Unorm = 24
	uavDimBuffer           = 1
	uavDimTexture2D        = 4
	uavFlagRaw             = 1
	srvDimTexture2D        = 4

	mapRead         = 1
	mapWriteDiscard = 4

	queryEvent        = 0
	getDataDoNotFlush = 1
)

type bufferDesc struct {
	ByteWidth, Usage, BindFlags, CPUAccessFlags, MiscFlags, StructureByteStride uint32
}

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type uavDesc struct {
	Format, ViewDimension uint32
	U0, U1, U2            uint32
}

type srvDesc struct {
	Format, ViewDimension uint32
	U0, U1, U2, U3        uint32
}

type mapped struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

type box struct{ Left, Top, Front, Right, Bottom, Back uint32 }

// gpuBuf is a raw buffer with a UAV over all of it.
type gpuBuf struct {
	buf, uav uintptr
	size     int
}

func (b *gpuBuf) release() {
	release(&b.uav)
	release(&b.buf)
}

// d3d is the device state shared by the encoder and decoder.
type d3d struct {
	dev, ctx uintptr
	cb       uintptr
	query    uintptr
	shaders  map[string]uintptr
}

func newD3D(dev uintptr) (*d3d, error) {
	d := &d3d{dev: dev, shaders: map[string]uintptr{}}
	addRef(dev)
	call1(dev, devGetImmediateContext, uintptr(unsafe.Pointer(&d.ctx)))
	if d.ctx == 0 {
		d.close()
		return nil, fmt.Errorf("pyrowave: no immediate context")
	}
	desc := bufferDesc{ByteWidth: uint32((unsafe.Sizeof(params{}) + 15) &^ 15), Usage: usageDefault, BindFlags: bindConstantBuffer}
	if err := hr(call(dev, devCreateBuffer, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&d.cb)))); err != nil {
		d.close()
		return nil, fmt.Errorf("pyrowave: constant buffer: %w", err)
	}
	q := [2]uint32{queryEvent, 0}
	if err := hr(call(dev, devCreateQuery, uintptr(unsafe.Pointer(&q)), uintptr(unsafe.Pointer(&d.query)))); err != nil {
		d.close()
		return nil, fmt.Errorf("pyrowave: query: %w", err)
	}
	// The decode goroutine and the present thread share the immediate
	// context.
	if mt, err := queryInterface(dev, &iidID3D10Multithread); err == nil {
		call1(mt, multithreadSetMultithreadProtect, 1)
		release(&mt)
	}
	return d, nil
}

func (d *d3d) close() {
	for k, s := range d.shaders {
		release(&s)
		delete(d.shaders, k)
	}
	release(&d.query)
	release(&d.cb)
	release(&d.ctx)
	release(&d.dev)
}

func (d *d3d) compile(src string, entries ...string) error {
	for _, e := range entries {
		code, err := compileHLSL(src, e)
		if err != nil {
			return err
		}
		var cs uintptr
		err = hr(call(d.dev, devCreateComputeShader, uintptr(unsafe.Pointer(&code[0])), uintptr(len(code)), 0, uintptr(unsafe.Pointer(&cs))))
		if err != nil {
			return fmt.Errorf("pyrowave: CreateComputeShader %s: %w", e, err)
		}
		d.shaders[e] = cs
	}
	return nil
}

// rawBuffer makes a buffer of size bytes with a raw UAV, optionally
// filled from init.
func (d *d3d) rawBuffer(size int, init []byte) (gpuBuf, error) {
	size = (size + 15) &^ 15
	desc := bufferDesc{ByteWidth: uint32(size), Usage: usageDefault, BindFlags: bindUnorderedAccess, MiscFlags: miscBufferAllowRawViews}
	var b gpuBuf
	b.size = size
	var err error
	if len(init) > 0 {
		data := make([]byte, size)
		copy(data, init)
		sub := struct {
			Mem        uintptr
			Pitch      uint32
			SlicePitch uint32
		}{uintptr(unsafe.Pointer(&data[0])), 0, 0}
		err = hr(call(d.dev, devCreateBuffer, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&sub)), uintptr(unsafe.Pointer(&b.buf))))
	} else {
		err = hr(call(d.dev, devCreateBuffer, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&b.buf))))
	}
	if err != nil {
		return b, fmt.Errorf("pyrowave: CreateBuffer %d bytes: %w", size, err)
	}
	ud := uavDesc{Format: formatR32Typeless, ViewDimension: uavDimBuffer, U0: 0, U1: uint32(size / 4), U2: uavFlagRaw}
	if err := hr(call(d.dev, devCreateUnorderedAccessView, b.buf, uintptr(unsafe.Pointer(&ud)), uintptr(unsafe.Pointer(&b.uav)))); err != nil {
		b.release()
		return b, fmt.Errorf("pyrowave: buffer UAV: %w", err)
	}
	return b, nil
}

// stagingBuffer makes a CPU-readable copy target of size bytes.
func (d *d3d) stagingBuffer(size int) (uintptr, error) {
	desc := bufferDesc{ByteWidth: uint32((size + 15) &^ 15), Usage: usageStaging, CPUAccessFlags: cpuAccessRead}
	var b uintptr
	if err := hr(call(d.dev, devCreateBuffer, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&b)))); err != nil {
		return 0, fmt.Errorf("pyrowave: staging buffer of %d bytes: %w", size, err)
	}
	return b, nil
}

// frameState holds what per-frame calls point at, on the heap.
type frameState struct {
	p      params
	uavs   [6]uintptr
	srvs   [1]uintptr
	counts [6]uint32
	clear  [4]uint32
	m      mapped
	box    box
	done   int32
	view   srvDesc
	tex    texture2DDesc
}

func (d *d3d) setParams(f *frameState) {
	call6(d.ctx, ctxUpdateSubresource, d.cb, 0, 0, uintptr(unsafe.Pointer(&f.p)), 0, 0)
}

func (d *d3d) bind(f *frameState, n int) {
	call4(d.ctx, ctxCSSetUnorderedAccessViews, 0, uintptr(n), uintptr(unsafe.Pointer(&f.uavs[0])), uintptr(unsafe.Pointer(&f.counts[0])))
	call3(d.ctx, ctxCSSetShaderResources, 0, 1, uintptr(unsafe.Pointer(&f.srvs[0])))
	call3(d.ctx, ctxCSSetConstantBuffers, 0, 1, uintptr(unsafe.Pointer(&d.cb)))
}

func (d *d3d) dispatch(f *frameState, shader string, x, y int) {
	call3(d.ctx, ctxCSSetShader, d.shaders[shader], 0, 0)
	d.setParams(f)
	call3(d.ctx, ctxDispatch, uintptr(max(x, 1)), uintptr(max(y, 1)), 1)
}

func (d *d3d) unbind(f *frameState) {
	var zero [6]uintptr
	f.uavs = zero
	f.srvs[0] = 0
	call4(d.ctx, ctxCSSetUnorderedAccessViews, 0, 6, uintptr(unsafe.Pointer(&f.uavs[0])), uintptr(unsafe.Pointer(&f.counts[0])))
	call3(d.ctx, ctxCSSetShaderResources, 0, 1, uintptr(unsafe.Pointer(&f.srvs[0])))
}

// wait flushes and polls the event query until the GPU is done.
func (d *d3d) wait(f *frameState) {
	call1(d.ctx, ctxEnd, d.query)
	call0(d.ctx, ctxFlush)
	for call4(d.ctx, ctxGetData, d.query, uintptr(unsafe.Pointer(&f.done)), 4, getDataDoNotFlush) != 0 {
		runtime.Gosched()
	}
}

const (
	queryTimestamp         = 2
	queryTimestampDisjoint = 3
	ctxBegin               = 27
	maxMarks               = 12
)

// profiler times GPU passes with timestamp queries, for tests and
// benchmarks; nil in normal use.
type profiler struct {
	disjoint uintptr
	ts       [maxMarks]uintptr
	names    [maxMarks]string
	n        int
	freq     struct {
		Frequency uint64
		Disjoint  int32
		_         int32
	}
	stamp uint64
	// Last is the time of each mark since the frame began, in ms.
	Last map[string]float64
}

func (d *d3d) newProfiler() (*profiler, error) {
	p := &profiler{Last: map[string]float64{}}
	q := [2]uint32{queryTimestampDisjoint, 0}
	if err := hr(call(d.dev, devCreateQuery, uintptr(unsafe.Pointer(&q)), uintptr(unsafe.Pointer(&p.disjoint)))); err != nil {
		return nil, err
	}
	for i := range p.ts {
		q = [2]uint32{queryTimestamp, 0}
		if err := hr(call(d.dev, devCreateQuery, uintptr(unsafe.Pointer(&q)), uintptr(unsafe.Pointer(&p.ts[i])))); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (d *d3d) profBegin(p *profiler) {
	if p == nil {
		return
	}
	p.n = 0
	call1(d.ctx, ctxBegin, p.disjoint)
	d.mark(p, "start")
}

func (d *d3d) mark(p *profiler, name string) {
	if p == nil || p.n == maxMarks {
		return
	}
	call1(d.ctx, ctxEnd, p.ts[p.n])
	p.names[p.n] = name
	p.n++
}

// profEnd collects the marks of a frame whose GPU work has finished.
func (d *d3d) profEnd(p *profiler) {
	if p == nil {
		return
	}
	call1(d.ctx, ctxEnd, p.disjoint)
	for call(d.ctx, ctxGetData, p.disjoint, uintptr(unsafe.Pointer(&p.freq)), 16, 0) != 0 {
		runtime.Gosched()
	}
	var start uint64
	for i := 0; i < p.n; i++ {
		for call(d.ctx, ctxGetData, p.ts[i], uintptr(unsafe.Pointer(&p.stamp)), 8, 0) != 0 {
			runtime.Gosched()
		}
		if i == 0 {
			start = p.stamp
		}
		p.Last[p.names[i]] = float64(p.stamp-start) * 1000 / float64(p.freq.Frequency)
	}
}

func (p *profiler) release() {
	if p == nil {
		return
	}
	release(&p.disjoint)
	for i := range p.ts {
		release(&p.ts[i])
	}
}
