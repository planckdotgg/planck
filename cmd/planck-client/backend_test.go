package main

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
)

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newTestBackend(t *testing.T, dir string, probe []netip.AddrPort) (*backend, *atomic.Int64) {
	t.Helper()
	sink := &countSink{}
	f := newFinder("", nil, probe, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.run(ctx)
	b, err := newBackend(backendConfig{
		dir: dir, name: "test-client", finder: f, tailscale: noTailnet{}, openSink: sink.open(), logf: t.Logf,
		encoders: []ui.EncoderInfo{{GPU: "Test GPU", API: "Fake"}},
		hostConfig: func(s ui.Settings) session.HostConfig {
			return session.HostConfig{
				ConfigDir: t.TempDir(), Name: "hosting", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off", FPS: 60, NoTailscale: true,
				Adapter: s.Encoder, OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
				OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil }, Logf: t.Logf,
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	var changes atomic.Int64
	b.Subscribe(func() { changes.Add(1) })
	return b, &changes
}

// TestBackendPairAndStream: an unpaired host opens Pairing with its
// fingerprint, a wrong PIN fails, the right one streams; the HUD's
// telemetry follows the stream and input reaches the host.
func TestBackendPairAndStream(t *testing.T) {
	h := startFakeHost(t, "off")
	dir := t.TempDir()
	b, changes := newTestBackend(t, dir, nil)
	ctx := context.Background()

	if err := b.Connect(ctx, h.Addr().String()); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect unpaired: %v, want ui.ErrPairingRequired", err)
	}
	if p := b.Pairing(); p.Status != ui.PairPending || p.HostName == "" {
		t.Fatalf("pairing: %+v", p)
	}
	if err := b.SubmitPIN(ctx, "000000"); err == nil {
		t.Fatal("wrong PIN accepted")
	}
	if p := b.Pairing(); p.Status != ui.PairFailed || p.Error == "" {
		t.Fatalf("after a wrong PIN: %+v", p)
	}
	if err := b.SubmitPIN(ctx, "246810"); err != nil {
		t.Fatal(err)
	}
	if p := b.Pairing(); p.Status != ui.PairPaired {
		t.Fatalf("after the PIN: %+v", p)
	}
	before := changes.Load()
	waitUntil(t, "live telemetry", func() bool {
		tel := b.Telemetry()
		return tel.Connected && tel.FPS > 30 && tel.TotalMs > 0 && changes.Load() > before+2
	})
	tel := b.Telemetry()
	if tel.HostName != "" && tel.HostName != "fake-host" || tel.PathKind != discovery.PathLANDirect || tel.Codec == 0 {
		t.Errorf("telemetry: %+v", tel)
	}
	b.SendInput(input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft, Pressed: true}})
	waitUntil(t, "input at the host", func() bool { return h.inputs.Load() == 1 })

	hosts := b.Hosts()
	if len(hosts) != 1 || !hosts[0].Paired || hosts[0].Address != h.Addr().String() {
		t.Fatalf("remembered hosts: %+v", hosts)
	}
	b.Disconnect()
	if b.Telemetry().Connected {
		t.Fatal("telemetry still live after Disconnect")
	}
	waitUntil(t, "the host to see the goodbye", func() bool { return !h.Stats().Connected })

	if err := b.Connect(ctx, hosts[0].ID); err != nil {
		t.Fatalf("reconnect to the paired host: %v", err)
	}
	waitUntil(t, "the stream again", func() bool { return b.Telemetry().Connected })
}

// TestBackendDiscoveredHost lists a host found by a discovery probe and
// connects through the race to its ID.
func TestBackendDiscoveredHost(t *testing.T) {
	h := startFakeHost(t, "127.0.0.1:0")
	disc := h.DiscoveryAddr().String()
	b, _ := newTestBackend(t, t.TempDir(), []netip.AddrPort{netip.MustParseAddrPort(disc)})
	waitUntil(t, "the host to be found", func() bool { return len(b.Hosts()) == 1 })
	hi := b.Hosts()[0]
	if hi.ID != h.HostID() || hi.Name != "fake-host" || !hi.Online || hi.Paired || hi.PathKind != discovery.PathLANDirect {
		t.Fatalf("host: %+v", hi)
	}
	if err := b.Connect(context.Background(), hi.ID); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect: %v", err)
	}
	if p := b.Pairing(); p.Fingerprint != h.Fingerprint() || p.HostName != "fake-host" {
		t.Fatalf("pairing shows %+v, want the host's fingerprint", p)
	}
	if err := b.SubmitPIN(context.Background(), "246810"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "paired in the list", func() bool { return b.Hosts()[0].Paired })
}

func TestBackendSettingsAndHosting(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	s := b.Settings()
	s.Codec, s.Chroma, s.TargetFPS, s.MaxBitrateMbps, s.AudioEnabled, s.Encoder = encode.AV1, encode.P010, 240, 80, false, "Test GPU"
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PreferredCodec != "av1" || stored.PreferredChroma != "p010" || stored.TargetFPS != 240 || stored.MaxBitrateKbps != 80_000 || stored.AudioEnabled {
		t.Fatalf("stored: %+v", stored)
	}
	if got := settingsFrom(stored); got.Codec != encode.AV1 || got.Chroma != encode.P010 || got.MaxBitrateMbps != 80 {
		t.Fatalf("settings read back: %+v", got)
	}

	if err := b.SetHosting(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if st := b.Hosting(); !st.On || st.Encoder != "Test GPU Fake" || st.Err != "" {
		t.Fatalf("hosting: %+v", st)
	}
	if err := b.SetHosting(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if st := b.Hosting(); st.On {
		t.Fatalf("hosting after off: %+v", st)
	}
}

// TestBackendSinkFailsAfterPairing: the PIN pairs and the host rotates it,
// then the video sink fails. Pairing reports paired with the error, not a
// PIN to try again, the host is listed as paired at once, and connecting to
// it from the list streams without a PIN.
func TestBackendSinkFailsAfterPairing(t *testing.T) {
	h := startFakeHost(t, "off")
	b, _ := newTestBackend(t, t.TempDir(), nil)
	ctx := context.Background()
	sinkErr := errors.New("no window to show the stream in")
	var failSink atomic.Bool
	failSink.Store(true)
	open := b.openSink
	b.openSink = func(cfg decode.Config) (session.Sink, error) {
		if failSink.Load() {
			return nil, sinkErr
		}
		return open(cfg)
	}

	if err := b.Connect(ctx, h.Addr().String()); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect unpaired: %v, want ui.ErrPairingRequired", err)
	}
	if err := b.SubmitPIN(ctx, "246810"); !errors.Is(err, sinkErr) {
		t.Fatalf("SubmitPIN: %v, want the sink's error", err)
	}
	if p := b.Pairing(); p.Status != ui.PairPaired || p.Error == "" {
		t.Fatalf("pairing after the sink failed: %+v, want paired with an error", p)
	}
	hosts := b.Hosts()
	if len(hosts) != 1 || !hosts[0].Paired || hosts[0].Address != h.Addr().String() {
		t.Fatalf("hosts after pairing: %+v", hosts)
	}

	failSink.Store(false)
	if err := b.Connect(ctx, hosts[0].ID); err != nil {
		t.Fatalf("connect to the paired host: %v", err)
	}
	waitUntil(t, "the stream", func() bool { return b.Telemetry().Connected })
}
