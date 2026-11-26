package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/transport"
)

func TestRoutePacingCalibratedQueue(t *testing.T) {
	mac := clientCaps{Client: defaults.ClientCapabilities{OS: "darwin"}}
	if p, ok := routePacing(defaults.RouteLAN, 2500, mac); !ok || p.Queue != transport.MacReceiveQueue {
		t.Fatalf("uncalibrated macOS client on the LAN: %+v, %v", p.Queue, ok)
	}
	calibrated := mac
	calibrated.Route.ReceiveQueue = &defaults.ReceiveQueue{Bytes: 300 * 1452, DrainMbps: 900}
	if p, ok := routePacing(defaults.RouteLAN, 2500, calibrated); !ok || p.Queue != (transport.ReceiveQueue{Bytes: 300 * 1452, DrainMbps: 900}) || p.LinkMbps != 2500 {
		t.Fatalf("calibrated macOS client on the LAN: %+v, %v", p, ok)
	}
	unbounded := mac
	unbounded.Route.ReceiveQueue = &defaults.ReceiveQueue{}
	if p, ok := routePacing(defaults.RouteLAN, 2500, unbounded); !ok || p.Queue != (transport.ReceiveQueue{}) {
		t.Fatalf("a client calibration found no queue on keeps the guess: %+v", p.Queue)
	}
	win := clientCaps{Client: defaults.ClientCapabilities{OS: "windows"}}
	if _, ok := routePacing(defaults.RouteTailscaleDirect, 0, win); ok {
		t.Fatal("an uncalibrated tailnet route changed the default pacing")
	}
	win.Route.ReceiveQueue = &defaults.ReceiveQueue{Bytes: 40 * 1152, DrainMbps: 80}
	p, ok := routePacing(defaults.RouteTailscaleDirect, 0, win)
	if !ok || p.Queue != (transport.ReceiveQueue{Bytes: 40 * 1152, DrainMbps: 80}) || p.Share != transport.DefaultPacing.Share || p.LinkMbps != 0 {
		t.Fatalf("calibrated tailnet route: %+v, %v", p, ok)
	}
}

// calibrationHost resolves streams from both ends' capabilities and
// records every pipe it opens.
func calibrationHost(t *testing.T, withCaps bool) (*Host, func() []VideoParams) {
	t.Helper()
	var mu sync.Mutex
	var opened []VideoParams
	cfg := HostConfig{
		ConfigDir: t.TempDir(), Name: "cal-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, PIN: "112233", NoAudio: true, Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			mu.Lock()
			opened = append(opened, p)
			mu.Unlock()
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
	}
	if withCaps {
		cfg.Capabilities = testHostCaps
	}
	h, err := NewHost(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h, func() []VideoParams {
		mu.Lock()
		defer mu.Unlock()
		return append([]VideoParams(nil), opened...)
	}
}

func calibrationClient(t *testing.T, dir string, mode CalibrationMode, o defaults.UserOverrides) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	c, err := NewClient(ClientConfig{
		ConfigDir: dir, Name: "cal-client", PIN: "112233", NoAudio: true, Overrides: o, Capabilities: testClientCaps,
		OpenSink: NullSinkOpener(r.sink), OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale: noTailscale{}, Logf: t.Logf, Calibrate: mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

// TestCalibrationStreams: a video calibration session streams the
// synthetic pattern, never a virtual display, and a network one streams
// nothing but carries probe trains.
func TestCalibrationStreams(t *testing.T) {
	h, opened := calibrationHost(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addr := h.Addr().String()

	video := calibrationClient(t, t.TempDir(), CalibrateVideo, defaults.UserOverrides{VirtualDisplay: "on"})
	if err := video.client.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "synthetic frames", func() bool { return video.client.Stats().FramesReceived > 5 })
	ps := opened()
	if len(ps) == 0 || !ps[0].Synth || ps[0].VirtualDisplay {
		t.Fatalf("video calibration opened %+v, want the synthetic pattern on no virtual display", ps)
	}
	video.client.Disconnect()

	network := calibrationClient(t, t.TempDir(), CalibrateNetwork, defaults.UserOverrides{})
	if err := network.client.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	train, err := network.client.Probe(ctx, transport.ProbeSpec{Count: 32, Size: 1000})
	if err != nil || train.Received() != 32 {
		t.Fatalf("probe on a network calibration: %d of 32, %v", train.Received(), err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(opened()); n != len(ps) {
		t.Fatalf("a network calibration opened a video pipe: %d pipes, %d before", n, len(ps))
	}
	if st := network.client.Stats(); st.FramesReceived != 0 {
		t.Fatalf("a network calibration streamed %d frames", st.FramesReceived)
	}
}

// TestCalibrationRefusedByOlderHost: a host without
// FeatureCalibration refuses a calibration with a clear error.
func TestCalibrationRefusedByOlderHost(t *testing.T) {
	h, opened := calibrationHost(t, false)
	r := calibrationClient(t, t.TempDir(), CalibrateVideo, defaults.UserOverrides{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.client.Connect(ctx, Target{Addr: h.Addr().String()})
	if !errors.Is(err, transport.ErrCalibrationUnsupported) {
		t.Fatalf("connect: %v, want ErrCalibrationUnsupported", err)
	}
	if len(opened()) != 0 {
		t.Fatal("the older host opened a stream")
	}
}

// TestStoredCalibrationApplies: a stream on a calibrated route asks for
// the calibration's knobs where the user's are auto, keeps the user's
// own, and offers the calibrated receive queue; a calibration session
// takes none of it.
func TestStoredCalibrationApplies(t *testing.T) {
	h, _ := calibrationHost(t, true)
	addr := h.Addr().String()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := calibrationClient(t, dir, "", defaults.UserOverrides{})
	if err := first.client.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	first.client.Disconnect()
	queue := defaults.ReceiveQueue{Bytes: 200 * 1452, DrainMbps: 600}
	err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		return c.ApplyCalibration("addr:"+addr, "lan-direct", config.CalibratedRoute{
			At: time.Now(), Overrides: defaults.UserOverrides{FPS: "60", SubFrameSlices: "1"}, ReceiveQueue: queue,
			Link: config.CalibratedLink{BandwidthMbps: 1800, RTTP50Micros: 300},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	sent := func() clientCaps {
		var cc clientCaps
		waitFor(t, "the stream", func() bool { return h.current() != nil })
		if err := json.Unmarshal(h.current().sess.PeerCapabilities(), &cc); err != nil {
			t.Fatal(err)
		}
		return cc
	}

	user := calibrationClient(t, dir, "", defaults.UserOverrides{SubFrameSlices: "4"})
	if err := user.client.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	cc := sent()
	if cc.Overrides.FPS != "60" || cc.Overrides.SubFrameSlices != "4" {
		t.Fatalf("overrides %+v: want fps 60 from the calibration and the user's 4 slices", cc.Overrides)
	}
	if q := cc.Route.ReceiveQueue; q == nil || *q != queue || cc.Route.ProbedMbps != 1800 || cc.Route.RTTMicros != 300 {
		t.Fatalf("route %+v", cc.Route)
	}
	if p := user.client.Stats().Profile; p == nil || p.FPS != 60 {
		t.Fatalf("profile %+v, want 60 fps", p)
	}
	user.client.Disconnect()
	waitFor(t, "the stream to end", func() bool { return h.current() == nil })

	cal := calibrationClient(t, dir, CalibrateVideo, defaults.UserOverrides{})
	if err := cal.client.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	if cc := sent(); cc.Overrides.FPS != "" || cc.Route.ReceiveQueue != nil {
		t.Fatalf("a calibration session took the stored calibration: %+v %+v", cc.Overrides, cc.Route)
	}
}
