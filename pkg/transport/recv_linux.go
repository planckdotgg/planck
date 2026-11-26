package transport

import (
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// blockingRead makes fd blocking with a receive timeout.
func blockingRead(fd uintptr) error {
	tv := unix.NsecToTimeval(rawReadTimeout.Nanoseconds())
	if err := unix.SetsockoptTimeval(int(fd), unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return err
	}
	return unix.SetNonblock(int(fd), false)
}

func (r *rawReader) recvfrom(fd uintptr) {
	r.fromSz = int32(unsafe.Sizeof(r.from))
	n, _, errno := syscall.Syscall6(syscall.SYS_RECVFROM, fd, uintptr(unsafe.Pointer(&r.buf[0])), uintptr(len(r.buf)), 0,
		uintptr(unsafe.Pointer(&r.from)), uintptr(unsafe.Pointer(&r.fromSz)))
	if errno != 0 {
		r.n, r.err = 0, &net.OpError{Op: "recvfrom", Net: "udp", Err: errno}
		return
	}
	r.n, r.err = int(n), nil
}
