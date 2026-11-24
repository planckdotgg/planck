package linkinfo

import (
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// ifmediareq from <net/if.h>; the kernel packs it to 4 bytes, so
// ifm_ulist sits at offset 36 and the struct is 44 bytes (SIOCGIFMEDIA).
type ifmediareq struct {
	name    [unix.IFNAMSIZ]byte
	current int32
	mask    int32
	status  int32
	active  int32
	count   int32
	ulist   [2]uint32
}

// <net/if_media.h>
const (
	ifmTypeMask  = 0x000000e0
	ifmEther     = 0x00000020
	ifmIEEE80211 = 0x00000080
)

func interfaceLink(ifc net.Interface) (defaults.Link, error) {
	wired, err := mediaWired(ifc.Name)
	if err != nil {
		return defaults.Link{}, err
	}
	bps, err := baudrate(ifc.Index)
	if err != nil {
		return defaults.Link{}, err
	}
	return defaults.Link{Wired: wired, SpeedMbps: int(bps / 1e6)}, nil
}

// mediaWired reports IFM_ETHER media as wired. Interfaces without media
// (utun, bridges) answer EINVAL and count as not wired.
func mediaWired(name string) (bool, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	var req ifmediareq
	copy(req.name[:len(req.name)-1], name)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), unix.SIOCGIFMEDIA, uintptr(unsafe.Pointer(&req))); errno != 0 {
		if errno == syscall.EINVAL || errno == syscall.EOPNOTSUPP {
			return false, nil
		}
		return false, errno
	}
	media := req.active
	if media == 0 {
		media = req.current
	}
	return media&ifmTypeMask == ifmEther, nil
}

// baudrate reads if_data64.ifi_baudrate from the NET_RT_IFLIST2 sysctl.
func baudrate(index int) (uint64, error) {
	rib, err := syscall.RouteRIB(syscall.NET_RT_IFLIST2, index)
	if err != nil {
		return 0, err
	}
	for len(rib) >= int(unsafe.Sizeof(unix.IfMsghdr2{})) {
		m := (*unix.IfMsghdr2)(unsafe.Pointer(&rib[0]))
		if m.Msglen == 0 || int(m.Msglen) > len(rib) {
			break
		}
		if m.Type == unix.RTM_IFINFO2 && int(m.Index) == index {
			return m.Data.Baudrate, nil
		}
		rib = rib[m.Msglen:]
	}
	return 0, errNoInterface
}
