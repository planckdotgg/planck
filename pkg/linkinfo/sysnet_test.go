package linkinfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// fakeIface writes one interface of a synthetic /sys/class/net.
func fakeIface(t *testing.T, root, name string, files map[string]string, dirs ...string) {
	t.Helper()
	base := filepath.Join(root, name)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, content := range files {
		if err := os.WriteFile(filepath.Join(base, f), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSysfsLink(t *testing.T) {
	root := t.TempDir()
	fakeIface(t, root, "eth0", map[string]string{"type": "1\n", "speed": "2500\n", "operstate": "up\n"}, "device")
	fakeIface(t, root, "enp3s0", map[string]string{"type": "1\n", "speed": "-1\n", "operstate": "up\n"}, "device")
	fakeIface(t, root, "enp4s0", map[string]string{"type": "1\n", "speed": "1000\n", "operstate": "down\n"}, "device")
	fakeIface(t, root, "wlan0", map[string]string{"type": "1\n", "speed": "-1\n", "operstate": "up\n"}, "device", "phy80211")
	fakeIface(t, root, "wlp2s0", map[string]string{"type": "1\n"}, "device", "wireless")
	fakeIface(t, root, "lo", map[string]string{"type": "772\n"})
	fakeIface(t, root, "tailscale0", map[string]string{"type": "65534\n", "speed": "-1\n"})
	fakeIface(t, root, "veth1a2b", map[string]string{"type": "1\n", "speed": "10000\n"})
	fakeIface(t, root, "br0", map[string]string{"type": "1\n"}, "bridge", "brif/veth1a2b", "brif/eth0")
	fakeIface(t, root, "brw", map[string]string{"type": "1\n"}, "bridge", "brif/wlan0")
	fakeIface(t, root, "brempty", map[string]string{"type": "1\n"}, "bridge", "brif")
	cases := map[string]defaults.Link{
		"eth0":       {Wired: true, SpeedMbps: 2500},
		"enp3s0":     {Wired: true},
		"enp4s0":     {Wired: true},
		"wlan0":      {},
		"wlp2s0":     {},
		"lo":         {},
		"tailscale0": {},
		"veth1a2b":   {},
		"br0":        {Wired: true, SpeedMbps: 2500},
		"brw":        {},
		"brempty":    {},
	}
	for name, want := range cases {
		got, err := sysfsLink(root, name)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v; want %+v", name, got, err, want)
		}
	}
	if _, err := sysfsLink(root, "gone0"); err == nil {
		t.Error("missing interface read without error")
	}
}

const procRouteFixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0100A8C0	0003	0	0	600	00000000	0	0	0
eth0	00000000	0100A8C0	0003	0	0	100	00000000	0	0	0
eth0	0000A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`

func TestProcRouteDefault(t *testing.T) {
	if got, err := procRouteDefault(procRouteFixture); err != nil || got != "eth0" {
		t.Errorf("default = %q, %v; want eth0, the lower metric", got, err)
	}
	down := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\neth0\t00000000\t0100A8C0\t0002\t0\t0\t0\t00000000\t0\t0\t0\n"
	if _, err := procRouteDefault(down); err != errNoDefaultRoute {
		t.Errorf("route that is not up: %v", err)
	}
	if _, err := procRouteDefault("Iface\tDestination\n"); err != errNoDefaultRoute {
		t.Errorf("header only: %v", err)
	}
}
