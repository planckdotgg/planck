//go:build windows

package transport

import (
	"net"
	"syscall"
	"unsafe"
)

var procRecvfrom = syscall.NewLazyDLL("ws2_32.dll").NewProc("recvfrom")

const soRcvTimeo = 0x1006 // SO_RCVTIMEO

// blockingRead sets a receive timeout on fd; Go's Windows sockets already
// block on calls without an OVERLAPPED.
func blockingRead(fd uintptr) error {
	ms := uint32(rawReadTimeout.Milliseconds())
	return syscall.Setsockopt(syscall.Handle(fd), syscall.SOL_SOCKET, soRcvTimeo, (*byte)(unsafe.Pointer(&ms)), 4)
}

func (r *rawReader) recvfrom(fd uintptr) {
	r.fromSz = int32(unsafe.Sizeof(r.from))
	n, _, errno := syscall.Syscall6(procRecvfrom.Addr(), 6, fd, uintptr(unsafe.Pointer(&r.buf[0])), uintptr(len(r.buf)), 0,
		uintptr(unsafe.Pointer(&r.from)), uintptr(unsafe.Pointer(&r.fromSz)))
	// recvfrom returns a C int: SOCKET_ERROR is -1 in the low 32 bits.
	r.n, r.err = int(int32(n)), nil
	if r.n < 0 {
		r.n, r.err = 0, &net.OpError{Op: "recvfrom", Net: "udp", Err: errno}
	}
}
