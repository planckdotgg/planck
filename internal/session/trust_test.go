package session

import (
	"context"
	"crypto/sha256"
	"net/netip"
	"testing"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// newTrustHost makes a host from host.json in dir, as edit leaves it,
// with a WhoIs that says every peer is the host's own tailnet user.
func newTrustHost(t *testing.T, edit func(*config.HostConfig)) (*Host, *fakeWhoIs) {
	t.Helper()
	dir := t.TempDir()
	stored := config.DefaultHostConfig(dir)
	if edit != nil {
		edit(&stored)
	}
	if err := config.SaveHostConfig(dir, stored); err != nil {
		t.Fatal(err)
	}
	who := &fakeWhoIs{sameUser: true}
	h, err := NewHost(HostConfig{ConfigDir: dir, WhoIs: who, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return h, who
}

func tailscaleAuth(remote string) transport.AuthInfo {
	return transport.AuthInfo{Method: wire.AuthTailscale, DeviceName: "laptop",
		ClientFingerprint: sha256.Sum256([]byte(remote)), RemoteAddr: netip.MustParseAddrPort(remote)}
}

// TestRequirePINForLAN: an unpaired peer on a LAN address pairs only with
// the PIN, even when Tailscale would vouch for it; a tailnet address
// still pairs by its identity.
func TestRequirePINForLAN(t *testing.T) {
	h, who := newTrustHost(t, nil)
	for _, lan := range []string{"192.168.188.41:50000", "10.0.0.7:50000", "172.16.3.9:50000", "[fe80::1]:50000", "[fd00::5]:50000"} {
		if _, err := h.trust(context.Background(), tailscaleAuth(lan)); err == nil {
			t.Errorf("LAN peer %s trusted without a PIN", lan)
		}
	}
	if n := len(h.PairedClients()); n != 0 || who.calls.Load() != 0 {
		t.Fatalf("after LAN peers: %d paired, %d WhoIs calls", n, who.calls.Load())
	}
	for _, tailnet := range []string{"100.101.102.103:50000", "[fd7a:115c:a1e0::1]:50000"} {
		if _, err := h.trust(context.Background(), tailscaleAuth(tailnet)); err != nil {
			t.Errorf("tailnet peer %s: %v", tailnet, err)
		}
	}
	pin := transport.AuthInfo{Method: wire.AuthPIN, DeviceName: "tv", ClientFingerprint: sha256.Sum256([]byte("tv")),
		RemoteAddr: netip.MustParseAddrPort("192.168.188.50:50000"), PINVerified: true}
	if _, err := h.trust(context.Background(), pin); err != nil {
		t.Fatalf("LAN peer with the PIN: %v", err)
	}
}

// TestAllowTailscaleSameUserOff: host.json's allowTailscaleSameUser false
// trusts no one by Tailscale identity.
func TestAllowTailscaleSameUserOff(t *testing.T) {
	h, who := newTrustHost(t, func(c *config.HostConfig) { c.AllowTailscaleSameUser = false })
	if _, err := h.trust(context.Background(), tailscaleAuth("100.101.102.103:50000")); err == nil {
		t.Fatal("trusted a tailnet peer with allowTailscaleSameUser off")
	}
	if n := len(h.PairedClients()); n != 0 || who.calls.Load() != 0 {
		t.Fatalf("%d paired, %d WhoIs calls", n, who.calls.Load())
	}
}
