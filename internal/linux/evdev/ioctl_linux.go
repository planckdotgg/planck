package evdev

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// Ioctl issues request on fd with arg, which may be nil.
func Ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

// IoctlInt issues request on fd with an integer argument.
func IoctlInt(fd int, req, arg uintptr) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, arg)
		if errno == unix.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}
