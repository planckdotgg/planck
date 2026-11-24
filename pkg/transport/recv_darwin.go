//go:build darwin

package transport

import (
	"net"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/internal/apple"
)

//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libcRecvfrom = apple.Sym(apple.System, "recvfrom")

// blockingRead makes fd blocking with a receive timeout.
func blockingRead(fd uintptr) error {
	tv := syscall.Timeval{Usec: int32(rawReadTimeout.Microseconds())}
	if err := syscall.SetsockoptTimeval(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return err
	}
	return syscall.SetNonblock(int(fd), false)
}

func (r *rawReader) recvfrom(fd uintptr) {
	r.fromSz = int32(unsafe.Sizeof(r.from))
	n, _, errno := syscall6(libcRecvfrom, fd, uintptr(unsafe.Pointer(&r.buf[0])), uintptr(len(r.buf)), 0,
		uintptr(unsafe.Pointer(&r.from)), uintptr(unsafe.Pointer(&r.fromSz)))
	r.n, r.err = int(n), nil
	if r.n < 0 {
		r.n, r.err = 0, &net.OpError{Op: "recvfrom", Net: "udp", Err: errno}
	}
}
