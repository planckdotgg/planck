package transport

import (
	"errors"
	"net"
)

// socketBufferSteps are the media socket buffer sizes asked for, largest
// first. XNU builds whose limit is kern.ipc.maxsockbuf less mbuf headroom
// refuse 8 MiB with ENOBUFS and leave the socket at its default
// (net.inet.udp.recvspace, 768 KiB), so each refusal steps down.
var socketBufferSteps = [...]int{8 << 20, 6 << 20, 4 << 20, 2 << 20, 1 << 20}

// stepDown calls set with each of socketBufferSteps until one is accepted.
func stepDown(set func(int) error) error {
	var err error
	for _, n := range socketBufferSteps {
		if err = set(n); err == nil {
			return nil
		}
	}
	return err
}

// sizeSocketBuffers asks for the largest buffers c's kernel accepts and
// reads back what it granted.
func sizeSocketBuffers(c *net.UDPConn) (SocketBuffers, error) {
	errRecv := stepDown(c.SetReadBuffer)
	errSend := stepDown(c.SetWriteBuffer)
	b, err := readSocketBuffers(c)
	return b, errors.Join(errRecv, errSend, err)
}

// SocketBuffers are the receive and send buffer sizes the kernel granted a
// media socket, as getsockopt reads them back (Linux reports twice the
// size asked for, its bookkeeping included).
type SocketBuffers struct {
	Recv, Send int
}

// readSocketBuffers reads back the socket's SO_RCVBUF and SO_SNDBUF.
func readSocketBuffers(c *net.UDPConn) (SocketBuffers, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return SocketBuffers{}, err
	}
	var b SocketBuffers
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		if b.Recv, gerr = getsockoptInt(fd, soRcvBuf); gerr != nil {
			return
		}
		b.Send, gerr = getsockoptInt(fd, soSndBuf)
	}); err != nil {
		return SocketBuffers{}, err
	}
	return b, gerr
}
