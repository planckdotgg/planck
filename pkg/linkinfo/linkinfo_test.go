package linkinfo

import (
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestRouteLoopbackIsNotWired(t *testing.T) {
	name, link, err := Route(netip.MustParseAddr("127.0.0.1"))
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		if err == nil {
			t.Fatalf("unsupported OS returned %s %+v", name, link)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || link.Wired {
		t.Fatalf("loopback: %q %+v", name, link)
	}
}

// TestLiveInterfaces prints every up interface's link, and the route to
// each address in PLANCK_LIVE_LINK_PEERS (comma separated).
func TestLiveInterfaces(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_LINK_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_LINK_TEST=1")
	}
	ifs, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		link, err := interfaceLink(ifc)
		t.Logf("%-40s wired=%-5v speed=%5d Mbps err=%v", ifc.Name, link.Wired, link.SpeedMbps, err)
	}
	for _, peer := range strings.Split(os.Getenv("PLANCK_LIVE_LINK_PEERS"), ",") {
		if peer == "" {
			continue
		}
		name, link, err := Route(netip.MustParseAddr(peer))
		t.Logf("route to %s: %s wired=%v speed=%d Mbps err=%v", peer, name, link.Wired, link.SpeedMbps, err)
	}
}
