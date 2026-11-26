package calibrate

import (
	"context"
	"encoding/binary"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
)

// loopPipe stands in for the synthetic pattern and its encoder: frames of
// a few KB at the profile's rate.
type loopPipe struct {
	fps  int
	n    uint32
	next int64
}

func (p *loopPipe) Next(timeout time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	if p.next == 0 {
		p.next = transport.Mono()
	}
	transport.SleepUntilMono(p.next)
	p.next += int64(time.Second) / int64(p.fps)
	size := 6000
	if forceKey {
		size = 30000
	}
	data := append(dst[:0], make([]byte, size)...)
	binary.BigEndian.PutUint32(data, p.n)
	p.n++
	return encode.Packet{Data: data, Keyframe: forceKey, PTS: int64(p.n)}, transport.Mono(), nil
}

func (p *loopPipe) SetBitrate(int) error       { return nil }
func (p *loopPipe) InvalidateRefs(int64) error { return encode.ErrUnsupported }
func (p *loopPipe) Info() session.VideoInfo {
	return session.VideoInfo{Width: 1920, Height: 1080, FPS: p.fps, Source: "loop"}
}
func (p *loopPipe) Close() error { return nil }

type noTailnet struct{}

func (noTailnet) PathInfo(context.Context, netip.Addr) (tailscale.PathInfo, error) {
	return tailscale.PathInfo{}, tailscale.ErrNotRunning
}

// TestRunOverLoopback calibrates a real host over loopback: probe trains,
// one stream per candidate, and the result kept for the route.
func TestRunOverLoopback(t *testing.T) {
	var mu sync.Mutex
	var synth []bool
	h, err := session.NewHost(session.HostConfig{
		ConfigDir: t.TempDir(), Name: "loop-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, PIN: "112233", NoAudio: true, Capabilities: hostCaps, Logf: t.Logf,
		OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) {
			mu.Lock()
			synth = append(synth, p.Synth)
			mu.Unlock()
			return &loopPipe{fps: max(p.FPS, 30)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	dir := t.TempDir()
	client := clientCaps()
	client.Decoders = client.Decoders[:2]
	d := &SessionDialer{
		Config: session.ClientConfig{ConfigDir: dir, Name: "loop-client", PIN: "112233", Tailscale: noTailnet{}, Logf: t.Logf,
			Capabilities: func() defaults.ClientCapabilities { return client }, OpenSink: session.NullSinkOpener(&session.NullSink{})},
		Target: session.Target{Addr: h.Addr().String()},
	}
	timing := Timing{Pings: 10, PingEvery: 2 * time.Millisecond, Settle: 5 * time.Millisecond, Bandwidth: 50 * time.Millisecond,
		Warmup: 3 * time.Second, Measure: 400 * time.Millisecond, DisplayMeasure: 400 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, d, Options{Client: client, Timing: &timing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Link.Route != "lan-direct" || res.Link.RTT.P50 <= 0 || res.Link.DatagramBytes == 0 || res.Link.BandwidthMbps <= 0 {
		t.Fatalf("link %+v", res.Link)
	}
	viable := 0
	for _, c := range res.Candidates {
		t.Logf("%-28s err %q frames %d total %.2f/%.2f ms score %.2f", c.Name, c.Err, c.Frames, c.Total.P50, c.Total.P99, c.Score)
		if c.Viable() {
			viable++
		}
	}
	if viable < 3 || res.Best < 0 {
		t.Fatalf("%d candidates streamed, best %d", viable, res.Best)
	}
	mu.Lock()
	for _, s := range synth {
		if !s {
			t.Fatal("a calibration stream captured the screen")
		}
	}
	mu.Unlock()
	if res.Display.Status != DisplaySkipped {
		t.Fatalf("display %+v", res.Display)
	}
	if err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
		return c.ApplyCalibration(res.HostKey(), res.Link.Route, res.Stored())
	}); err != nil {
		t.Fatal(err)
	}
	stored, _ := config.LoadClientConfig(dir)
	if got, ok := stored.Calibration(res.HostKey(), "lan-direct"); !ok || got.Overrides != res.Recommended {
		t.Fatalf("kept %+v, %v", got, ok)
	}
}
