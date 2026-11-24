package transport

import (
	"bytes"
	"errors"
	"testing"

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
	if string(p.client.Profile()) != `{"codec":4}` || p.client.Config().Codec != wire.CodecPyroWave || !p.client.intra {
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
	s := &Session{intra: true, txBuf: make([]byte, 1500), ctrlOut: make(chan wire.Message, 8), done: make(chan struct{}),
		h: Handlers{VideoFrame: func(_ wire.VideoInfo, f []byte) { frames = append(frames, bytes.Clone(f)) }}}
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
