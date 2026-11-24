package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/discovery"
)

// TestHostThisMachineWhileBrowsing: the client browses for LAN beacons on
// the discovery port, and hosting from the same app answers on that port
// too, on and off and on again, without "address already in use".
func TestHostThisMachineWhileBrowsing(t *testing.T) {
	port := freeUDPPort(t)
	addr := fmt.Sprintf(":%d", port)
	f := newFinder(addr, nil, nil, t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.run(ctx)
		close(done)
	}()
	b, err := newBackend(backendConfig{
		dir: t.TempDir(), name: "test-client", finder: f, tailscale: noTailnet{}, openSink: (&countSink{}).open(), logf: t.Logf,
		encoders: []ui.EncoderInfo{{GPU: "Test GPU", API: "Fake"}},
		hostConfig: func(s ui.Settings) session.HostConfig {
			return session.HostConfig{
				ConfigDir: t.TempDir(), Name: "hosting", ListenAddr: "127.0.0.1:0", DiscoveryAddr: addr, FPS: 60, NoTailscale: true,
				OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
				OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil }, Logf: t.Logf,
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the finder to bind the discovery port", func() bool {
		c, err := net.ListenPacket("udp4", addr)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
	at := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))
	for i := range 3 {
		if err := b.SetHosting(context.Background(), true); err != nil {
			t.Fatalf("hosting, round %d: %v", i, err)
		}
		if h := b.Hosting(); !h.On || h.Err != "" {
			t.Fatalf("round %d: hosting state %+v", i, h)
		}
		pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
		beacon, _, err := discovery.Probe(pctx, at)
		pcancel()
		if err != nil || beacon.Name != "hosting" {
			t.Fatalf("round %d: probe the hosting app: %+v, %v", i, beacon, err)
		}
		if err := b.SetHosting(context.Background(), false); err != nil {
			t.Fatalf("stop hosting, round %d: %v", i, err)
		}
	}
	b.close()
	cancel()
	<-done
	c, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatalf("the discovery port is still held after both let go: %v", err)
	}
	c.Close()
}

// TestHostingPortHeldElsewhere: a port another app holds alone fails
// hosting with a sentence, not a socket error.
func TestHostingPortHeldElsewhere(t *testing.T) {
	port := freeUDPPort(t)
	held, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	b, err := newBackend(backendConfig{
		dir: t.TempDir(), name: "test-client", finder: newFinder("", nil, nil, t.Logf), tailscale: noTailnet{}, logf: t.Logf,
		hostConfig: func(s ui.Settings) session.HostConfig {
			return session.HostConfig{
				ConfigDir: t.TempDir(), Name: "hosting", ListenAddr: "127.0.0.1:0", DiscoveryAddr: fmt.Sprintf(":%d", port), FPS: 60, NoTailscale: true,
				OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
				OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil }, Logf: t.Logf,
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err := b.SetHosting(context.Background(), true); err == nil {
		t.Fatal("hosted on a port another app holds alone")
	}
	want := fmt.Sprintf("Another app is using UDP port %d.", port)
	if h := b.Hosting(); h.On || h.Err != want {
		t.Fatalf("hosting state %+v, want the error %q", h, want)
	}
}
