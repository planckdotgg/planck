// Package linkinfo finds the local interface that routes to a peer, whether
// it is wired, and its link speed.
package linkinfo

import (
	"errors"
	"net"
	"net/netip"

	"github.com/planckdotgg/planck/pkg/defaults"
)

var errNoInterface = errors.New("linkinfo: no interface holds the route's local address")

// Route returns the local interface the system routes to remote through.
// On error the Link is zero (unknown).
func Route(remote netip.Addr) (name string, link defaults.Link, err error) {
	ifc, err := routeInterface(remote)
	if err != nil {
		return "", defaults.Link{}, err
	}
	link, err = interfaceLink(ifc)
	if err != nil {
		return ifc.Name, defaults.Link{}, err
	}
	return ifc.Name, link, nil
}

// routeInterface asks the kernel for the source address it would use toward
// remote (a UDP connect sends nothing) and returns the interface holding it.
func routeInterface(remote netip.Addr) (net.Interface, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(remote.Unmap(), 9)))
	if err != nil {
		return net.Interface{}, err
	}
	local := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	c.Close()
	ifs, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, err
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == local {
					return ifc, nil
				}
			}
		}
	}
	return net.Interface{}, errNoInterface
}
