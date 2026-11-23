package main

import (
	"syscall"
	_ "unsafe"
)

// syscall6 calls a C function without allocating, as purego.SyscallN does
// (see the vendored gpusurface's ccall_darwin.go).
//
//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)
