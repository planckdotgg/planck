package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/tailscale"
)

const testFP = "7b4d00112233445566778899aabbccddeeff00112233445566778899aabbc219"

func TestParseQuickConnect(t *testing.T) {
	for _, c := range []struct {
		in   string
		kind quickKind
		text string
	}{
		{"192.168.188.41", quickAddress, "192.168.188.41"},
		{" 192.168.188.41:41000 ", quickAddress, "192.168.188.41:41000"},
		{"fe80::1", quickAddress, "fe80::1"},
		{"[fd7a:115c:a1e0::1]:47701", quickAddress, "[fd7a:115c:a1e0::1]:47701"},
		{"shop-rig.local:47701", quickAddress, "shop-rig.local:47701"},
		{"bigboi", quickName, "bigboi"},
		{"bigboi.tail1234.ts.net.", quickName, "bigboi.tail1234.ts.net"},
		{strings.ToUpper(testFP), quickFingerprint, testFP},
		{"7B4D 0011 2233 4455 6677 8899 AABB CCDD EEFF 0011 2233 4455 6677 8899 AABB C219", quickFingerprint, testFP},
		{"planck.gg/j/abc-123", quickShareLink, "abc-123"},
		{"https://WWW.Planck.gg/j/XyZ9/", quickShareLink, "XyZ9"},
	} {
		got, err := parseQuickConnect(c.in)
		if err != nil || got.kind != c.kind || got.text != c.text {
			t.Errorf("parseQuickConnect(%q) = %+v, %v; want kind %d %q", c.in, got, err, c.kind, c.text)
		}
	}
	for _, bad := range []string{"", "   ", "planck.gg/j/", "planck.gg/j/a b", "bigboi:0", "bigboi:99999", "two words", "user@host"} {
		if got, err := parseQuickConnect(bad); err == nil {
			t.Errorf("parseQuickConnect(%q) = %+v, want an error", bad, got)
		}
	}
}

// TestQuickConnectResolve finds a remembered host by its name, alias and
// fingerprint, a tailnet peer by MagicDNS, and refuses what names none.
func TestQuickConnectResolve(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		c.HostAliases = map[string]string{"h-cabin": "Cabin"}
		return c.Pin(config.PinnedHost{HostID: "h-cabin", Name: "cabin-pc", CertFingerprint: testFP, LastAddr: "192.168.188.7:47701"})
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, dir, nil)
	b.finder.status = &tailscale.Status{BackendState: "Running", Peers: []tailscale.Node{
		{HostName: "studio-mac", DNSName: "studio-mac.tail1234.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("fd7a:115c:a1e0::9"), netip.MustParseAddr("100.101.102.103")}},
	}}
	for in, want := range map[string]string{
		"h-cabin": "h-cabin", "cabin-pc": "h-cabin", "CABIN": "h-cabin", strings.ToUpper(testFP): "h-cabin",
		"studio-mac": "100.101.102.103:47701", "studio-mac.tail1234.ts.net": "100.101.102.103:47701",
		"10.0.0.5": "10.0.0.5", "shop-rig.local": "shop-rig.local",
	} {
		if got, err := b.quickConnect(in); err != nil || got != want {
			t.Errorf("quickConnect(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]error{
		"nobody": ui.ErrUnknownHost, strings.Repeat("ab", 32): ui.ErrUnknownHost, "planck.gg/j/abc": ui.ErrShareLink,
	} {
		if err := b.Connect(context.Background(), in); !errors.Is(err, want) {
			t.Errorf("Connect(%q) = %v, want %v", in, err, want)
		}
	}
}

func TestAppHostConfig(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateHostConfig(dir, func(c *config.HostConfig) error {
		c.ControlPort, c.MediaPort, c.DiscoveryPort = 41000, 41001, 0
		c.Display, c.Adapter, c.FixedPIN, c.NoInput, c.Clipboard = "2", "Radeon", "424242", true, config.ClipboardBoth
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := appHostConfig(dir, t.Logf)(ui.Settings{Encoder: "ignored"})
	if c.ConfigDir != dir || c.ListenAddr != ":41000" || c.MediaPort != 41001 || c.DiscoveryAddr != "off" {
		t.Errorf("ports: %+v", c)
	}
	if c.Display != "2" || c.Adapter != "Radeon" || c.PIN != "424242" || c.InjectInput || c.Clipboard != (session.ClipboardConfig{Send: true, Receive: true}) {
		t.Errorf("settings: %+v", c)
	}
}

// hostingFromAppConfig hosts with host.json's settings, as the app does,
// on a fake pipeline so nothing is captured or injected.
func hostingFromAppConfig(t *testing.T, hostDir string) func(ui.Settings) session.HostConfig {
	app := appHostConfig(hostDir, t.Logf)
	return func(s ui.Settings) session.HostConfig {
		c := app(s)
		c.ListenAddr, c.InjectInput, c.NoTailscale, c.NoAudio = "127.0.0.1:"+strings.TrimPrefix(c.ListenAddr, ":"), false, true, true
		c.OpenVideo = func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil }
		c.OpenAudio = func() (audio.Capturer, error) { return &toneCapturer{}, nil }
		c.Capabilities = func() defaults.HostCapabilities {
			a := gpu.Adapter{LUID: 7, Vendor: gpu.VendorNVIDIA, Name: "Test GPU"}
			return defaults.HostCapabilities{OS: "windows",
				Encoders: []defaults.EncoderCap{{Adapter: a, API: defaults.APINVENC, Engines: 1, DrivesDisplay: true,
					Codecs: []defaults.CodecCap{{Codec: defaults.CodecHEVC, TenBit: true}}}},
				Displays: []defaults.HostDisplay{{ID: "1", Width: 1920, Height: 1080, RefreshHz: 120, Adapter: a.LUID, Primary: true}}}
		}
		return c
	}
}

// TestBackendHostingPolicyStatsAndRevoke: the Hosting toggle serves
// host.json's policy, which a client sees in SessionStats as Set by host;
// the paired device lists as connected until revoked, which ends its
// stream and leaves LastSession.
func TestBackendHostingPolicyStatsAndRevoke(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateHostConfig(dir, func(c *config.HostConfig) error {
		c.ControlPort, c.MediaPort, c.DiscoveryPort, c.FixedPIN = 0, 0, 0, "135790"
		c.Policy = defaults.UserOverrides{FPS: "60", BitrateMbps: 30}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, t.TempDir(), nil)
	b.hostConfig = hostingFromAppConfig(t, dir)
	ctx := context.Background()
	if err := b.SetHosting(ctx, true); err != nil {
		t.Fatal(err)
	}
	st := b.Hosting()
	if !st.On || st.PIN != "135790" || st.Using.Control == 0 || st.Using.Discovery != 0 || len(st.Fingerprint) != 64 {
		t.Fatalf("hosting: %+v", st)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(st.Using.Control))
	if _, ok := b.LastSession(); ok {
		t.Fatal("a last session before any stream")
	}
	if err := b.Connect(ctx, addr); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("first connect: %v", err)
	}
	if p := b.Pairing(); p.Address != addr || p.Fingerprint != st.Fingerprint {
		t.Errorf("pairing metadata: %+v", p)
	}
	if err := b.SubmitPIN(ctx, "135790"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "live session stats", func() bool {
		s := b.SessionStats()
		return s.Live && len(s.FrameTimesMs) > 5 && s.Total.P50 > 0
	})
	s := b.SessionStats()
	if s.HostPolicy.FPS != "60" || s.HostPolicy.BitrateMbps != 30 || s.TargetFPS != 60 || s.EncoderGPU != "Test GPU" || s.Started.IsZero() {
		t.Errorf("session stats: policy %+v target %d gpu %q started %v", s.HostPolicy, s.TargetFPS, s.EncoderGPU, s.Started)
	}
	waitUntil(t, "the paired device connected", func() bool {
		pc := b.PairedClients()
		return len(pc) == 1 && pc[0].Connected
	})
	pc := b.PairedClients()[0]
	if pc.Name != "test-client" || pc.LastSeen.IsZero() || pc.TrustedViaTailnet || b.Hosting().Client != "test-client" {
		t.Errorf("paired device: %+v, hosting client %q", pc, b.Hosting().Client)
	}
	if err := b.RevokeClient(strings.Repeat("0", 64)); err == nil {
		t.Error("revoked a device never paired")
	}
	if err := b.RevokeClient(pc.Fingerprint); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the revoked stream to end", func() bool { return !b.SessionStats().Live })
	last, ok := b.LastSession()
	if !ok || last.Live || last.Ended.Before(last.Started) || last.HostPolicy.FPS != "60" {
		t.Fatalf("last session: %v %+v", ok, last)
	}
	if left := b.PairedClients(); len(left) != 0 {
		t.Fatalf("after revoke: %+v", left)
	}
}

func TestBackendHostSettings(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	s := b.HostSettings()
	if !s.RequirePINForLAN || !s.AllowTailscaleSameUser || !s.AllowInput || s.Clipboard != ui.ClipboardOff ||
		s.Ports != (ui.HostPorts{Control: 47701, Media: 47702, Discovery: 47700}) {
		t.Fatalf("defaults: %+v", s)
	}
	s.Name, s.Encoder, s.Display, s.VDDPrimary, s.FixedPIN = " studio ", "Test GPU", "2", true, "000111"
	s.AllowTailscaleSameUser, s.AllowInput, s.Clipboard = false, false, ui.ClipboardHostToClient
	s.Policy = defaults.UserOverrides{Codec: "hevc", FPS: "120"}
	s.Ports = ui.HostPorts{Control: 41000, Media: 0, Discovery: 0}
	if err := b.SetHostSettings(s); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "studio" || stored.Adapter != "Test GPU" || stored.Display != "2" || !stored.VDDPrimary || stored.FixedPIN != "000111" ||
		stored.AllowTailscaleSameUser || !stored.NoInput || stored.Clipboard != config.ClipboardHostToClient ||
		stored.Policy.Codec != "hevc" || stored.Policy.FPS != "120" || stored.ControlPort != 41000 || stored.MediaPort != 0 || stored.DiscoveryPort != 0 {
		t.Fatalf("host.json: %+v", stored)
	}
	if got := b.HostSettings(); got.Name != "studio" || got.Clipboard != ui.ClipboardHostToClient || b.Settings().HostName != "studio" || b.Settings().Encoder != "Test GPU" {
		t.Errorf("read back: %+v, settings %+v", got, b.Settings())
	}
	for _, bad := range []func(*ui.HostSettings){
		func(s *ui.HostSettings) { s.FixedPIN = "12" },
		func(s *ui.HostSettings) { s.Ports.Control = 70000 },
		func(s *ui.HostSettings) { s.Ports.Control, s.Ports.Media = 41000, 41000 },
		func(s *ui.HostSettings) { s.Policy.FPS = "75" },
		func(s *ui.HostSettings) { s.Clipboard = "sideways" },
		func(s *ui.HostSettings) { s.Name = strings.Repeat("n", session.MaxHostNameLen+1) },
	} {
		s := b.HostSettings()
		bad(&s)
		if err := b.SetHostSettings(s); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	if got := b.HostSettings(); got.FixedPIN != "000111" {
		t.Errorf("a refused change was kept: %+v", got)
	}
}

// TestBackendHostSettingsRestartHosting: while hosting, a new name goes to
// the running host, and any other change restarts it.
func TestBackendHostSettingsRestartHosting(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir(), nil)
	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	host := func() *session.Host { b.mu.Lock(); defer b.mu.Unlock(); return b.host }
	first := host()
	s := b.HostSettings()
	s.Name = "renamed"
	if err := b.SetHostSettings(s); err != nil {
		t.Fatal(err)
	}
	if host() != first || first.Name() != "renamed" {
		t.Fatalf("rename restarted hosting or missed the host: %q", first.Name())
	}
	s.FixedPIN = "999000"
	if err := b.SetHostSettings(s); err != nil {
		t.Fatal(err)
	}
	if h := host(); h == first || h == nil || !b.Hosting().On {
		t.Fatalf("a new PIN policy did not restart hosting: %+v", b.Hosting())
	}
}

func TestBackendTestPorts(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir(), nil)
	taken, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	busy := taken.LocalAddr().(*net.UDPAddr).Port
	free, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()
	st := b.TestPorts(ui.HostPorts{Control: busy, Media: freePort, Discovery: 0})
	if st.Control == "" || st.Media != "" || st.Discovery != "" {
		t.Errorf("busy control, free media: %+v", st)
	}
	if st := b.TestPorts(ui.HostPorts{Control: freePort, Media: freePort}); st.Media == "" {
		t.Errorf("control and media on one port: %+v", st)
	}
}

func TestBackendClientName(t *testing.T) {
	dir := t.TempDir()
	if err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error { c.DeviceName = "couch"; return nil }); err != nil {
		t.Fatal(err)
	}
	b, err := newBackend(backendConfig{dir: dir, finder: newFinder("", nil, nil, t.Logf), logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.ClientName(); got != "couch" {
		t.Fatalf("stored name: %q", got)
	}
	if got := deviceName(dir, ""); got != "couch" {
		t.Errorf("headless name %q", got)
	}
	if got := deviceName(dir, "flagged"); got != "flagged" {
		t.Errorf("-name: %q", got)
	}
	if err := b.SetClientName("  den  "); err != nil {
		t.Fatal(err)
	}
	if c, _ := config.LoadClientConfig(dir); c.DeviceName != "den" || b.ClientName() != "den" {
		t.Fatalf("after rename: stored %q, live %q", c.DeviceName, b.ClientName())
	}
	if err := b.SetClientName(""); err != nil || b.ClientName() != config.Hostname() {
		t.Fatalf("cleared: %q, %v", b.ClientName(), err)
	}
}

func TestBackendMeasureEncoder(t *testing.T) {
	b, err := newBackend(backendConfig{dir: t.TempDir(), finder: newFinder("", nil, nil, t.Logf), logf: t.Logf,
		encoders: []ui.EncoderInfo{{GPU: "Test GPU", API: "Fake"}},
		measure: func(ctx context.Context, gpu string) (session.Latency, error) {
			return session.Latency{P50: 1.41, P99: 1.9, Samples: 120}, nil
		},
		displays: func() []defaults.HostDisplay {
			return []defaults.HostDisplay{{ID: "1", Width: 3024, Height: 1964, RefreshHz: 120, Primary: true}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.MeasureEncoder(context.Background(), "Other GPU"); err == nil {
		t.Error("measured an encoder that is not listed")
	}
	ms, err := b.MeasureEncoder(context.Background(), "Test GPU")
	if err != nil || ms != 1.41 || b.Encoders()[0].EncodeMs != 1.41 {
		t.Fatalf("measured %v, %v; encoders %+v", ms, err, b.Encoders())
	}
	if d := b.Displays(); len(d) != 1 || d[0] != (ui.DisplayInfo{ID: "1", Width: 3024, Height: 1964, RefreshHz: 120, Primary: true}) {
		t.Errorf("displays %+v", d)
	}
}

func TestSessionStatsFromClient(t *testing.T) {
	started := time.Unix(1_800_000_000, 0)
	s := sessionStats(session.ClientStats{
		Connected: true, HostName: "bigboi", HostID: "h1", Path: "lan-direct", Codec: "hevc", Chroma: "4:2:0",
		Encode: session.Latency{P50: 3, P99: 5}, HostEncodeMs: 1.4, CaptureMs: 0.6, Network: session.Latency{P50: 0.4, P99: 0.9},
		E2E: session.Latency{P50: 8, P99: 11}, E2EGlass: session.Latency{P50: 8.4, P99: 11.2, Samples: 10}, Glass: session.Latency{P50: 2.5, P99: 3},
		Kbps: 84_200, MaxBitrateKbps: 150_000, FrameTimesMs: []float64{8.3, 8.4}, SlicesRecovered: 3, FramesLost: 1,
		Profile: &defaults.ResolvedProfile{FPS: 120, BitDepth: 10, Chroma444: true, EncoderAPI: defaults.APINVENC,
			EncodeAdapter: gpu.Adapter{Name: "RTX 4080"}},
	}, started)
	if !s.Live || s.Started != started || s.Capture.P50 != 0.6 || s.Encode != (ui.StageStat{P50: 1.4}) || s.Total != (ui.StageStat{P50: 8.4, P99: 11.2}) ||
		s.Present.P50 != 2.5 || s.BitrateMbps != 84.2 || s.CeilingMbps != 150 || s.FECRepaired != 3 || s.FramesLost != 1 {
		t.Errorf("figures: %+v", s)
	}
	if s.Chroma != encode.YUV444P10 || s.BitDepth != 10 || s.TargetFPS != 120 || s.EncoderGPU != "RTX 4080" || s.PathKind.String() != "lan-direct" {
		t.Errorf("format: chroma %v depth %d fps %d gpu %q path %v", s.Chroma, s.BitDepth, s.TargetFPS, s.EncoderGPU, s.PathKind)
	}
}

// TestPairingKeepsTheCheckedFingerprint: the PIN pairs only with the host
// whose fingerprint the pairing panel showed; another host answering in
// its place is refused.
func TestPairingKeepsTheCheckedFingerprint(t *testing.T) {
	shown := startFakeHost(t, "off")
	other := startFakeHost(t, "off")
	b, _ := newTestBackend(t, t.TempDir(), nil)
	ctx := context.Background()
	if err := b.Connect(ctx, shown.Addr().String()); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect: %v", err)
	}
	if p := b.Pairing(); p.Fingerprint != shown.Fingerprint() {
		t.Fatalf("pairing shows %q, want the host's %q", p.Fingerprint, shown.Fingerprint())
	}
	b.mu.Lock()
	b.pairTo.Addr = other.Addr().String()
	b.mu.Unlock()
	if err := b.SubmitPIN(ctx, "246810"); !errors.Is(err, config.ErrFingerprintMismatch) {
		t.Fatalf("PIN to another host: %v, want ErrFingerprintMismatch", err)
	}
	if n := len(other.PairedClients()); n != 0 {
		t.Fatalf("the other host paired %d clients", n)
	}
}
