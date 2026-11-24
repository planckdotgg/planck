package session

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/wire"
)

// switchSinks records every sink a client opened and how many are open.
type switchSinks struct {
	sink      *NullSink
	misrouted atomic.Int64
	chunkLen  atomic.Int64
	mu   sync.Mutex
	cfgs []decode.Config
	open int
}

type countedSink struct {
	*NullSink
	s    *switchSinks
	cfg  decode.Config
	once sync.Once
}

// Submit counts a unit of the other stream's encoder as a misroute:
// chunkPipe frames are whole triples of chunks, fakePipe's are not.
func (c *countedSink) Submit(au []byte, pts int64) (player.Timing, error) {
	pyro := len(au)%(3*c.s.chunk(c.cfg)) == 0
	if pyro != (c.cfg.Codec == encode.PyroWave) {
		c.s.misrouted.Add(1)
	}
	return c.NullSink.Submit(au, pts)
}

func (c *countedSink) Close() error {
	c.once.Do(func() {
		c.s.mu.Lock()
		c.s.open--
		c.s.mu.Unlock()
	})
	return nil
}

func (s *switchSinks) opener(cfg decode.Config) (Sink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfgs = append(s.cfgs, cfg)
	s.open++
	if cfg.ChunkLen > 0 {
		s.chunkLen.Store(int64(cfg.ChunkLen))
	}
	return &countedSink{NullSink: s.sink, s: s, cfg: cfg}, nil
}

func (s *switchSinks) chunk(decode.Config) int { return int(max(s.chunkLen.Load(), 1)) }

func (s *switchSinks) state() (last decode.Config, opened, open int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfgs[len(s.cfgs)-1], len(s.cfgs), s.open
}

// wiredLAN makes every route a wired 2.5GbE link, as Chris's Mac and
// bigboi have, so PyroWave is eligible on small windows and not on a
// 5120x1440 one.
func wiredLAN(t *testing.T) {
	old := routeLink
	routeLink = func(netip.Addr) (string, defaults.Link, error) {
		return "en7", defaults.Link{Wired: true, SpeedMbps: 2500}, nil
	}
	t.Cleanup(func() { routeLink = old })
}

func ultrawideClientCaps() defaults.ClientCapabilities {
	c := testClientCaps()
	c.Display = defaults.ClientDisplay{WidthPx: 5120, HeightPx: 1440, Scale: 1, MaxHz: 120, MinHz: 120}
	return c
}

func startSwitchHost(t *testing.T, vdd *fakeVDD, pipes *pipeLog) *Host {
	t.Helper()
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "switch-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: vddHostCaps, OpenVirtualDisplay: vdd.open, Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			pipes.mu.Lock()
			pipes.opens = append(pipes.opens, p)
			pipes.mu.Unlock()
			fp := fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}
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

// TestResizeSwitchesCodec: a window that starts small streams PyroWave;
// going full screen on a 5120x1440 display moves the same session to
// HEVC, on the same virtual display, with a new decoder; shrinking moves
// it back to PyroWave, and a PyroWave resize opens a decoder of the new
// size. No frame of one stream reaches the other's decoder, and the
// display goes with the session.
func TestResizeSwitchesCodec(t *testing.T) {
	wiredLAN(t)
	vdd, pipes := &fakeVDD{}, &pipeLog{}
	h := startSwitchHost(t, vdd, pipes)
	sinks := &switchSinks{sink: &NullSink{}}
	var corrupt atomic.Int64
	sinks.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			corrupt.Add(1)
		}
	}
	var view atomic.Uint64
	view.Store(packView(1280, 768))
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "switch-client", PIN: h.PIN(), NoAudio: true, Capabilities: ultrawideClientCaps,
		OpenSink: sinks.opener, OpenAudioPlayer: func() (audio.Player, error) { return &fakePlayer{}, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
		ViewSize: func() (int, int) { v := view.Load(); return int(v >> 32), int(uint32(v)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	sessID := c.conn.session().ID()
	c.mu.Unlock()
	units := func() uint64 { return sinks.sink.Units.Load() }
	waitFor(t, "PyroWave frames", func() bool { return units() >= 10 })
	if p := c.Stats().Profile; p == nil || p.Codec != defaults.CodecPyroWave || p.Width != 1280 || p.Height != 768 {
		t.Fatalf("start: profile %+v, want PyroWave 1280x768", p)
	}

	step := func(w, hh int, codec defaults.Codec, wantW, wantH int, newSinks int) {
		t.Helper()
		_, before, _ := sinks.state()
		if err := c.Resize(w, hh); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the new profile", func() bool {
			p := c.Stats().Profile
			return p.Codec == codec && p.Width == wantW && p.Height == wantH
		})
		waitFor(t, "the pipeline", func() bool {
			p, _ := pipes.last()
			return p.Codec == encode.Codec(codec) && p.Width == wantW && p.Height == wantH
		})
		n := units()
		waitFor(t, "video of the new stream", func() bool { return units() >= n+10 })
		last, opened, open := sinks.state()
		if opened != before+newSinks || open != 1 || last.Codec != encode.Codec(codec) {
			t.Fatalf("sinks: %d opened (want %d), %d open, last %+v", opened, before+newSinks, open, last)
		}
		if codec == defaults.CodecPyroWave && (last.Width != wantW || last.Height != wantH || last.ChunkLen == 0) {
			t.Fatalf("PyroWave sink %+v, want %dx%d in chunks", last, wantW, wantH)
		}
		if live, made, size := vdd.state(); live != 1 || made != 1 || size != [2]int{wantW, wantH} {
			t.Fatalf("display: %d live of %d made, last %v", live, made, size)
		}
		if cs := c.Stats(); cs.Codec != encode.Codec(codec).String() || cs.Width != wantW || cs.RouteSwitches != 0 {
			t.Fatalf("client stats %s %dx%d, %d route switches", cs.Codec, cs.Width, cs.Height, cs.RouteSwitches)
		}
	}
	step(5120, 1440, defaults.CodecHEVC, 5120, 1440, 1)
	step(1600, 900, defaults.CodecPyroWave, 1600, 900, 1)
	step(1280, 720, defaults.CodecPyroWave, 1280, 720, 1)

	c.mu.Lock()
	same := c.conn != nil && c.conn.session().ID() == sessID
	c.mu.Unlock()
	if !same {
		t.Fatal("the stream switched sessions")
	}
	select {
	case <-c.Done():
		t.Fatalf("stream ended: %v", c.Err())
	default:
	}
	if n, m := corrupt.Load(), sinks.misrouted.Load(); n != 0 || m != 0 {
		t.Fatalf("%d corrupt units, %d units on a decoder of another stream", n, m)
	}
	c.Disconnect()
	waitFor(t, "the display removed", func() bool { live, _, _ := vdd.state(); return live == 0 })
	waitFor(t, "the sink closed", func() bool { _, _, open := sinks.state(); return open == 0 })
}

// TestStaleEpochFramesDropped: once a client moved to stream 2, frames of
// stream 1 still in flight, and of stream 3 before its announcement, are
// dropped without asking for a keyframe.
func TestStaleEpochFramesDropped(t *testing.T) {
	cc := &conn{frames: make(chan *frameBuf, 4), free: make(chan *frameBuf, 4)}
	cc.free <- &frameBuf{}
	cc.live.Store(&route{})
	cc.stream.Store(&streamState{epoch: 2})
	cc.onFrame(wire.VideoInfo{FrameID: 7, Keyframe: true, Epoch: 1}, []byte{1, 2, 3})
	cc.onFrame(wire.VideoInfo{FrameID: 8, Keyframe: true, Epoch: 3}, []byte{1, 2, 3})
	if len(cc.frames) != 0 || cc.dropped.Load() != 0 || cc.lastPLI.Load() != 0 {
		t.Fatalf("other streams' frames: %d queued, %d dropped, PLI at %d", len(cc.frames), cc.dropped.Load(), cc.lastPLI.Load())
	}
}
