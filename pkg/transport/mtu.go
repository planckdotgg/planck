package transport

import (
	"net"
	"net/netip"
)

// routeMTU is the MTU of the local interface the system routes to remote
// through, or 0 when it cannot tell. No packet is sent.
func routeMTU(remote netip.Addr) int {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(remote.Unmap(), 9)))
	if err != nil {
		return 0
	}
	local := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	c.Close()
	ifs, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == local {
					return ifc.MTU
				}
			}
		}
	}
	return 0
}

// lanDatagramOK reports whether media datagrams of wire.MaxLANDatagram
// reach remote unfragmented from this end.
func lanDatagramOK(remote netip.Addr) bool { return routeMTU(remote) >= 1500 }
