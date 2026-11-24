//go:build windows

package pyrowave

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	d3dcompiler    = windows.NewLazySystemDLL("d3dcompiler_47.dll")
	procD3DCompile = d3dcompiler.NewProc("D3DCompile")
)

const (
	d3dCompileOptimizationLevel3 = 1 << 15
	blobGetBufferPointer         = 3
	blobGetBufferSize            = 4
)

// compileHLSL compiles one cs_5_0 entry point of src to bytecode.
func compileHLSL(src, entry string) ([]byte, error) {
	if err := procD3DCompile.Find(); err != nil {
		return nil, fmt.Errorf("pyrowave: d3dcompiler_47.dll: %w", err)
	}
	srcB := []byte(src)
	name := append([]byte("pyrowave.hlsl"), 0)
	entryB := append([]byte(entry), 0)
	target := append([]byte("cs_5_0"), 0)
	var code, errs uintptr
	r := d3dCompile(uintptr(unsafe.Pointer(&srcB[0])), uintptr(len(srcB)), uintptr(unsafe.Pointer(&name[0])),
		uintptr(unsafe.Pointer(&entryB[0])), uintptr(unsafe.Pointer(&target[0])),
		uintptr(unsafe.Pointer(&code)), uintptr(unsafe.Pointer(&errs)))
	if errs != 0 {
		msg := string(blobBytes(errs))
		release(&errs)
		if err := hr(r); err != nil {
			release(&code)
			return nil, fmt.Errorf("pyrowave: D3DCompile %s: %w: %s", entry, err, msg)
		}
	}
	if err := hr(r); err != nil {
		return nil, fmt.Errorf("pyrowave: D3DCompile %s: %w", entry, err)
	}
	out := append([]byte(nil), blobBytes(code)...)
	release(&code)
	return out, nil
}

//go:uintptrescapes
func d3dCompile(src, n, name, entry, target, code, errs uintptr) uintptr {
	r, _, _ := procD3DCompile.Call(src, n, name, 0, 0, entry, target, d3dCompileOptimizationLevel3, 0, code, errs)
	return r
}

func blobBytes(blob uintptr) []byte {
	p := call0(blob, blobGetBufferPointer)
	n := call0(blob, blobGetBufferSize)
	return unsafe.Slice((*byte)(ptr(p)), n)
}
