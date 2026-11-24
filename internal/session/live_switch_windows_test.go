//go:build windows

package session

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/vdd"
)

// TestLiveCodecSwitch streams a real SudoVDA display to a client on
// loopback, on a wired 2.5GbE LAN as the resolver sees it, with a
// 5120x1440 display: a small window streams PyroWave, the full display
// HEVC, and back, in one session, each decoded on the GPU at its size,
// with one display throughout and none left after. Run it with
// PLANCK_LIVE_VDD_TEST=1 [PLANCK_LIVE_VDD_GPU=4080]; it uses ports 41110
// and 41111.
func TestLiveCodecSwitch(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_VDD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_VDD_TEST=1 to add a real SudoVDA display")
	}
	gpuName := os.Getenv("PLANCK_LIVE_VDD_GPU")
	if gpuName == "" {
		gpuName = "4080"
	}
	wiredLAN(t)
	before, err := vdd.Monitors()
	if err != nil {
		t.Fatal(err)
	}
	const addr = "127.0.0.1:41110"
	h, err := NewHost(HostConfig{ConfigDir: t.TempDir(), Name: "live-switch-host", ListenAddr: addr, MediaPort: 41111,
		DiscoveryAddr: "off", Adapter: gpuName, NoAudio: true, NoTailscale: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	var view [2]int
	var viewMu sync.Mutex
	setView := func(w, hh int) { viewMu.Lock(); view = [2]int{w, hh}; viewMu.Unlock() }
	setView(1280, 768)
	caps := ProbeClientCapabilities()
	caps.Display = defaults.ClientDisplay{WidthPx: 5120, HeightPx: 1440, Scale: 1, MaxHz: 120, MinHz: 120}
	sink := &sizeSink{}
	var opened []decode.Config
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "live-switch-client", PIN: h.PIN(), NoAudio: true, Tailscale: noTailscale{},
		Capabilities: func() defaults.ClientCapabilities { return caps },
		ViewSize:     func() (int, int) { viewMu.Lock(); defer viewMu.Unlock(); return view[0], view[1] },
		OpenSink: func(cfg decode.Config) (Sink, error) {
			s, err := OpenDecodeSink(cfg)
			if err != nil {
				return nil, err
			}
			viewMu.Lock()
			opened = append(opened, cfg)
			viewMu.Unlock()
			sink.decodeSink = s.(*decodeSink)
			return sink, nil
		},
		OpenAudioPlayer: func() (audio.Player, error) { return nil, audio.ErrClosed },
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	waitLive := func(what string, limit time.Duration, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(limit); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %s after %v", what, limit)
			}
		}
	}
	for i, s := range []struct {
		w, h  int
		codec encode.Codec
	}{{1280, 768, encode.PyroWave}, {5120, 1440, encode.HEVC}, {1280, 768, encode.PyroWave}} {
		start := time.Now()
		if i > 0 {
			setView(s.w, s.h)
			if err := c.Resize(s.w, s.h); err != nil {
				t.Fatal(err)
			}
		}
		waitLive("host video in the new stream", 15*time.Second, func() bool {
			hs := h.Stats()
			return hs.VirtualDisplay != "" && hs.Video.Codec == s.codec && hs.Video.Width == s.w && hs.Video.Height == s.h
		})
		hs := h.Stats()
		x, y := displayOrigin(hs.VirtualDisplay)
		stop, done := make(chan struct{}), make(chan struct{})
		go func() { defer close(done); win.Animate(x+50, y+50, 96, stop) }()
		waitLive("a decoded picture of the new stream", 10*time.Second, func() bool { return sink.last.Load() == packView(s.w, s.h) })
		close(stop)
		<-done
		ms, err := vdd.Monitors()
		if err != nil {
			t.Fatal(err)
		}
		// A headless host keeps a SudoVDA monitor up between sessions, at
		// the last one's mode, and a new display takes its place.
		if len(ms) == 0 || len(ms) > len(before)+1 {
			t.Fatalf("%d SudoVDA monitors in %v %dx%d, %d before the test", len(ms), s.codec, s.w, s.h, len(before))
		}
		cs := c.Stats()
		if cs.Codec != s.codec.String() || cs.RouteSwitches != 0 {
			t.Fatalf("client on %s with %d route switches, want %v", cs.Codec, cs.RouteSwitches, s.codec)
		}
		t.Logf("%v %dx%d on %s decoded %v after the request; sink errors %d, frames dropped %d", s.codec, s.w, s.h,
			hs.VirtualDisplay, time.Since(start), cs.SinkErrors, cs.FramesDropped)
	}
	viewMu.Lock()
	defer viewMu.Unlock()
	if len(opened) != 3 {
		t.Fatalf("decoders opened: %+v, want PyroWave, HEVC, PyroWave", opened)
	}
	c.Disconnect()
	waitLive("removal of the virtual display", 6*time.Second, func() bool {
		ms, err := vdd.Monitors()
		return err == nil && len(ms) <= len(before)
	})
}
