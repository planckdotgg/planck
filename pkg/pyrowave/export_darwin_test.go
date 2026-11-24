//go:build darwin && arm64

package pyrowave

import "unsafe"

// ReadSurface copies a decoded BGRA IOSurface to tightly packed BGRA8, for
// tests.
func ReadSurface(surface uintptr, w, h int) []byte {
	syscall6(mtl.ioLock, surface, 1, 0, 0, 0, 0)
	defer syscall6(mtl.ioUnlock, surface, 1, 0, 0, 0, 0)
	base, _, _ := syscall6(mtl.ioBase, surface, 0, 0, 0, 0, 0)
	pitch, _, _ := syscall6(mtl.ioBytesPerRow, surface, 0, 0, 0, 0, 0)
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		copy(out[y*w*4:(y+1)*w*4], unsafe.Slice((*byte)(ptrOf(base+uintptr(y)*pitch)), w*4))
	}
	return out
}
