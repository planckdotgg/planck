package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

// startRouteCapsHost is startRouteHost resolving streams from both ends'
// capabilities, with PyroWave framed in the session's chunks.
func startRouteCapsHost(t *testing.T) *Host {
	t.Helper()
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "route-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "127.0.0.1:0",
		NoTailscale: true, PIN: "112233", NoAudio: true, Capabilities: testHostCaps, Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			fp := fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}
			if p.Codec == encode.PyroWave {
				return &chunkPipe{fakePipe: fp, chunk: p.ChunkLen}, nil
			}
			return &fp, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func routeCapsClient(t *testing.T, o defaults.UserOverrides) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	r.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			r.corrupt.Add(1)
		}
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "route-client", PIN: "112233", NoAudio: true, Overrides: o, Capabilities: testClientCaps,
		OpenSink: NullSinkOpener(r.sink), OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
		Route:     RouteConfig{PreferLAN: true, ProbeInterval: 100 * time.Millisecond, StallTimeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

// TestRouteMoveResolvesProfile: a stream that moves from the tailnet to
// the LAN offers its capabilities again, and takes the profile the host
// resolves for the LAN.
func TestRouteMoveResolvesProfile(t *testing.T) {
	h := startRouteCapsHost(t)
	lan := newLANResponder(t, h)
	r := routeCapsClient(t, defaults.UserOverrides{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	if p := r.client.Stats().Profile; p == nil || p.Route != defaults.RouteTailscaleDirect {
		t.Fatalf("tailnet profile %+v", p)
	}
	lan.up()
	waitFor(t, "the move to the LAN", func() bool {
		cs := r.client.Stats()
		return cs.Path == discovery.PathLANDirect.String() && cs.RouteSwitches == 1
	})
	cs := r.client.Stats()
	if cs.Profile == nil || cs.Profile.Route != defaults.RouteLAN || cs.Profile.MaxDatagram <= 1152 {
		t.Fatalf("LAN profile %+v", cs.Profile)
	}
	units := r.sink.Units.Load()
	waitFor(t, "video on the LAN", func() bool { return r.sink.Units.Load() >= units+30 })
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d corrupt units", n)
	}
}

// TestRouteMoveKeepsFormat: a stream moving to a route where the host
// would resolve another format, as a PyroWave override does on the LAN
// after a tailnet start in HEVC, keeps its format, so its decoder goes on.
func TestRouteMoveKeepsFormat(t *testing.T) {
	h := startRouteCapsHost(t)
	lan := newLANResponder(t, h)
	r := routeCapsClient(t, defaults.UserOverrides{Codec: "pyrowave"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	if cs := r.client.Stats(); cs.Codec != "HEVC" || cs.Path != discovery.PathTailscaleDirect.String() {
		t.Fatalf("tailnet start: %s on %s, want the HEVC fallback", cs.Codec, cs.Path)
	}
	lan.up()
	waitFor(t, "the move to the LAN", func() bool {
		cs := r.client.Stats()
		return cs.Path == discovery.PathLANDirect.String() && cs.RouteSwitches == 1
	})
	cs := r.client.Stats()
	if cs.Codec != "HEVC" || cs.Profile == nil || cs.Profile.Route != defaults.RouteLAN || cs.Profile.Codec != defaults.CodecHEVC {
		t.Fatalf("on the LAN: %s, profile %+v", cs.Codec, cs.Profile)
	}
	units := r.sink.Units.Load()
	waitFor(t, "video on the LAN", func() bool { return r.sink.Units.Load() >= units+30 })
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d corrupt units", n)
	}
	select {
	case <-r.client.Done():
		t.Fatalf("stream ended: %v", r.client.Err())
	default:
	}
}
