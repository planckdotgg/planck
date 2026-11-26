package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDir(t *testing.T) {
	d, err := Dir()
	if err != nil {
		t.Skip("no user config dir:", err)
	}
	if filepath.Base(d) != "planck" {
		t.Fatalf("Dir() = %s", d)
	}
}

func TestHostConfigRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planck")

	first, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.HostID) != 32 || first.ControlPort != 47701 || first.MediaPort != 47702 || !first.RequirePINForLAN || !first.AllowTailscaleSameUser {
		t.Fatalf("defaults = %+v", first)
	}
	if first.CertPath != filepath.Join(dir, "host-cert.pem") {
		t.Fatalf("CertPath = %s", first.CertPath)
	}

	first.Name = "bigboi"
	first.Display = `\\.\DISPLAY1`
	first.Codec, first.Chroma, first.FPS = "av1", "yuv444p10", 240
	first.RequirePINForLAN = false
	if err := SaveHostConfig(dir, first); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, HostConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("host.json mode %v, want 0600", fi.Mode().Perm())
		}
	}

	again, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("reloaded %+v\nwant     %+v", again, first)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("config dir has %d entries, want only host.json (temp file left behind?)", len(entries))
	}
}

func TestClientConfigRoundTripWithPins(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AudioEnabled || cfg.PinnedHosts == nil || cfg.PreferredCodec != "hevc" {
		t.Fatalf("defaults = %+v", cfg)
	}

	fp := strings.Repeat("AB", 32)
	paired := time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)
	if err := cfg.Pin(PinnedHost{HostID: "h1", Name: "bigboi", CertFingerprint: fp, PairedAt: paired, LastAddr: "192.168.188.126:47998"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveClientConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := loaded.CheckPin("h1", strings.ToLower(fp))
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "bigboi" || !h.PairedAt.Equal(paired) || h.LastAddr != "192.168.188.126:47998" || h.CertFingerprint != strings.ToLower(fp) {
		t.Fatalf("pinned host = %+v", h)
	}
	if _, err := loaded.CheckPin("h1", strings.Repeat("cd", 32)); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong fingerprint: err = %v", err)
	}
	if _, err := loaded.CheckPin("h2", fp); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("unknown host: err = %v", err)
	}
	if err := loaded.Pin(PinnedHost{HostID: "h3", CertFingerprint: "short"}); err == nil {
		t.Fatal("pinned a malformed fingerprint")
	}
}

func TestValidateRejects(t *testing.T) {
	dir := t.TempDir()
	host := DefaultHostConfig(dir)
	host.Chroma = "420"
	if err := SaveHostConfig(dir, host); err == nil {
		t.Fatal("saved unknown chroma")
	}
	host = DefaultHostConfig(dir)
	host.MediaPort = host.ControlPort
	if err := host.Validate(); err == nil {
		t.Fatal("accepted equal control and media ports")
	}

	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(`{"preferredCodec":"vp9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientConfig(dir); err == nil {
		t.Fatal("loaded unknown codec")
	}
	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClientConfig(dir); err == nil {
		t.Fatal("loaded corrupt JSON")
	}
}

func TestIdentityCreateAndReload(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "id", "host-cert.pem"), filepath.Join(dir, "id", "host-key.pem")

	created, err := LoadOrCreateIdentity(certPath, keyPath, "planck-host test")
	if err != nil {
		t.Fatal(err)
	}
	key, ok := created.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Fatalf("key is %T, want ECDSA P-256", created.PrivateKey)
	}
	if created.Leaf.Subject.CommonName != "planck-host test" || created.Leaf.NotAfter.Before(time.Now().Add(19*365*24*time.Hour)) {
		t.Fatalf("leaf = %v until %v", created.Leaf.Subject, created.Leaf.NotAfter)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %v, want 0600", fi.Mode().Perm())
		}
	}

	reloaded, err := LoadOrCreateIdentity(certPath, keyPath, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	fp := CertFingerprint(created.Certificate[0])
	if got := CertFingerprint(reloaded.Certificate[0]); got != fp || len(fp) != 64 || strings.ToLower(fp) != fp {
		t.Fatalf("fingerprint changed or malformed: %s vs %s", got, fp)
	}

	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(certPath, keyPath, "x"); err == nil {
		t.Fatal("regenerated an identity over an existing key")
	}
}

func TestPinnedTLSHandshake(t *testing.T) {
	dir := t.TempDir()
	hostCert, err := LoadOrCreateIdentity(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), "planck-host")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{hostCert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()

	dial := func(fp string) error {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", ln.Addr().String(), &tls.Config{
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: VerifyPinnedCert(fp),
			MinVersion:            tls.VersionTLS13,
		})
		if err == nil {
			conn.Close()
		}
		return err
	}
	if err := dial(CertFingerprint(hostCert.Certificate[0])); err != nil {
		t.Fatalf("pinned fingerprint rejected: %v", err)
	}
	if err := dial(strings.Repeat("0", 64)); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong fingerprint: err = %v, want ErrFingerprintMismatch", err)
	}
}

// TestOverlayLayoutPlacementsAndLegacy: a layout stored before placements
// keeps its fractions for the client to migrate, and one stored with
// placements writes them alone.
func TestOverlayLayoutPlacementsAndLegacy(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"overlay":{"dock":"left","dockFrac":0.5,"buttonX":0.9,"buttonY":0.3,"metrics":true,"metricsX":0.7,"metricsY":0.02}}`
	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := OverlayLayout{Dock: "left", DockFrac: 0.5, ButtonX: 0.9, ButtonY: 0.3, Metrics: true, MetricsX: 0.7, MetricsY: 0.02}
	if o := cfg.Overlay; o == nil || *o != want {
		t.Fatalf("legacy overlay %+v, want %+v", o, want)
	}

	cfg.Overlay = &OverlayLayout{Dock: "right", DockFrac: 0.25, Metrics: true, MetricsCompact: true,
		Button:       &Placement{AnchorX: "center", AnchorY: "end", OffsetX: -10.5, OffsetY: 40},
		MetricsPlace: &Placement{AnchorX: "end", AnchorY: "start", OffsetX: 16, OffsetY: 16}}
	if err := SaveClientConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ClientConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"buttonX"`, `"buttonY"`, `"metricsX"`, `"metricsY"`} {
		if strings.Contains(string(raw), s) {
			t.Errorf("saved %s with placements set: %s", s, raw)
		}
	}
	loaded, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	o := loaded.Overlay
	if o == nil || o.Button == nil || o.MetricsPlace == nil || *o.Button != *cfg.Overlay.Button || *o.MetricsPlace != *cfg.Overlay.MetricsPlace ||
		o.Dock != "right" || o.DockFrac != 0.25 || !o.MetricsCompact {
		t.Fatalf("round trip %+v", o)
	}
}

func TestHostConfigHostingFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, HostConfigFile), []byte(`{"hostID":"h1","controlPort":47701,"mediaPort":47702,"codec":"hevc","chroma":"nv12","fps":60,"maxBitrateKbps":1000,"certPath":"c","keyPath":"k"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if old.DiscoveryPort != DefaultDiscoveryPort {
		t.Errorf("a host.json from before discoveryPort: port %d, want %d", old.DiscoveryPort, DefaultDiscoveryPort)
	}

	host := DefaultHostConfig(dir)
	host.ControlPort, host.MediaPort, host.DiscoveryPort = 0, 0, 0
	host.FixedPIN, host.Clipboard, host.NoInput, host.Adapter, host.VDDPrimary = "012345", ClipboardHostToClient, true, "4080", true
	if err := SaveHostConfig(dir, host); err != nil {
		t.Fatalf("ports left to pick: %v", err)
	}
	back, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.DiscoveryPort != 0 || back.ControlPort != 0 || back.FixedPIN != "012345" || back.Clipboard != ClipboardHostToClient || !back.NoInput || back.Adapter != "4080" || !back.VDDPrimary {
		t.Errorf("round trip: %+v", back)
	}

	for _, bad := range []func(*HostConfig){
		func(c *HostConfig) { c.FixedPIN = "12345" },
		func(c *HostConfig) { c.FixedPIN = "12345a" },
		func(c *HostConfig) { c.Clipboard = "sideways" },
	} {
		c := DefaultHostConfig(dir)
		bad(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestClientDeviceName(t *testing.T) {
	dir := t.TempDir()
	if err := UpdateClientConfig(dir, func(c *ClientConfig) error { c.DeviceName = "couch-mac"; return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := LoadClientConfig(dir)
	if err != nil || c.DeviceName != "couch-mac" {
		t.Fatalf("device name %q, %v", c.DeviceName, err)
	}
}

func TestStreamWindows(t *testing.T) {
	for _, c := range []struct{ set, want int }{{0, DefaultStreamWindows}, {1, MinStreamWindows}, {3, 3}, {9, MaxStreamWindows}} {
		if got := (ClientConfig{MaxStreamWindows: c.set}).StreamWindows(); got != c.want {
			t.Errorf("MaxStreamWindows %d: %d windows, want %d", c.set, got, c.want)
		}
	}
}

func TestTelemetryEnabledDefaultsOnAndPersistsOff(t *testing.T) {
	dir := t.TempDir()
	if !TelemetryOn(dir) {
		t.Fatal("telemetry is off without a client.json")
	}
	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(`{"targetFPS": 60, "maxBitrateKbps": 1000, "preferredCodec": "hevc", "preferredChroma": "nv12"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !TelemetryOn(dir) {
		t.Fatal("telemetry is off in a client.json that does not name it")
	}
	if err := UpdateClientConfig(dir, func(c *ClientConfig) error { off := false; c.TelemetryEnabled = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ClientConfigFile))
	if !strings.Contains(string(raw), `"telemetryEnabled": false`) {
		t.Fatalf("client.json does not keep the switch off:\n%s", raw)
	}
	if TelemetryOn(dir) {
		t.Fatal("telemetry is on after it was turned off")
	}
	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if TelemetryOn(dir) {
		t.Fatal("telemetry is on with a client.json that cannot be read")
	}
}
