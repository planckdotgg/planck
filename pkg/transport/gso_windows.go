package transport

import (
	"encoding/binary"
	"net"

	"golang.org/x/sys/windows"
)

const (
	ipprotoUDP     = 17
	udpSendMsgSize = 2 // UDP_SEND_MSG_SIZE, Windows 10 1809 and later
	// segmentOOBLen is WSA_CMSG_SPACE(sizeof(DWORD)): a 16-byte
	// WSACMSGHDR (SIZE_T length, INT level, INT type), then the DWORD
	// padded to 8.
	segmentOOBLen = 24
)

// gsoSupported asks the socket for UDP_SEND_MSG_SIZE. Windows before 1809
// does not know the option; it would also ignore the control message and
// send the whole train as one fragmented datagram.
func gsoSupported(c *net.UDPConn) bool {
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	if err := rc.Control(func(fd uintptr) {
		_, gerr := windows.GetsockoptInt(windows.Handle(fd), ipprotoUDP, udpSendMsgSize)
		ok = gerr == nil
	}); err != nil {
		return false
	}
	return ok
}

// putSegmentOOB writes the UDP_SEND_MSG_SIZE control message for
// segments of seg bytes into oob.
func putSegmentOOB(oob []byte, seg int) {
	binary.LittleEndian.PutUint64(oob[0:], 16+4)
	binary.LittleEndian.PutUint32(oob[8:], ipprotoUDP)
	binary.LittleEndian.PutUint32(oob[12:], udpSendMsgSize)
	binary.LittleEndian.PutUint64(oob[16:], uint64(uint32(seg)))
}
