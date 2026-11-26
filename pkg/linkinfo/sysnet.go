package linkinfo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	arphrdEther    = 1
	maxBridgeDepth = 2
)

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	return strings.TrimSpace(string(b)), err
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// sysfsLink reads an interface's link from a /sys/class/net directory:
// Ethernet-type interfaces backed by a device are wired, 802.11 ones
// (phy80211 or wireless) are not, and a bridge is wired when one of its
// members is. The speed is what the driver reports in Mbit/s; a link
// without one (Wi-Fi, a virtual interface, a cable that is down) reports 0.
func sysfsLink(root, name string) (defaults.Link, error) {
	return sysfsLinkDepth(root, name, 0)
}

func sysfsLinkDepth(root, name string, depth int) (defaults.Link, error) {
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil {
		return defaults.Link{}, err
	}
	if exists(filepath.Join(dir, "phy80211")) || exists(filepath.Join(dir, "wireless")) {
		return defaults.Link{}, nil
	}
	if members, err := os.ReadDir(filepath.Join(dir, "brif")); err == nil && depth < maxBridgeDepth {
		var best defaults.Link
		for _, m := range members {
			l, err := sysfsLinkDepth(root, m.Name(), depth+1)
			if err == nil && l.Wired && (!best.Wired || l.SpeedMbps > best.SpeedMbps) {
				best = l
			}
		}
		return best, nil
	}
	typ, err := readTrim(filepath.Join(dir, "type"))
	if err != nil {
		return defaults.Link{}, err
	}
	if n, err := strconv.Atoi(typ); err != nil || n != arphrdEther {
		return defaults.Link{}, nil
	}
	if !exists(filepath.Join(dir, "device")) {
		return defaults.Link{}, nil
	}
	link := defaults.Link{Wired: true}
	if state, _ := readTrim(filepath.Join(dir, "operstate")); state == "down" {
		return link, nil
	}
	if s, err := readTrim(filepath.Join(dir, "speed")); err == nil {
		if mbps, err := strconv.Atoi(s); err == nil && mbps > 0 {
			link.SpeedMbps = mbps
		}
	}
	return link, nil
}

var errNoDefaultRoute = errors.New("linkinfo: no IPv4 default route")

// procRouteDefault is the interface of the default route in the text of
// /proc/net/route: the live route with destination and mask 0, and the
// lowest metric among them.
func procRouteDefault(route string) (string, error) {
	const flagUp = 1
	best, bestMetric := "", -1
	for i, line := range strings.Split(route, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 8 {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&flagUp == 0 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		metric, err := strconv.Atoi(f[6])
		if err != nil {
			return "", fmt.Errorf("linkinfo: bad route metric %q", f[6])
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	if best == "" {
		return "", errNoDefaultRoute
	}
	return best, nil
}
