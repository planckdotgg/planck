package session

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

var testGPU = gpu.Adapter{LUID: 7, Vendor: gpu.VendorNVIDIA, Name: "NVIDIA GeForce RTX 4080"}

func testHostCaps() defaults.HostCapabilities {
	return defaults.HostCapabilities{
		OS: "windows",
		Encoders: []defaults.EncoderCap{
			{Adapter: testGPU, API: defaults.APINVENC, Engines: 2, SubFrame: true, DrivesDisplay: true,
				Codecs: []defaults.CodecCap{{Codec: defaults.CodecH264}, {Codec: defaults.CodecHEVC, TenBit: true}}},
			{Adapter: testGPU, API: defaults.APIPyroWave, Engines: 1, DrivesDisplay: true, MaxPixelRate: 1.4e9,
				Codecs: []defaults.CodecCap{{Codec: defaults.CodecPyroWave, TenBit: true, Chroma444: true}}},
		},
		Displays: []defaults.HostDisplay{{ID: `\\.\DISPLAY1`, Width: 1920, Height: 1080, RefreshHz: 120, Adapter: testGPU.LUID, Primary: true}},
	}
}

func testClientCaps() defaults.ClientCapabilities {
	return defaults.ClientCapabilities{OS: "darwin", Display: defaults.ClientDisplay{WidthPx: 1920, HeightPx: 1080, Scale: 1, MaxHz: 120, MinHz: 24},
		Decoders: []defaults.CodecCap{{Codec: defaults.CodecHEVC, TenBit: true}, {Codec: defaults.CodecPyroWave, Chroma444: true}}}
}

// chunkPipe stands in for the PyroWave encoder: frames of whole chunks in
// three slices, the first critical, in fakePipe's checkable pattern.
type chunkPipe struct {
	fakePipe
	chunk int
	ends  []int
}

func (p *chunkPipe) Next(timeout time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	pkt, captured, err := p.fakePipe.Next(timeout, forceKey, dst)
	if err != nil {
		return pkt, captured, err
	}
	n := p.n - 1
	size := (len(pkt.Data) + 3*p.chunk - 1) / (3 * p.chunk) * 3 * p.chunk
	data := pkt.Data[:0]
	for i := range size {
		data = append(data, byte(int(n)*31+i))
	}
	binary.BigEndian.PutUint32(data, n)
	p.ends = append(p.ends[:0], size/3, 2*size/3, size)
	return encode.Packet{Data: data, Keyframe: true, SliceEnds: p.ends, Critical: 1, PTS: pkt.PTS}, captured, nil
}

func startCapsHost(t *testing.T, open func(VideoParams) (VideoPipe, error)) *Host {
	t.Helper()
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, OpenVideo: open, Capabilities: testHostCaps, Logf: t.Logf,
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

func capsClient(t *testing.T, pin string, o defaults.UserOverrides) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	r.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			r.corrupt.Add(1)
		}
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "test-client", PIN: pin, NoAudio: true, Overrides: o, Capabilities: testClientCaps,
		OpenSink: NullSinkOpener(r.sink), OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

func TestProfileDrivesPipeline(t *testing.T) {
	var mu sync.Mutex
	var got VideoParams
	var forced, kbps atomic.Int64
	h := startCapsHost(t, func(p VideoParams) (VideoPipe, error) {
		mu.Lock()
		got = p
		mu.Unlock()
		return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
	})
	r := capsClient(t, h.PIN(), defaults.UserOverrides{})
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 30 })
	mu.Lock()
	p := got
	mu.Unlock()
	if p.Codec != encode.HEVC || p.Chroma != encode.P010 || p.Adapter != testGPU.Name || p.Width != 1920 || p.Height != 1080 || p.FPS != 120 {
		t.Fatalf("pipeline params %+v: want HEVC Main10 1080p120 on the 4080", p)
	}
	cs := r.client.Stats()
	if cs.Profile == nil || cs.Profile.EncoderAPI != defaults.APINVENC || r.corrupt.Load() != 0 {
		t.Fatalf("client profile %+v, %d corrupt", cs.Profile, r.corrupt.Load())
	}
	if hs := h.Stats(); hs.Profile == nil || hs.Profile.Codec != defaults.CodecHEVC {
		t.Fatalf("host profile %+v", hs.Profile)
	}
}

func TestPyroWaveSession(t *testing.T) {
	var forced, kbps atomic.Int64
	var params atomic.Pointer[VideoParams]
	h := startCapsHost(t, func(p VideoParams) (VideoPipe, error) {
		params.Store(&p)
		return &chunkPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, chunk: p.ChunkLen}, nil
	})
	r := capsClient(t, h.PIN(), defaults.UserOverrides{Codec: "pyrowave"})
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 60 })
	p := params.Load()
	r.client.mu.Lock()
	shard, intra := r.client.conn.session().ShardSize(), r.client.conn.intra.Load()
	r.client.mu.Unlock()
	if p.Codec != encode.PyroWave || p.ChunkLen != shard || p.Slices != 0 || !intra {
		t.Fatalf("params %+v shard %d intra %v", *p, shard, intra)
	}
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d PyroWave frames arrived corrupt", n)
	}
	if cs := r.client.Stats(); cs.Codec != "PyroWave" || cs.FramesLost != 0 || cs.FramesPartial != 0 {
		t.Fatalf("client stats %+v", cs)
	}
}
