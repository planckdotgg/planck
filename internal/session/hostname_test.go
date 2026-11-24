package session

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/discovery"
)

func probeName(t *testing.T, h *Host) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, _, err := discovery.Probe(ctx, h.DiscoveryAddr().(*net.UDPAddr).AddrPort())
	if err != nil {
		t.Fatal(err)
	}
	return b.Name
}

// TestHostSetName: a running host renamed answers discovery and later
// handshakes with the new name and keeps it in host.json; an empty name
// goes back to the machine's.
func TestHostSetName(t *testing.T) {
	dir := t.TempDir()
	hs := startHost(t, dir, "127.0.0.1:0", nil, "127.0.0.1:0")
	h := hs.host
	if h.Name() != "test-host" || probeName(t, h) != "test-host" {
		t.Fatalf("name %q before the rename", h.Name())
	}
	if err := h.SetName("  Living Room PC "); err != nil {
		t.Fatal(err)
	}
	if got := probeName(t, h); got != "Living Room PC" {
		t.Fatalf("discovery answers %q after the rename", got)
	}
	stored, err := config.LoadHostConfig(dir)
	if err != nil || stored.Name != "Living Room PC" {
		t.Fatalf("host.json name %q, %v", stored.Name, err)
	}

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if got := r.client.Stats().HostName; got != "Living Room PC" {
		t.Fatalf("handshake names the host %q", got)
	}

	if err := h.SetName(strings.Repeat("x", MaxHostNameLen+1)); err == nil {
		t.Fatal("an over-long name was taken")
	}
	if err := h.SetName(""); err != nil {
		t.Fatal(err)
	}
	if got := probeName(t, h); got != config.Hostname() {
		t.Fatalf("discovery answers %q after clearing the name, want %q", got, config.Hostname())
	}
	if stored, _ := config.LoadHostConfig(dir); stored.Name != "" {
		t.Fatalf("host.json name %q after clearing", stored.Name)
	}
}

// TestHostNameFromConfig: without a Name override a host advertises
// host.json's name.
func TestHostNameFromConfig(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateHostConfig(dir, func(c *config.HostConfig) error { c.Name = "studio"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(HostConfig{ConfigDir: dir, NoTailscale: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if h.Name() != "studio" {
		t.Fatalf("name %q, want host.json's", h.Name())
	}
}
