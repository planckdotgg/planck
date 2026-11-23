//go:build windows

package main

import (
	"unsafe"

	"github.com/planckdotgg/planck/internal/win"
)

func unsafePointer(o *win.Object) unsafe.Pointer { return unsafe.Pointer(o) }
