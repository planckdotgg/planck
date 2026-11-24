package transport

import (
	"encoding/binary"
	"unsafe"
)

// portOf reads a sockaddr port, which is in network byte order.
func portOf(p uint16) uint16 {
	b := (*[2]byte)(unsafe.Pointer(&p))
	return binary.BigEndian.Uint16(b[:])
}
