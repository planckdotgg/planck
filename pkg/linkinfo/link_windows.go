package linkinfo

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// interfaceLink reads the adapter from GetAdaptersAddresses: IfType 6 is
// Ethernet, 71 is Wi-Fi.
func interfaceLink(ifc net.Interface) (defaults.Link, error) {
	size := uint32(16 << 10)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return defaults.Link{}, err
		}
	}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if int(a.IfIndex) != ifc.Index && int(a.Ipv6IfIndex) != ifc.Index {
			continue
		}
		speed := min(a.TransmitLinkSpeed, a.ReceiveLinkSpeed)
		if speed == ^uint64(0) {
			speed = 0
		}
		return defaults.Link{Wired: a.IfType == windows.IF_TYPE_ETHERNET_CSMACD, SpeedMbps: int(speed / 1e6)}, nil
	}
	return defaults.Link{}, errNoInterface
}
