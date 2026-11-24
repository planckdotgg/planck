//go:build !windows

package transport

import (
	"net"
	"net/netip"
)

func newMediaReader(c *net.UDPConn) func([]byte) (int, netip.AddrPort, error) {
	return c.ReadFromUDPAddrPort
}
