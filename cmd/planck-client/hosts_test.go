package main

import (
	"context"
	"crypto/tls"
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
	"github.com/planckdotgg/planck/pkg/defaults"
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
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect, RTT: time.Millisecond})
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("100.104.23.37:41000"), Kind: discovery.PathTailscaleDirect, RTT: 3 * time.Millisecond})

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
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
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
	again.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
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

// TestBackendHostsLeaveOutSelf: hosting from the client's own config dir
// is heard by its discovery but never listed, on this run and the next;
// another host not paired leaves the list once it is gone.
func TestBackendHostsLeaveOutSelf(t *testing.T) {
	dir := t.TempDir()
	port := freeUDPPort(t)
	disc := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))
	newClient := func() *backend {
		f := newFinder("", nil, []netip.AddrPort{disc}, t.Logf)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go f.run(ctx)
		b, err := newBackend(backendConfig{
			dir: dir, name: "test-client", finder: f, tailscale: noTailnet{}, logf: t.Logf,
			hostConfig: func(s ui.Settings) session.HostConfig {
				return session.HostConfig{
					ConfigDir: dir, ListenAddr: "127.0.0.1:0", DiscoveryAddr: disc.String(), FPS: 60, NoTailscale: true, NoAudio: true,
					OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
					OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil }, Logf: t.Logf,
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.close)
		return b
	}
	b := newClient()
	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	selfID := b.host.HostID()
	b.mu.Unlock()
	b.RefreshHosts()
	waitUntil(t, "discovery to hear this machine's host", func() bool { _, ok := b.finder.lookup(selfID); return ok })
	if hs := b.Hosts(); len(hs) != 0 {
		t.Fatalf("hosts while hosting: %+v, want this machine left out", hs)
	}
	if err := b.SetHosting(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if hs := b.Hosts(); len(hs) != 0 {
		t.Fatalf("hosts after hosting stopped: %+v", hs)
	}

	next := newClient()
	next.finder.add(discovery.Announcement{Beacon: discovery.Beacon{HostID: selfID, Name: "me", CertFingerprint: strings.ToUpper(b.selfFingerprint(t))},
		Addr: disc, Kind: discovery.PathLANDirect})
	next.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
	if hs := next.Hosts(); len(hs) != 1 || hs[0].ID != bigboi.HostID || hs[0].Paired {
		t.Fatalf("next run: %+v, want bigboi alone, not paired", hs)
	}
	next.finder.mu.Lock()
	next.finder.hosts[bigboi.HostID].seen = time.Now().Add(-onlineFor)
	next.finder.mu.Unlock()
	if hs := next.Hosts(); len(hs) != 0 {
		t.Fatalf("an unpaired host gone: %+v, want it left out", hs)
	}
}

func (b *backend) selfFingerprint(t *testing.T) string {
	t.Helper()
	hc, err := config.LoadHostConfig(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(hc.CertPath, hc.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	return config.CertFingerprint(cert.Certificate[0])
}

// TestBackendRemoveHost: removing a paired host drops its pins by ID and
// by address, its alias and its overrides from client.json and its card at
// once; one found by discovery only goes until it is heard again.
func TestBackendRemoveHost(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		for _, p := range []config.PinnedHost{
			{HostID: "addr:192.168.188.126:47701", CertFingerprint: bigboi.CertFingerprint, LastAddr: "192.168.188.126:47701"},
			{HostID: bigboi.HostID, Name: "bigboi", CertFingerprint: bigboi.CertFingerprint, LastAddr: "192.168.188.126:47701"},
			{HostID: "h-cabin", Name: "cabin", CertFingerprint: strings.Repeat("cd", 32), LastAddr: "10.0.0.9:47701"},
		} {
			if err := c.Pin(p); err != nil {
				return err
			}
		}
		c.HostAliases = map[string]string{bigboi.HostID: "Rig"}
		c.HostOverrides = map[string]defaults.UserOverrides{bigboi.HostID: {FPS: "60"}, "h-cabin": {FPS: "120"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, changes := newTestBackend(t, dir, nil)
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
	if hs := b.Hosts(); len(hs) != 2 {
		t.Fatalf("before: %+v", hs)
	}
	before := changes.Load()
	if err := b.RemoveHost(bigboi.HostID); err != nil {
		t.Fatal(err)
	}
	if changes.Load() == before {
		t.Fatal("remove did not ask for a redraw")
	}
	if hs := b.Hosts(); len(hs) != 1 || hs[0].ID != "h-cabin" {
		t.Fatalf("after: %+v, want cabin alone", hs)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.PinnedHosts) != 1 || stored.HostAliases[bigboi.HostID] != "" || len(stored.HostOverrides) != 1 {
		t.Fatalf("stored pins %v aliases %v overrides %v", stored.PinnedHosts, stored.HostAliases, stored.HostOverrides)
	}
	if o := b.Overrides(bigboi.HostID); o != (defaults.UserOverrides{}) {
		t.Fatalf("overrides kept in memory: %+v", o)
	}

	if err := b.RemoveHost("h-cabin"); err != nil {
		t.Fatal(err)
	}
	if hs := b.Hosts(); len(hs) != 0 {
		t.Fatalf("an offline paired host after its removal: %+v", hs)
	}
	b.finder.add(discovery.Announcement{Beacon: bigboi, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
	if hs := b.Hosts(); len(hs) != 1 || hs[0].Paired {
		t.Fatalf("bigboi heard again: %+v, want it back, not paired", hs)
	}

	b.mu.Lock()
	b.connected = bigboi.HostID
	b.mu.Unlock()
	if err := b.RemoveHost(bigboi.HostID); err == nil {
		t.Fatal("removed the host streaming now")
	}
}

// TestBackendPinnedHostMovedPorts: with client.json as it was on the Mac,
// an address pin from before discovery found bigboi beside its pin by ID,
// both on the old control port, bigboi heard on a new control port shows
// as one card, online, at the port its beacon gives, and no offline card
// is left for the old address.
func TestBackendPinnedHostMovedPorts(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		for _, p := range []config.PinnedHost{
			{HostID: "addr:192.168.188.126:41001", CertFingerprint: bigboi.CertFingerprint, LastAddr: "192.168.188.126:41001"},
			{HostID: bigboi.HostID, Name: "bigboi", CertFingerprint: bigboi.CertFingerprint, LastAddr: "192.168.188.126:41001",
				LastLANAddr: "192.168.188.126:41001", LastTailscaleAddr: "100.104.23.37:41001"},
		} {
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
	if hs := b.Hosts(); len(hs) != 1 || hs[0].Online {
		t.Fatalf("not heard yet: %+v, want one offline card", hs)
	}
	moved := bigboi
	moved.ControlPort = 41005
	b.finder.add(discovery.Announcement{Beacon: moved, Addr: netip.MustParseAddrPort("192.168.188.126:41000"), Kind: discovery.PathLANDirect})
	hs := b.Hosts()
	if len(hs) != 1 || hs[0].ID != bigboi.HostID || !hs[0].Online || hs[0].Address != "192.168.188.126:41005" {
		t.Fatalf("heard on the new port: %+v, want bigboi alone, online at 192.168.188.126:41005", hs)
	}
}

// TestIdentifyKeepsTypedPort: an address typed with a port dials that
// port. The host answering discovery on the IP names its own control port;
// when another host on the same machine answers, as bigboi's installed host
// on the default discovery port did for a test host on 127.0.0.1, the
// typed port stays and the answer is not taken for its identity. With no
// port typed, the answering host's control port is used.
func TestIdentifyKeepsTypedPort(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go discovery.Serve(ctx, conn, discovery.Beacon{HostID: "h-other", Name: "other", ControlPort: 41001, CertFingerprint: strings.Repeat("cd", 32)})
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)

	if tgt, _ := identify(ctx, "127.0.0.1:5555", port); tgt.Addr != "127.0.0.1:5555" || tgt.HostID != "" || tgt.Fingerprint != "" {
		t.Errorf("typed 127.0.0.1:5555: %+v, want that address and no identity of the other host", tgt)
	}
	if tgt, name := identify(ctx, "127.0.0.1:41001", port); tgt.Addr != "127.0.0.1:41001" || tgt.HostID != "h-other" || name != "other" {
		t.Errorf("typed the answering host's port: %+v %q, want it identified", tgt, name)
	}
	if tgt, _ := identify(ctx, "127.0.0.1", port); tgt.Addr != "127.0.0.1:41001" || tgt.HostID != "h-other" {
		t.Errorf("no port typed: %+v, want the answering host's control port", tgt)
	}
}
