package transport

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/wire"
)

func TestCapabilitiesExchange(t *testing.T) {
	var got ResolveRequest
	p := newPair(t, pairOpts{
		server: func(c *ServerConfig) {
			c.Negotiate = func(wire.SessionConfig) (wire.SessionConfig, error) {
				return wire.SessionConfig{}, errors.New("Negotiate must not run with capabilities")
			}
			c.Capabilities = func() []byte { return []byte(`{"os":"windows"}`) }
			c.Resolve = func(r ResolveRequest) (ResolveResult, error) {
				got = r
				cfg := r.Request
				cfg.Codec, cfg.Slices, cfg.BitrateKbps = wire.CodecPyroWave, 1, 400_000
				return ResolveResult{Config: cfg, Profile: []byte(`{"codec":4}`), StartKbps: 300_000, MaxKbps: 400_000,
					Parity: ParityProfile{CriticalPercent: 25}}, nil
			}
		},
		client: func(c *ClientConfig) { c.Capabilities = []byte(`{"v":1}`) },
	})
	if string(got.ClientCaps) != `{"v":1}` || got.Request.Width != 1280 {
		t.Fatalf("Resolve saw %+v", got)
	}
	if string(p.client.PeerCapabilities()) != `{"os":"windows"}` || string(p.host.PeerCapabilities()) != `{"v":1}` {
		t.Fatalf("caps: client saw %q, host saw %q", p.client.PeerCapabilities(), p.host.PeerCapabilities())
	}
	if string(p.client.Profile()) != `{"codec":4}` || p.client.Config().Codec != wire.CodecPyroWave || !p.client.intra.Load() {
		t.Fatalf("client profile %q config %+v", p.client.Profile(), p.client.Config())
	}
	if k := p.host.TargetBitrateKbps(); k != 300_000 {
		t.Fatalf("start target %d kbps, want 300000", k)
	}
	if n := p.host.pol.critical.Parity(10); n < 3 {
		t.Fatalf("critical parity %d for 10 shards, want 25%% rounded up", n)
	}
}

// TestCapabilitiesOldPeers: a client without the feature gets Negotiate,
// and a host without it never sees the client's JSON.
func TestCapabilitiesOldPeers(t *testing.T) {
	negotiated := false
	old := newPair(t, pairOpts{
		server: func(c *ServerConfig) {
			c.Capabilities = func() []byte { return []byte(`{}`) }
			c.Resolve = func(ResolveRequest) (ResolveResult, error) {
				return ResolveResult{}, errors.New("Resolve must not run for an old client")
			}
			c.Negotiate = func(r wire.SessionConfig) (wire.SessionConfig, error) { negotiated = true; return r, nil }
		},
	})
	if !negotiated || old.client.Profile() != nil || old.host.PeerCapabilities() != nil {
		t.Fatalf("old client: negotiated %v profile %q", negotiated, old.client.Profile())
	}
	oldHost := newPair(t, pairOpts{client: func(c *ClientConfig) { c.Capabilities = []byte(`{"v":1}`) }})
	if oldHost.client.PeerCapabilities() != nil || oldHost.client.Profile() != nil || oldHost.client.Config().Codec != wire.CodecHEVC {
		t.Fatalf("old host: caps %q profile %q", oldHost.client.PeerCapabilities(), oldHost.client.Profile())
	}
}

func TestPyroWaveNeedsCapabilities(t *testing.T) {
	hostCert, clientCert := identities(t)
	scfg := pinServerConfig(hostCert)
	scfg.Negotiate = func(r wire.SessionConfig) (wire.SessionConfig, error) { r.Codec = wire.CodecPyroWave; return r, nil }
	sv := startServer(t, scfg)
	go sv.Accept(t.Context())
	_, err := Dial(t.Context(), sv.Addr().String(), ClientConfig{Certificate: clientCert, Name: "mac",
		VerifyHost: PinnedHost(CertFingerprint(hostCert)), Auth: wire.AuthPIN, PIN: testPIN,
		Session: wire.SessionConfig{Codec: wire.CodecHEVC, Width: 640, Height: 360, FPS: 60}})
	if err == nil {
		t.Fatal("a PyroWave session came up without capabilities")
	}
}

// TestIntraPartialDelivery feeds an intra session's receive path directly:
// frame 1 loses a chunk and is handed on with it zeroed once frame 2
// starts, without a PLI; its late shard does not deliver it again.
func TestIntraPartialDelivery(t *testing.T) {
	const shard = 512
	var frames [][]byte
	s := &Session{txBuf: make([]byte, 1500), ctrlOut: make(chan wire.Message, 8), done: make(chan struct{}),
		h: Handlers{VideoFrame: func(_ wire.VideoInfo, f []byte) { frames = append(frames, bytes.Clone(f)) }}}
	s.intra.Store(true)
	s.dec = fec.NewDecoder(shard, s.onFrameFinal)
	enc := fec.NewEncoder(shard, 0, 0)
	frame := func(id uint32) []byte {
		b := make([]byte, 6*shard)
		for i := range b {
			b[i] = byte(id) + byte(i/shard) + 1
		}
		return b
	}
	var late []byte
	for id := uint32(1); id <= 2+fec.ReassemblyWindow; id++ {
		var pkts [][]byte
		info := wire.VideoInfo{FrameID: id, SliceCount: 1}
		if _, err := enc.EncodeSlice(info, 0, frame(id), fec.Fixed(0), func(p []byte) error {
			pkts = append(pkts, bytes.Clone(p))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for i, p := range pkts {
			if id == 1 && i == 2 {
				late = p
				continue
			}
			s.handleVideo(p, Mono())
		}
		if id == 2 {
			s.handleVideo(late, Mono())
		}
	}
	if len(frames) < 2 {
		t.Fatalf("%d frames delivered", len(frames))
	}
	want := frame(1)
	clear(want[2*shard : 3*shard])
	if !bytes.Equal(frames[0], want) || !bytes.Equal(frames[1], frame(2)) {
		t.Fatal("frame 1 not delivered partially, or frame 2 not whole after it")
	}
	if len(frames) != 1+fec.ReassemblyWindow+1 {
		t.Fatalf("%d frames delivered, want %d (the late shard must not deliver frame 1 again)", len(frames), 2+fec.ReassemblyWindow)
	}
	if st := s.Stats(); st.FramesPartial != 1 || st.PLIsSent != 0 || len(s.ctrlOut) != 0 {
		t.Fatalf("stats %+v, %d control messages queued", st, len(s.ctrlOut))
	}
}

// TestSetStream: a PyroWave session moved to HEVC mid-session loses its
// intra delivery, its rate controller takes the new cap, and frames carry
// the new epoch to the client.
func TestSetStream(t *testing.T) {
	epochs := make(chan uint8, 8)
	p := newPair(t, pairOpts{
		server: func(c *ServerConfig) {
			c.Capabilities = func() []byte { return []byte(`{}`) }
			c.Resolve = func(r ResolveRequest) (ResolveResult, error) {
				cfg := r.Request
				cfg.Codec, cfg.Slices, cfg.BitrateKbps = wire.CodecPyroWave, 1, 400_000
				return ResolveResult{Config: cfg, Profile: []byte(`{"codec":4}`), StartKbps: 300_000, MaxKbps: 400_000}, nil
			}
		},
		client: func(c *ClientConfig) {
			c.Capabilities = []byte(`{"v":1}`)
			c.Handlers.VideoFrame = func(info wire.VideoInfo, _ []byte) { epochs <- info.Epoch }
		},
	})
	hevc := wire.SessionConfig{Codec: wire.CodecHEVC, Chroma: wire.Chroma420, Width: 5120, Height: 1440, FPS: 120, BitrateKbps: 80_000, Slices: 2}
	p.host.SetStream(hevc, 0, 150_000, ParityProfile{MinPercent: 10})
	p.client.SetStream(hevc, 0, 0, ParityProfile{})
	if p.client.intra.Load() || p.host.Config() != hevc || p.client.Config() != hevc {
		t.Fatalf("after SetStream: client intra %v, host config %+v", p.client.intra.Load(), p.host.Config())
	}
	if k := p.host.TargetBitrateKbps(); k != 80_000 {
		t.Fatalf("target %d kbps, want the HEVC cap 80000", k)
	}
	if n := p.host.pol.normal.Parity(10); n < 1 {
		t.Fatalf("normal parity %d for 10 shards, want the 10%% floor", n)
	}
	if err := p.host.SendVideoFrame(wire.VideoInfo{FrameID: 1, Keyframe: true, Epoch: 1}, make([]byte, 4000)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-epochs:
		if e != 1 {
			t.Fatalf("client saw epoch %d, want 1", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame reached the client")
	}
	p.host.SetStream(wire.SessionConfig{Codec: wire.CodecPyroWave, Width: 1280, Height: 768, FPS: 120, BitrateKbps: 400_000}, 300_000, 400_000, ParityProfile{})
	if k := p.host.TargetBitrateKbps(); k != 300_000 {
		t.Fatalf("back on PyroWave: target %d kbps, want its start 300000", k)
	}
}
