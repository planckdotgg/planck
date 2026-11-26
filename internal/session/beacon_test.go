package session

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/discovery"
)

// TestBeaconAddrsIncludeDefaultPort: a host whose discovery port is not
// the default also broadcasts its beacons to the default port, so a client
// left on it lists the host online instead of offline, as bigboi was when
// its host ran on 41000 and the client listened on 47700.
func TestBeaconAddrsIncludeDefaultPort(t *testing.T) {
	ports := func(port uint16) []uint16 {
		var out []uint16
		for _, ap := range beaconAddrs(port) {
			out = append(out, ap.Port())
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	if len(discovery.BroadcastAddrs(discovery.DefaultPort)) == 0 {
		t.Skip("no broadcast interface")
	}
	if got := ports(45000); !slices.Equal(got, []uint16{discovery.DefaultPort, 45000}) {
		t.Errorf("port 45000 beacons to ports %v, want %d and 45000", got, discovery.DefaultPort)
	}
	if got := ports(discovery.DefaultPort); !slices.Equal(got, []uint16{discovery.DefaultPort}) {
		t.Errorf("default port beacons to ports %v", got)
	}
}
