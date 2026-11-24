//go:build windows

package transport

import (
	"encoding/binary"
	"net"
	"net/netip"
	"syscall"
	"unsafe"
)

var procRecvfrom = syscall.NewLazyDLL("ws2_32.dll").NewProc("recvfrom")

const wsaEWouldBlock = 10035

// rawReader reads with a blocking recvfrom on the calling thread. Go's own
// read waits on the I/O completion port, and the goroutine it wakes runs
// about a millisecond later on a loaded host; a thread blocked in recvfrom
// returns as the datagram lands. Closing the socket ends the call with an
// error. The sockaddr and its length live on the heap: the call takes
// their addresses as integers.
type rawReader struct {
	conn   *net.UDPConn
	h      uintptr
	from   syscall.RawSockaddrAny
	fromSz int32
	// fallback is Go's read, for a socket that turns out non-blocking.
	fallback bool
}

func newMediaReader(c *net.UDPConn) func([]byte) (int, netip.AddrPort, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return c.ReadFromUDPAddrPort
	}
	r := &rawReader{conn: c}
	if err := rc.Control(func(fd uintptr) { r.h = fd }); err != nil {
		return c.ReadFromUDPAddrPort
	}
	return r.read
}

func (r *rawReader) read(b []byte) (int, netip.AddrPort, error) {
	if r.fallback {
		return r.conn.ReadFromUDPAddrPort(b)
	}
	r.fromSz = int32(unsafe.Sizeof(r.from))
	n, _, errno := syscall.Syscall6(procRecvfrom.Addr(), 6, r.h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0,
		uintptr(unsafe.Pointer(&r.from)), uintptr(unsafe.Pointer(&r.fromSz)))
	// recvfrom returns a C int: SOCKET_ERROR is -1 in the low 32 bits.
	if int32(n) == -1 {
		if errno == wsaEWouldBlock {
			r.fallback = true
			return r.conn.ReadFromUDPAddrPort(b)
		}
		return 0, netip.AddrPort{}, &net.OpError{Op: "recvfrom", Net: "udp", Err: errno}
	}
	return int(int32(n)), r.addr(), nil
}

func (r *rawReader) addr() netip.AddrPort {
	switch r.from.Addr.Family {
	case syscall.AF_INET:
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&r.from))
		return netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), portOf(sa.Port))
	case syscall.AF_INET6:
		sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(&r.from))
		return netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), portOf(sa.Port))
	}
	return netip.AddrPort{}
}

// portOf reads a port in network byte order.
func portOf(p uint16) uint16 {
	b := (*[2]byte)(unsafe.Pointer(&p))
	return binary.BigEndian.Uint16(b[:])
}
