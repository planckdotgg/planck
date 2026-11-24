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
	"github.com/planckdotgg/planck/pkg/defaults"
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
	s.AudioEnabled, s.Encoder = false, "Test GPU"
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AudioEnabled {
		t.Fatalf("stored: %+v", stored)
	}
	if got := settingsFrom(stored); got.AudioEnabled {
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

// TestBackendOverridesPersist: overrides for every host and for one host
// survive a restart, a host's knobs win over every host's and the command
// line's over both, all-auto host overrides leave client.json, and an
// invalid knob is refused.
func TestBackendOverridesPersist(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	all := defaults.UserOverrides{Codec: "hevc", FPS: "120", BitrateMbps: 200}
	host := defaults.UserOverrides{Codec: "av1", Chroma: "444", Resolution: "custom", CustomWidth: 2560, CustomHeight: 1600}
	if err := b.SaveOverrides("", all); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveOverrides("h1", host); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveOverrides("h1", defaults.UserOverrides{Codec: "vp9"}); err == nil {
		t.Fatal("codec vp9 accepted")
	}

	b2, _ := newTestBackend(t, dir, nil)
	if got := b2.Overrides(""); got != all.Normalize() {
		t.Fatalf("all hosts after a restart: %+v", got)
	}
	if got := b2.Overrides("h1"); got != host.Normalize() {
		t.Fatalf("h1 after a restart: %+v", got)
	}
	if got := b2.Overrides("h2"); got != (defaults.UserOverrides{}) {
		t.Fatalf("h2 has overrides: %+v", got)
	}
	got := b2.overridesFor("h1")
	if got.Codec != "av1" || got.Chroma != "444" || got.FPS != "120" || got.BitrateMbps != 200 || got.CustomWidth != 2560 {
		t.Fatalf("h1 merged: %+v", got)
	}
	if got := b2.overridesFor("h2"); got.Codec != "hevc" || got.Chroma != defaults.Auto {
		t.Fatalf("h2 merged: %+v", got)
	}
	b2.overrides = defaults.UserOverrides{Codec: "h264"}
	if got := b2.overridesFor("h1"); got.Codec != "h264" || got.Chroma != "444" {
		t.Fatalf("command line over h1: %+v", got)
	}

	if err := b2.SaveOverrides("h1", defaults.UserOverrides{}); err != nil {
		t.Fatal(err)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.HostOverrides["h1"]; ok || stored.Overrides != all.Normalize() {
		t.Fatalf("after resetting h1: %+v, all %+v", stored.HostOverrides, stored.Overrides)
	}
}

// TestBackendOverridesRestartStream: changing the streaming host's
// overrides reconnects with them; another host's leave the stream alone.
func TestBackendOverridesRestartStream(t *testing.T) {
	h := startFakeHost(t, "off")
	b, _ := newTestBackend(t, t.TempDir(), nil)
	ctx := context.Background()
	if err := b.Connect(ctx, h.Addr().String()); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect unpaired: %v", err)
	}
	if err := b.SubmitPIN(ctx, "246810"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stream", func() bool { return b.Telemetry().Connected })
	tel := b.Telemetry()
	if tel.HostID != "addr:"+h.Addr().String() {
		t.Fatalf("telemetry host ID %q, want the pin key", tel.HostID)
	}
	first := b.current()
	if err := b.SaveOverrides("someone-else", defaults.UserOverrides{FPS: "60"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if b.current() != first {
		t.Fatal("another host's overrides restarted the stream")
	}
	if err := b.SaveOverrides(tel.HostID, defaults.UserOverrides{FPS: "60"}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "a new stream", func() bool {
		c := b.current()
		return c != nil && c != first && c.Stats().Connected
	})
}

func TestTelemetryProfile(t *testing.T) {
	p := &defaults.ResolvedProfile{Codec: defaults.CodecHEVC, BitDepth: 10, Width: 1920, Height: 1080, FPS: 120}
	tel := telemetry(session.ClientStats{Connected: true, HostID: "h1", Profile: p})
	if tel.HostID != "h1" || tel.Profile != p {
		t.Fatalf("%+v", tel)
	}
	if tel := telemetry(session.ClientStats{Addr: "10.0.0.2:47700"}); tel.HostID != "addr:10.0.0.2:47700" {
		t.Fatalf("host without an ID: %q", tel.HostID)
	}
}

func TestLegacyQuality(t *testing.T) {
	for _, tc := range []struct {
		o    defaults.UserOverrides
		want session.Quality
	}{
		{defaults.UserOverrides{}.Normalize(), session.Quality{}},
		{defaults.UserOverrides{Codec: "pyrowave", Chroma: "444"}, session.Quality{Chroma: encode.YUV444}},
		{defaults.UserOverrides{Codec: "av1", BitDepth: "10", FPS: "144", SubFrameSlices: "4", BitrateMbps: 80},
			session.Quality{Codec: encode.AV1, Chroma: encode.P010, FPS: 144, Slices: 4, BitrateKbps: 80_000}},
		{defaults.UserOverrides{Codec: "h264", Chroma: "444", BitDepth: "10"}, session.Quality{Codec: encode.H264, Chroma: encode.YUV444P10}},
		{defaults.UserOverrides{Chroma: "420"}, session.Quality{Chroma: encode.NV12}},
	} {
		if got := legacyQuality(tc.o); got != tc.want {
			t.Errorf("%+v: %+v, want %+v", tc.o, got, tc.want)
		}
	}
}

func TestRouteAllowed(t *testing.T) {
	lan := discovery.Candidate{Kind: discovery.PathLANDirect, Addr: netip.MustParseAddrPort("192.168.1.20:47700")}
	ts := discovery.Candidate{Kind: discovery.PathTailscaleDirect, Addr: netip.MustParseAddrPort("100.64.0.7:47700")}
	t0 := session.Target{Candidates: []discovery.Candidate{lan, ts}, Addr: "100.64.0.7:47701"}
	got, err := routeAllowed(t0, "lan")
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0] != lan || got.Addr != "" {
		t.Fatalf("lan: %+v, %v", got, err)
	}
	if len(t0.Candidates) != 2 {
		t.Fatalf("routeAllowed changed its argument: %+v", t0)
	}
	if got, err := routeAllowed(t0, "tailscale"); err != nil || len(got.Candidates) != 1 || got.Candidates[0] != ts || got.Addr != t0.Addr {
		t.Fatalf("tailscale: %+v, %v", got, err)
	}
	if got, err := routeAllowed(t0, "auto"); err != nil || len(got.Candidates) != 2 {
		t.Fatalf("auto: %+v, %v", got, err)
	}
	if _, err := routeAllowed(session.Target{Candidates: []discovery.Candidate{ts}}, "lan"); err == nil {
		t.Fatal("lan with only a tailnet route: no error")
	}
}

// TestTelemetryStages: the host's capture stage and the round trip reach
// the UI, and the encode stage leaves capture out once the host splits it.
func TestTelemetryStages(t *testing.T) {
	cs := session.ClientStats{Connected: true, RTTMs: 0.41, MaxBitrateKbps: 150_000, FramesDropped: 4}
	cs.Encode.P50 = 4.3
	tel := telemetry(cs)
	if tel.EncodeMs != 4.3 || tel.CaptureMs != 0 || tel.RTTMs != 0.41 || tel.CeilingMbps != 150 || tel.FramesDropped != 4 {
		t.Fatalf("without host stats: %+v", tel)
	}
	cs.CaptureMs, cs.HostEncodeMs = 2.9, 1.4
	if tel := telemetry(cs); tel.CaptureMs != 2.9 || tel.EncodeMs != 1.4 {
		t.Fatalf("with host stats: capture %v encode %v, want 2.9 and 1.4", tel.CaptureMs, tel.EncodeMs)
	}
}

// TestBackendOverlayAndSoundPersist: the overlay's layout, the volume and
// mute, and the clipboard switches come back on the next run.
func TestBackendOverlayAndSoundPersist(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	if s := b.Settings(); s.AudioVolume != 1 || s.AudioMuted || s.ClipboardSend || s.ClipboardReceive {
		t.Fatalf("first run: %+v", s)
	}
	if l := b.OverlayLayout(); l.Placed {
		t.Fatalf("first run layout: %+v", l)
	}
	s := b.Settings()
	s.AudioVolume, s.AudioMuted, s.ClipboardSend, s.ClipboardReceive = 0.35, true, true, true
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	l := ui.OverlayLayout{Placed: true, Dock: ui.DockRight, DockFrac: 0.25, ButtonX: 0.9, ButtonY: 0.3,
		Metrics: true, MetricsX: 0.5, MetricsY: 0.1, MetricsCompact: true}
	if err := b.SaveOverlayLayout(l); err != nil {
		t.Fatal(err)
	}
	again, _ := newTestBackend(t, dir, nil)
	got := again.Settings()
	if got.AudioVolume != 0.35 || !got.AudioMuted || !got.ClipboardSend || !got.ClipboardReceive {
		t.Fatalf("next run: %+v", got)
	}
	if gl := again.OverlayLayout(); gl != l {
		t.Fatalf("next run layout %+v, want %+v", gl, l)
	}
}

// TestBackendCapabilitiesWindowDisplay: the capabilities a stream offers
// carry the display the window is on, a second 3440x1440 75 Hz one here,
// and fall back to the probe without a window.
func TestBackendCapabilitiesWindowDisplay(t *testing.T) {
	second := defaults.ClientDisplay{WidthPx: 3440, HeightPx: 1440, Scale: 1, MaxHz: 75, MinHz: 24}
	b := &backend{display: func() defaults.ClientDisplay { return second }}
	if got := b.capabilities().Display; got != second {
		t.Fatalf("display %+v, want the window's %+v", got, second)
	}
	b.display = func() defaults.ClientDisplay { return defaults.ClientDisplay{} }
	if got, want := b.capabilities().Display, session.ProbeClientCapabilities().Display; got != want {
		t.Fatalf("no window: display %+v, want the probe's %+v", got, want)
	}
}
