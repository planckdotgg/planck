package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/wire"
)

var bigboi = discovery.Beacon{HostID: "h-bigboi", Name: "bigboi", ControlPort: 47701, CertFingerprint: strings.Repeat("ab", 32), OS: "windows"}

// TestBackendMergesRoutes: a host heard on the LAN and the tailnet, and
// pinned earlier by its LAN address, is one card with both routes; its
// pin learns both routes and its advertised name.
func TestBackendMergesRoutes(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		if err := c.Pin(config.PinnedHost{HostID: "addr:192.168.188.126:47701", CertFingerprint: bigboi.CertFingerprint, LastAddr: "192.168.188.126:47701"}); err != nil {
			return err
		}
		return c.Pin(config.PinnedHost{HostID: bigboi.HostID, Name: "old name", CertFingerprint: bigboi.CertFingerprint})
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, dir, nil)
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:47700"), Kind: discovery.PathLANDirect, RTT: time.Millisecond})
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("100.104.23.37:47700"), Kind: discovery.PathTailscaleDirect, RTT: 3 * time.Millisecond})

	hosts := b.Hosts()
	if len(hosts) != 1 {
		t.Fatalf("%d cards, want 1: %+v", len(hosts), hosts)
	}
	h := hosts[0]
	if h.ID != bigboi.HostID || h.Name != "bigboi" || !h.Paired || !h.Online || h.PathKind != discovery.PathLANDirect ||
		h.Address != "192.168.188.126:47701" || h.LANAddress != "192.168.188.126:47701" || h.TailscaleAddress != "100.104.23.37:47701" ||
		!h.LANReachable || !h.TailscaleReachable {
		t.Fatalf("card: %+v", h)
	}

	waitUntil(t, "the pin to learn the routes", func() bool {
		stored, _ := config.LoadClientConfig(dir)
		p := stored.PinnedHosts[bigboi.HostID]
		return p.Name == "bigboi" && p.LastLANAddr == "192.168.188.126:47701" && p.LastTailscaleAddr == "100.104.23.37:47701"
	})

	tgt, name := b.resolveTarget(context.Background(), bigboi.HostID)
	if name != "bigboi" || tgt.HostID != bigboi.HostID || len(tgt.Candidates) != 2 ||
		!slices.ContainsFunc(tgt.Candidates, func(c discovery.Candidate) bool { return c.Kind == discovery.PathLANDirect }) ||
		!slices.ContainsFunc(tgt.Candidates, func(c discovery.Candidate) bool { return c.Kind == discovery.PathTailscaleDirect }) {
		t.Fatalf("target %+v (%q), want both routes", tgt, name)
	}
}

// TestBackendPinnedRoutesAreCandidates: a paired host not found now is
// dialed on both remembered routes, on the finder's discovery port.
func TestBackendPinnedRoutesAreCandidates(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		return c.Pin(config.PinnedHost{HostID: bigboi.HostID, Name: "bigboi", CertFingerprint: bigboi.CertFingerprint,
			LastAddr: "100.104.23.37:41000", LastLANAddr: "192.168.188.126:41000", LastTailscaleAddr: "100.104.23.37:41000"})
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, dir, nil)
	b.finder.port = 41100
	tgt, _ := b.resolveTarget(context.Background(), bigboi.HostID)
	want := []discovery.Candidate{
		{HostID: bigboi.HostID, Kind: discovery.PathLANDirect, Addr: netip.MustParseAddrPort("192.168.188.126:41100")},
		{HostID: bigboi.HostID, Kind: discovery.PathTailscaleDirect, Addr: netip.MustParseAddrPort("100.104.23.37:41100")},
	}
	if !slices.Equal(tgt.Candidates, want) || tgt.Addr != "100.104.23.37:41000" || tgt.Fingerprint != bigboi.CertFingerprint {
		t.Fatalf("target %+v", tgt)
	}
	hosts := b.Hosts()
	if len(hosts) != 1 || hosts[0].Online || hosts[0].LANAddress != "192.168.188.126:41000" || hosts[0].TailscaleAddress != "100.104.23.37:41000" {
		t.Fatalf("offline card: %+v", hosts)
	}
}

// TestBackendHostOrder: connected, then online, then by name ignoring
// case, then by ID, the same every call.
func TestBackendHostOrder(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		for _, p := range []config.PinnedHost{
			{HostID: "h-zeta", Name: "zeta"}, {HostID: "h-alpha2", Name: "Alpha"}, {HostID: "h-alpha1", Name: "alpha"},
			{HostID: "h-cabin", Name: "cabin"},
		} {
			p.CertFingerprint = strings.Repeat(p.HostID[len(p.HostID)-1:], 64)
			if err := c.Pin(p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, dir, nil)
	for _, name := range []string{"Studio", "bigboi"} {
		beacon := discovery.Beacon{HostID: "h-" + strings.ToLower(name), Name: name, ControlPort: 47701, CertFingerprint: strings.Repeat("f", 63) + name[:1]}
		b.finder.add(discovery.Announcement{Beacon: beacon, Addr: netip.AddrPortFrom(netip.MustParseAddr("192.168.1.2"), uint16(len(name))), Kind: discovery.PathLANDirect})
	}
	b.mu.Lock()
	b.connected = "h-cabin"
	b.mu.Unlock()

	want := []string{"h-cabin", "h-bigboi", "h-studio", "h-alpha1", "h-alpha2", "h-zeta"}
	for range 50 {
		var got []string
		for _, h := range b.Hosts() {
			got = append(got, h.ID)
			if h.Connected != (h.ID == "h-cabin") {
				t.Fatalf("%s connected=%v", h.ID, h.Connected)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

// TestBackendRenameHost: an alias names the card and survives a restart;
// clearing it goes back to the host's own name.
func TestBackendRenameHost(t *testing.T) {
	dir := t.TempDir()
	b, changes := newTestBackend(t, dir, nil)
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:47700"), Kind: discovery.PathLANDirect})
	before := changes.Load()
	if err := b.RenameHost(bigboi.HostID, "  Gaming Rig "); err != nil {
		t.Fatal(err)
	}
	if changes.Load() == before {
		t.Fatal("rename did not ask for a redraw")
	}
	if h := b.Hosts()[0]; h.Name != "Gaming Rig" || h.Alias != "Gaming Rig" || h.AdvertisedName != "bigboi" {
		t.Fatalf("after the rename: %+v", h)
	}

	again, _ := newTestBackend(t, dir, nil)
	again.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:47700"), Kind: discovery.PathLANDirect})
	if h := again.Hosts()[0]; h.Name != "Gaming Rig" {
		t.Fatalf("after a restart: %+v", h)
	}
	if err := again.RenameHost(bigboi.HostID, " "); err != nil {
		t.Fatal(err)
	}
	if h := again.Hosts()[0]; h.Name != "bigboi" || h.Alias != "" {
		t.Fatalf("after clearing: %+v", h)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.HostAliases[bigboi.HostID]; ok {
		t.Fatalf("alias still stored: %v", stored.HostAliases)
	}
}

type fixedTailnet struct{ st *tailscale.Status }

func (f fixedTailnet) Status(context.Context) (*tailscale.Status, error) { return f.st, nil }

// TestFinderTailnetPort: tailnet peers are probed on the finder's port,
// with no LAN listener.
func TestFinderTailnetPort(t *testing.T) {
	if p := newFinder(":41100", nil, nil, t.Logf).port; p != 41100 {
		t.Fatalf("port from the listen address: %d", p)
	}
	if p := newFinder("", nil, nil, t.Logf).port; p != discovery.DefaultPort {
		t.Fatalf("default port: %d", p)
	}
	h := startFakeHost(t, "127.0.0.1:0")
	st := &tailscale.Status{BackendState: "Running", Self: tailscale.Node{UserID: 1}, Peers: []tailscale.Node{
		{HostName: "host", UserID: 1, Online: true, Direct: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
	}}
	f := newFinder("", fixedTailnet{st}, nil, t.Logf)
	f.port = uint16(h.DiscoveryAddr().(*net.UDPAddr).Port)
	f.probeAll(context.Background())
	v, ok := f.lookup(h.HostID())
	if !ok || v.Best.Kind != discovery.PathTailscaleDirect || !v.SameUser {
		t.Fatalf("found %+v (%v), want the host on the tailnet", v, ok)
	}
}

// TestBackendHostNameSetting: Settings.HostName is host.json's name; a
// new one is kept there, and a running host takes it at once.
func TestBackendHostNameSetting(t *testing.T) {
	dir := t.TempDir()
	f := newFinder("", nil, nil, t.Logf)
	b, err := newBackend(backendConfig{
		dir: dir, name: "test-client", finder: f, tailscale: noTailnet{}, logf: t.Logf,
		hostConfig: func(s ui.Settings) session.HostConfig {
			return session.HostConfig{
				ConfigDir: dir, ListenAddr: "127.0.0.1:0", DiscoveryAddr: "127.0.0.1:0", FPS: 60, NoTailscale: true, NoAudio: true,
				OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
				OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil }, Logf: t.Logf,
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	if got := b.Settings().HostName; got != config.Hostname() {
		t.Fatalf("HostName %q, want the machine's %q", got, config.Hostname())
	}
	s := b.Settings()
	s.HostName = " Den PC "
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if hc, _ := config.LoadHostConfig(dir); hc.Name != "Den PC" || b.Settings().HostName != "Den PC" {
		t.Fatalf("host.json name %q, setting %q", hc.Name, b.Settings().HostName)
	}

	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	h := b.host
	b.mu.Unlock()
	if h.Name() != "Den PC" {
		t.Fatalf("hosting as %q", h.Name())
	}
	s.HostName = "Attic PC"
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	beacon, _, err := discovery.Probe(ctx, h.DiscoveryAddr().(*net.UDPAddr).AddrPort())
	if err != nil || beacon.Name != "Attic PC" {
		t.Fatalf("running host answers %q, %v", beacon.Name, err)
	}
	if hc, _ := config.LoadHostConfig(dir); hc.Name != "Attic PC" {
		t.Fatalf("host.json name %q", hc.Name)
	}
}

// TestBackendTelemetryFollowsRoute: the HUD's telemetry shows the route
// and its datagram size, and flips when the stream moves to the LAN.
func TestBackendTelemetryFollowsRoute(t *testing.T) {
	h := startFakeHost(t, "127.0.0.1:0")
	lanConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lanConn.Close()
	b, _ := newTestBackend(t, t.TempDir(), nil)
	b.routeConfig = session.RouteConfig{ProbeInterval: 100 * time.Millisecond}
	beacon := discovery.Beacon{HostID: h.HostID(), Name: h.Name(), ControlPort: uint16(h.Addr().(*net.UDPAddr).Port), CertFingerprint: h.Fingerprint()}
	b.finder.add(discovery.Announcement{Beacon: beacon, Addr: lanConn.LocalAddr().(*net.UDPAddr).AddrPort(), Kind: discovery.PathLANDirect})
	b.finder.add(discovery.Announcement{Beacon: beacon, Addr: h.DiscoveryAddr().(*net.UDPAddr).AddrPort(), Kind: discovery.PathTailscaleDirect})

	ctx := context.Background()
	if err := b.Connect(ctx, h.HostID()); err != nil && !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatal(err)
	}
	if err := b.SubmitPIN(ctx, "246810"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stream on the tailnet", func() bool {
		tel := b.Telemetry()
		return tel.Connected && tel.PathKind == discovery.PathTailscaleDirect && tel.DatagramBytes == wire.MaxDatagram
	})
	if hs := b.Hosts(); len(hs) != 1 || !hs[0].Connected {
		t.Fatalf("hosts while streaming: %+v", hs)
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go discovery.ServeFunc(sctx, lanConn, func() discovery.Beacon { return beacon })
	waitUntil(t, "the stream on the LAN", func() bool {
		tel := b.Telemetry()
		return tel.Connected && tel.PathKind == discovery.PathLANDirect && tel.DatagramBytes == wire.MaxLANDatagram
	})
}

// TestBackendConnectToGoneHost: a host discovery found, gone before the
// connect, fails it. The UI's connect has no deadline of its own, so a race
// that waited for an answer forever kept every later Connect from starting.
func TestBackendConnectToGoneHost(t *testing.T) {
	h := startFakeHost(t, "127.0.0.1:0")
	disc := h.DiscoveryAddr().(*net.UDPAddr).AddrPort()
	b, _ := newTestBackend(t, t.TempDir(), []netip.AddrPort{disc})
	waitUntil(t, "the host to be found", func() bool { return len(b.Hosts()) == 1 })
	id := b.Hosts()[0].ID
	h.Stop()
	if !b.Hosts()[0].Online {
		t.Fatal("the host went offline at once; the test needs it still listed online")
	}
	done := make(chan error, 1)
	go func() { done <- b.Connect(context.Background(), id) }()
	select {
	case err := <-done:
		if !errors.Is(err, session.ErrNoAnswer) {
			t.Fatalf("connect: %v, want session.ErrNoAnswer", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("connect to a host that went away never returned")
	}
}
