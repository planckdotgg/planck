//go:build linux && !android && (amd64 || arm64)

package nvdec

import "unsafe"

func unsafeBytes(p uintptr, n int) []byte { return unsafe.Slice((*byte)(ptr(p)), n) }
