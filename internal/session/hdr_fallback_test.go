package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// sdrSource delivers 8-bit BGRA frames at fps, as DXGI duplication does
// for an HDR desktop it cannot capture 10-bit.
type sdrSource struct {
	w, h, fps int
	next      int64
}

func (s *sdrSource) Next(time.Duration) (capture.Frame, error) {
	if s.next == 0 {
		s.next = transport.Mono()
	}
	transport.SleepUntilMono(s.next)
	s.next += int64(time.Second) / int64(s.fps)
	return capture.Frame{Width: s.w, Height: s.h, Format: capture.FormatBGRA8}, nil
}
func (s *sdrSource) Release() error { return nil }
func (s *sdrSource) Close() error   { return nil }

// encoderLog records the config of every encoder opened.
type encoderLog struct {
	mu   sync.Mutex
	cfgs []encode.Config
}

func (l *encoderLog) open(cfg encode.Config, _ ...gpu.Device) (encode.Encoder, error) {
	l.mu.Lock()
	l.cfgs = append(l.cfgs, cfg)
	l.mu.Unlock()
	return &frameEncoder{}, nil
}

func (l *encoderLog) all() []encode.Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]encode.Config(nil), l.cfgs...)
}

type frameEncoder struct{ n uint32 }

func (e *frameEncoder) Caps() encode.Caps { return encode.Caps{} }
func (e *frameEncoder) Encode(_ capture.Frame, key bool, dst []byte) (encode.Packet, error) {
	pkt := encode.Packet{Data: frameBytes(dst, e.n, key), Keyframe: key, PTS: int64(e.n)}
	e.n++
	return pkt, nil
}
func (e *frameEncoder) SetBitrate(int) error { return nil }
func (e *frameEncoder) Close() error         { return nil }

// TestHDR10CaptureFallsBackToSDR: a stream the profile settles as HDR10,
// on an HDR display whose capture delivers 8-bit BGRA, encodes SDR, and
// the client hears of it before the first frame: its sink opens SDR, its
// profile says SDR and why, and no sink presents these frames as PQ. It
// holds for a stream that starts on the display and one switched to it.
func TestHDR10CaptureFallsBackToSDR(t *testing.T) {
	encs := &encoderLog{}
	var mu sync.Mutex
	var pipes []VideoParams
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "hdr-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: twoDisplayCaps(false), Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			mu.Lock()
			pipes = append(pipes, p)
			mu.Unlock()
			src := &sdrSource{w: p.Width, h: p.Height, fps: p.FPS}
			return &sourcePipe{src: src, params: p, openEnc: encs.open, info: VideoInfo{Width: p.Width, Height: p.Height, FPS: p.FPS, Source: "sdr"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	caps := testClientCaps()
	caps.Display.HDR, caps.PresentHDR10 = true, true
	connect := func(o defaults.UserOverrides) (*Client, *hdrSinks) {
		t.Helper()
		sinks := &hdrSinks{}
		c, err := NewClient(ClientConfig{
			ConfigDir: t.TempDir(), Name: "hdr-client", PIN: h.PIN(), NoAudio: true, Overrides: o,
			Capabilities: func() defaults.ClientCapabilities { return caps },
			OpenSink:     sinks.opener, OpenAudioPlayer: func() (audio.Player, error) { return &fakePlayer{}, nil },
			Tailscale: noTailscale{}, Logf: t.Logf,
			ViewSize: func() (int, int) { return 1600, 900 },
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
		return c, sinks
	}
	check := func(name string, c *Client, sinks *hdrSinks) {
		t.Helper()
		n := sinks.units(false)
		waitFor(t, name+": frames of the SDR stream", func() bool { return sinks.units(false) >= n+5 })
		mu.Lock()
		p := pipes[len(pipes)-1]
		mu.Unlock()
		if !p.HDR10 || p.Display != display2 {
			t.Fatalf("%s: pipeline %+v, want the HDR10 the profile settled on display 2", name, p)
		}
		es := encs.all()
		if e := es[len(es)-1]; e.HDR10 || e.Source != capture.FormatBGRA8 {
			t.Errorf("%s: encoder %+v, want SDR from BGRA8", name, e)
		}
		if u := sinks.units(true); u > 0 {
			t.Errorf("%s: %d SDR frames shown on an HDR10 sink", name, u)
		}
		prof := c.Stats().Profile
		if prof == nil || prof.HDR10 || decisionWhy(prof, "hdr10") != hdrCaptured8Bit {
			t.Errorf("%s: client profile HDR10 %v, why %q", name, prof != nil && prof.HDR10, decisionWhy(prof, "hdr10"))
		}
	}

	c, sinks := connect(defaults.UserOverrides{})
	waitFor(t, "frames", func() bool { return sinks.units(false) >= 3 })
	if err := c.SwitchDisplay(display2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stream on display 2", func() bool { return c.StreamedDisplay() == display2 })
	check("switched", c, sinks)
	c.Disconnect()

	c, sinks = connect(defaults.UserOverrides{Display: display2})
	check("started", c, sinks)
}

// TestHDR10FallbackBeforeRouteLive: the host's SDR stream can reach the
// client before its connect makes the route live (here held there until
// it has). The client still follows it: its first sink opens SDR, and it
// shows the SDR frames, none as PQ.
func TestHDR10FallbackBeforeRouteLive(t *testing.T) {
	settled := make(chan struct{})
	var once sync.Once
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "hdr-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: twoDisplayCaps(false),
		Logf: func(f string, a ...any) {
			if line := fmt.Sprintf(f, a...); strings.Contains(line, "as the encoder settled") {
				once.Do(func() { close(settled) })
			}
			t.Logf(f, a...)
		},
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			src := &sdrSource{w: p.Width, h: p.Height, fps: p.FPS}
			return &sourcePipe{src: src, params: p, openEnc: (&encoderLog{}).open, info: VideoInfo{Width: p.Width, Height: p.Height, FPS: p.FPS, Source: "sdr"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	caps := testClientCaps()
	caps.Display.HDR, caps.PresentHDR10 = true, true
	sinks := &hdrSinks{}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "hdr-client", PIN: h.PIN(), NoAudio: true, Overrides: defaults.UserOverrides{Display: display2},
		Capabilities: func() defaults.ClientCapabilities { return caps },
		OpenSink:     sinks.opener, OpenAudioPlayer: func() (audio.Player, error) { return &fakePlayer{}, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
		ViewSize: func() (int, int) { return 1600, 900 },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	c.beforeLive = func(cc *conn, rt *route) {
		select {
		case <-settled:
		case <-time.After(10 * time.Second):
			t.Error("the host never settled SDR")
			return
		}
		// The host answers a StatsReport on the control stream, after the
		// Stream it already sent there.
		sentinel := wire.HostStats{CaptureMicros: ^uint32(0), EncodeMicros: ^uint32(0)}
		cc.mu.Lock()
		cc.host = sentinel
		cc.mu.Unlock()
		if err := rt.sess.SendControl(&wire.StatsReport{}); err != nil {
			t.Error(err)
			return
		}
		waitFor(t, "the host's stats", func() bool {
			cc.mu.Lock()
			defer cc.mu.Unlock()
			return cc.host != sentinel
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames of the SDR stream", func() bool { return sinks.units(false) >= 5 })
	if u := sinks.units(true); u > 0 {
		t.Errorf("%d SDR frames shown on an HDR10 sink", u)
	}
	if prof := c.Stats().Profile; prof == nil || prof.HDR10 {
		t.Errorf("client profile HDR10 %v, want SDR", prof != nil && prof.HDR10)
	}
}

// hdrSinks counts the frames shown on SDR and on HDR10 sinks.
type hdrSinks struct {
	mu       sync.Mutex
	sdr, hdr uint64
}

type hdrSink struct {
	s   *hdrSinks
	hdr bool
}

func (s *hdrSinks) opener(cfg decode.Config) (Sink, error) {
	return &hdrSink{s: s, hdr: cfg.HDR10}, nil
}

func (s *hdrSinks) units(hdr bool) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hdr {
		return s.hdr
	}
	return s.sdr
}

func (k *hdrSink) Submit(au []byte, pts int64) (player.Timing, error) {
	k.s.mu.Lock()
	if k.hdr {
		k.s.hdr++
	} else {
		k.s.sdr++
	}
	k.s.mu.Unlock()
	return player.Timing{PTS: pts}, nil
}

func (k *hdrSink) Close() error { return nil }
