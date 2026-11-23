//go:build windows

// Package win holds the minimal COM, DXGI, and Direct3D 11 bindings Planck
// needs, called through syscall with no cgo.
package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Object is any COM interface pointer. Its first word is the vtable.
type Object struct {
	vtbl *[512]uintptr
}

type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type HRESULT uint32

func (h HRESULT) Failed() bool { return int32(h) < 0 }

func (h HRESULT) Error() string { return fmt.Sprintf("HRESULT 0x%08x", uint32(h)) }

func hr(r uintptr) error {
	if HRESULT(r).Failed() {
		return HRESULT(r)
	}
	return nil
}

// call keeps what pointer arguments point to on the heap and alive until it
// returns.
//
//go:uintptrescapes
func (o *Object) call(index int, args ...uintptr) uintptr {
	all := make([]uintptr, 0, 8)
	all = append(all, uintptr(unsafe.Pointer(o)))
	all = append(all, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[index], all...)
	return r
}

// The fixed-arity calls below keep the per-frame paths free of allocations.

func (o *Object) call0(index int) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[index], uintptr(unsafe.Pointer(o)))
	return r
}

func (o *Object) call1(index int, a uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[index], uintptr(unsafe.Pointer(o)), a)
	return r
}

func (o *Object) call2(index int, a, b uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[index], uintptr(unsafe.Pointer(o)), a, b)
	return r
}

func (o *Object) call3(index int, a, b, c uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[index], uintptr(unsafe.Pointer(o)), a, b, c)
	return r
}

func (o *Object) Release() {
	if o != nil {
		o.call0(2)
	}
}

func (o *Object) QueryInterface(iid *GUID) (*Object, error) {
	var out *Object
	if err := hr(o.call2(0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))); err != nil {
		return nil, err
	}
	return out, nil
}

func utf16(s []uint16) string { return syscall.UTF16ToString(s) }

func NewGUID(d1 uint32, d2, d3 uint16, d4 [8]byte) GUID {
	return GUID{Data1: d1, Data2: d2, Data3: d3, Data4: d4}
}

// Ptr turns an address returned by native code into a pointer. The memory is
// not Go managed, so the conversion cannot confuse the garbage collector.
func Ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// ObjectAt views a raw interface pointer, such as capture.Frame.Surface.
func ObjectAt(p uintptr) *Object { return (*Object)(Ptr(p)) }
