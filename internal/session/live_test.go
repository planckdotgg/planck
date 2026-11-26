package session

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
)

// TestLiveSynthHardware streams the synthetic pattern through this
// machine's hardware encoder and decoder. It needs a GPU, so it runs only
// with PLANCK_LIVE_VIDEO_TEST=1; PLANCK_LIVE_ADAPTER picks the encoding GPU,
// PLANCK_LIVE_FPS the rate (120), PLANCK_LIVE_SECONDS the run (4) and
// PLANCK_LIVE_LOSS a fraction of the host's media datagrams to drop, to
// count what recovering from lost frames costs. It never captures the
// screen or injects input.
func TestLiveSynthHardware(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_VIDEO_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_VIDEO_TEST=1 to run on the GPU")
	}
	adapter := os.Getenv("PLANCK_LIVE_ADAPTER")
	fps, _ := strconv.Atoi(os.Getenv("PLANCK_LIVE_FPS"))
	if fps <= 0 {
		fps = 120
	}
	secs, _ := strconv.Atoi(os.Getenv("PLANCK_LIVE_SECONDS"))
	if secs <= 0 {
		secs = 4
	}
	loss, _ := strconv.ParseFloat(os.Getenv("PLANCK_LIVE_LOSS"), 64)
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "live-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		Synth: true, Adapter: adapter, Width: 1920, Height: 1080, FPS: fps, BitrateKbps: 40_000, Slices: 4,
		Codec: encode.HEVC, Chroma: encode.NV12, NoAudio: true, NoTailscale: true, Logf: t.Logf, SimulateLoss: loss,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	var sink Sink
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "live-client", PIN: h.PIN(), NoAudio: true, Tailscale: noTailscale{},
		Quality: Quality{FPS: fps},
		OpenSink: func(cfg decode.Config) (Sink, error) {
			s, err := OpenDecodeSink(cfg)
			sink = s
			return s, err
		},
		OpenAudioPlayer: func() (audio.Player, error) { return nil, audio.ErrClosed },
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	time.Sleep(time.Duration(secs) * time.Second)
	cs, hs := c.Stats(), h.Stats()
	t.Logf("host: %s on %s (power %s) %.0f fps encode p50 %.2f p99 %.2f ms %.0f kbps",
		hs.Video.Source, hs.Video.Adapter.Name, hs.Video.Power, hs.FPS, hs.EncodeMs.P50, hs.EncodeMs.P99, hs.SentKbps)
	t.Logf("client: %.0f fps decode %.2f ms e2e p50/p95/p99 %.2f/%.2f/%.2f ms (%d samples) sink errors %d",
		cs.FPS, cs.DecodeMs, cs.E2E.P50, cs.E2E.P95, cs.E2E.P99, cs.E2E.Samples, cs.SinkErrors)
	t.Logf("client: %d frames lost, %d dropped, %d keyframes", cs.FramesLost, cs.FramesDropped, cs.Keyframes)
	if sink == nil || cs.SinkErrors != 0 || cs.FPS < 60 || cs.E2E.Samples < 100 || cs.DecodeMs <= 0 {
		t.Fatalf("hardware loopback unhealthy: %+v", cs)
	}
}
