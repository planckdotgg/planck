//go:build darwin || windows

package transport

import (
	"net"
	"net/netip"
	"syscall"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/rtprio"
)

// rawReadTimeout bounds a blocking read, so the reader sees the session
// close and Close, which waits for reads in flight, returns soon.
const rawReadTimeout = 100 * time.Millisecond

// rawReader reads media datagrams with a blocking recvfrom on its own
// thread at a raised class (rtprio). The kernel wakes that thread as a
// datagram lands. Go's netpoller would wake it through another thread
// first, which on a loaded Mac stalled a thread-locked reader for 10 to
// 15 ms at a time. The read runs inside RawConn.Read, which holds the
// socket open for the call, so Close cannot free the descriptor under it.
// The buffer, sockaddr and length are on the heap: the call takes their
// addresses as integers.
type rawReader struct {
	rc     syscall.RawConn
	read   func(fd uintptr) bool
	buf    []byte
	from   syscall.RawSockaddrAny
	fromSz int32
	n      int
	err    error
}

// newMediaReader returns the read for recvLoop. Called on recvLoop's
// goroutine: with a raw reader it locks that goroutine to its thread,
// which recvLoop returning ends.
func newMediaReader(c *net.UDPConn) func([]byte) (int, netip.AddrPort, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return c.ReadFromUDPAddrPort
	}
	var setErr error
	if err := rc.Control(func(fd uintptr) { setErr = blockingRead(fd) }); err != nil || setErr != nil {
		return c.ReadFromUDPAddrPort
	}
	r := &rawReader{rc: rc}
	r.read = func(fd uintptr) bool {
		r.recvfrom(fd)
		return true
	}
	_ = rtprio.Boost()
	return r.next
}

func (r *rawReader) next(b []byte) (int, netip.AddrPort, error) {
	r.buf = b
	if err := r.rc.Read(r.read); err != nil {
		return 0, netip.AddrPort{}, err
	}
	if r.err != nil {
		return 0, netip.AddrPort{}, r.err
	}
	switch r.from.Addr.Family {
	case syscall.AF_INET:
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&r.from))
		return r.n, netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), portOf(sa.Port)), nil
	case syscall.AF_INET6:
		sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(&r.from))
		return r.n, netip.AddrPortFrom(netip.AddrFrom16(sa.Addr), portOf(sa.Port)), nil
	}
	return r.n, netip.AddrPort{}, nil
}
