package linkinfo

import (
	"net"
	"net/netip"
	"os"

	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	sysClassNet = "/sys/class/net"
	procRoute   = "/proc/net/route"
)

func init() { routeFallback = defaultRouteInterface }

func interfaceLink(ifc net.Interface) (defaults.Link, error) {
	return sysfsLink(sysClassNet, ifc.Name)
}

// defaultRouteInterface is the interface of the IPv4 default route, for a
// peer the kernel would not pick a source address toward.
func defaultRouteInterface(netip.Addr) (net.Interface, error) {
	b, err := os.ReadFile(procRoute)
	if err != nil {
		return net.Interface{}, err
	}
	name, err := procRouteDefault(string(b))
	if err != nil {
		return net.Interface{}, err
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return net.Interface{}, err
	}
	return *ifc, nil
}
