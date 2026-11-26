package transport

import (
	"net"
	"net/netip"
)

// listenUDP binds laddr, nil for any address and port, in peer's address
// family. macOS can give a dual-stack socket an ephemeral port that a
// socket bound to one IPv4 address already holds; that socket then gets
// every packet sent to the port, and the dual-stack one hears nothing.
func listenUDP(laddr *net.UDPAddr, peer netip.Addr) (*net.UDPConn, error) {
	network := "udp6"
	if peer.Unmap().Is4() {
		network = "udp4"
	}
	if laddr != nil && laddr.IP.IsUnspecified() {
		laddr = &net.UDPAddr{Port: laddr.Port}
	}
	return net.ListenUDP(network, laddr)
}
