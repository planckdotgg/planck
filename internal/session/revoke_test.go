package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/transport"
)

// TestRevokeClient: revoking the connected client ends its stream, keeps
// it out of paired-clients.json, and makes it pair again with the PIN.
func TestRevokeClient(t *testing.T) {
	hostDir := t.TempDir()
	hs := startHost(t, hostDir, "127.0.0.1:0", nil, "off")
	addr := hs.host.Addr().String()
	clientDir := t.TempDir()
	r := newClient(t, clientDir, hs.host.PIN())
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	paired := hs.host.PairedClients()
	if len(paired) != 1 {
		t.Fatalf("paired %d clients", len(paired))
	}
	var fp string
	for k, c := range paired {
		fp = k
		if c.LastSeen.IsZero() || c.TrustedViaTailnet() {
			t.Errorf("PIN-paired client: %+v", c)
		}
	}
	if err := hs.host.RevokeClient("00" + fp[2:]); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("revoke an unknown fingerprint: %v, want ErrNotPaired", err)
	}
	waitFor(t, "the host to run the stream", func() bool { return hs.host.Stats().Connected })
	done := r.client.Done()
	if err := hs.host.RevokeClient(fp); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the revoked client's stream did not end")
	}
	if stored, err := LoadPairedClients(hostDir); err != nil || len(stored) != 0 {
		t.Fatalf("paired-clients.json after revoke: %v, %v", stored, err)
	}
	if err := connect(t, newClient(t, clientDir, ""), addr); !errors.Is(err, transport.ErrAuthDenied) {
		t.Fatalf("revoked client reconnects without a PIN: %v, want ErrAuthDenied", err)
	}
}

func TestRevokePairedClientsOffline(t *testing.T) {
	dir := t.TempDir()
	p, err := loadPairings(dir + "/" + PairedClientsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{1, 2, 3} {
		if err := p.add([32]byte{b}, PairedClient{Name: string('a' + rune(b)), Method: "pin"}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := LoadPairedClients(dir)
	var one string
	for k := range all {
		one = k
		break
	}
	if err := RevokePairedClients(dir, []string{one}); err != nil {
		t.Fatal(err)
	}
	if left, _ := LoadPairedClients(dir); len(left) != 2 || left[one] != (PairedClient{}) {
		t.Fatalf("after one revoke: %v", left)
	}
	if err := RevokePairedClients(dir, []string{one}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("revoke twice: %v", err)
	}
	if err := RevokePairedClients(dir, nil); err != nil {
		t.Fatal(err)
	}
	if left, _ := LoadPairedClients(dir); len(left) != 0 {
		t.Fatalf("after revoke all: %v", left)
	}
}

func TestHostConfigFromStored(t *testing.T) {
	s := config.DefaultHostConfig(t.TempDir())
	s.ControlPort, s.MediaPort, s.DiscoveryPort = 41000, 0, 0
	s.Display, s.Adapter, s.VDDPrimary, s.FixedPIN = "\\\\.\\DISPLAY2", "4080", true, "135790"
	s.NoInput, s.Clipboard = true, config.ClipboardClientToHost
	base := DefaultHostConfig()
	base.InjectInput, base.Clipboard = true, ClipboardConfig{Send: true, Receive: true}
	c := base.FromStored(s)
	if c.ListenAddr != ":41000" || c.MediaPort != 0 || c.DiscoveryAddr != "off" {
		t.Errorf("ports: listen %q media %d discovery %q", c.ListenAddr, c.MediaPort, c.DiscoveryAddr)
	}
	if c.Display != s.Display || c.Adapter != "4080" || !c.VDDPrimary || c.PIN != "135790" {
		t.Errorf("display, GPU, VDD, PIN: %+v", c)
	}
	if c.InjectInput || c.Clipboard != (ClipboardConfig{Receive: true}) {
		t.Errorf("permissions: inject %v clipboard %+v", c.InjectInput, c.Clipboard)
	}
	s.DiscoveryPort, s.NoInput, s.Clipboard, s.Adapter = 47700, false, "", ""
	base.Adapter = "Radeon"
	c = base.FromStored(s)
	if c.DiscoveryAddr != ":47700" || !c.InjectInput || c.Clipboard.any() || c.Adapter != "Radeon" {
		t.Errorf("defaults: %+v", c)
	}
}

func TestMeasureEncoder(t *testing.T) {
	var got VideoParams
	open := func(p VideoParams) (VideoPipe, error) { got = p; return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil }
	l, err := MeasureEncoder(context.Background(), open, "4080", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Synth || got.Adapter != "4080" || got.Width != 1920 || got.FPS != 120 {
		t.Errorf("opened %+v", got)
	}
	if l.Samples != 20 || l.P50 < 0 || l.P99 < l.P50 {
		t.Errorf("latency %+v", l)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MeasureEncoder(ctx, open, "", 20); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
