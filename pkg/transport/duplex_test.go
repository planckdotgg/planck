package transport

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

func TestMicFlowReachesHostOnly(t *testing.T) {
	mics := make(chan wire.AudioHeader, 4)
	desktop := make(chan wire.AudioHeader, 4)
	p := newPair(t, pairOpts{
		server: func(c *ServerConfig) {
			c.Handlers.Mic = func(h wire.AudioHeader, payload []byte) {
				if string(payload) == "pcm!" {
					mics <- h
				}
			}
		},
		client: func(c *ClientConfig) {
			c.Handlers.Audio = func(h wire.AudioHeader, _ []byte) { desktop <- h }
		},
	})
	if p.client.PeerFeatures()&wire.FeatureMic == 0 || p.host.PeerFeatures()&wire.FeaturePeerState == 0 {
		t.Fatalf("features: client sees %#x, host sees %#x", p.client.PeerFeatures(), p.host.PeerFeatures())
	}
	if err := p.client.SendMic(wire.AudioHeader{Codec: wire.AudioPCM16, FrameQuarterMs: 20, Seq: 3}, []byte("pcm!")); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-mics:
		if h.Flow != wire.FlowMic || h.Codec != wire.AudioPCM16 || h.FrameQuarterMs != 20 || h.Seq != 3 {
			t.Fatalf("host got %+v", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mic packet did not reach the host")
	}
	if err := p.host.SendAudio(1, 0, []byte("opus")); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-desktop:
		if h.Flow != wire.FlowDesktop {
			t.Fatalf("client got flow %d", h.Flow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("desktop audio did not reach the client")
	}
	if err := p.host.SendMic(wire.AudioHeader{}, nil); !errors.Is(err, ErrWrongRole) {
		t.Fatalf("host SendMic: %v", err)
	}
}

func TestPeerStateIsKept(t *testing.T) {
	p := newPair(t, pairOpts{})
	want := wire.PeerState{ClipboardRecv: true, MicHint: "none"}
	if err := p.client.SendControl(&want); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.host.PeerState() != want {
		if time.Now().After(deadline) {
			t.Fatalf("host has %+v", p.host.PeerState())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSendMicNeedsHostSupport(t *testing.T) {
	p := newPair(t, pairOpts{})
	if err := p.client.SendMic(wire.AudioHeader{}, []byte{1}); !errors.Is(err, ErrPeerUnsupported) {
		t.Fatalf("SendMic to a host without a mic handler: %v", err)
	}
}

func TestClipboardStreamBothWays(t *testing.T) {
	toHost := make(chan *wire.Clip, 4)
	toClient := make(chan *wire.Clip, 4)
	p := newPair(t, pairOpts{
		server: func(c *ServerConfig) { c.Handlers.Clipboard = func(c *wire.Clip) { toHost <- c } },
		client: func(c *ClientConfig) { c.Handlers.Clipboard = func(c *wire.Clip) { toClient <- c } },
	})
	ctx := context.Background()
	origin := [16]byte{9}
	img := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1<<20) // 4 MiB
	if err := p.client.SendClipboard(ctx, wire.NewClip(wire.ClipPNG, origin, img)); err != nil {
		t.Fatal(err)
	}
	if err := p.client.SendClipboard(ctx, wire.NewClip(wire.ClipText, origin, []byte("second"))); err != nil {
		t.Fatal(err)
	}
	if err := p.host.SendClipboard(ctx, wire.NewClip(wire.ClipText, [16]byte{1}, []byte("from host"))); err != nil {
		t.Fatal(err)
	}
	recv := func(ch chan *wire.Clip) *wire.Clip {
		select {
		case c := <-ch:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("clipboard item did not arrive")
			return nil
		}
	}
	if c := recv(toHost); c.Format != wire.ClipPNG || !bytes.Equal(c.Data, img) || c.Origin != origin {
		t.Fatalf("host got format %d, %d bytes", c.Format, len(c.Data))
	}
	if c := recv(toHost); string(c.Data) != "second" {
		t.Fatalf("host got %q", c.Data)
	}
	if c := recv(toClient); string(c.Data) != "from host" {
		t.Fatalf("client got %q", c.Data)
	}
	over := &wire.Clip{Format: wire.ClipPNG, Data: make([]byte, wire.MaxClip+1)}
	if err := p.client.SendClipboard(ctx, over); !errors.Is(err, wire.ErrClipTooLarge) {
		t.Fatalf("17 MiB item: %v", err)
	}
	if st := p.client.Stats(); st.ClipboardSent != 2 {
		t.Fatalf("client sent %d items", st.ClipboardSent)
	}
}

func TestClipboardNeedsPeerSupport(t *testing.T) {
	p := newPair(t, pairOpts{client: func(c *ClientConfig) { c.Handlers.Clipboard = func(*wire.Clip) {} }})
	if err := p.client.SendClipboard(context.Background(), wire.NewClip(wire.ClipText, [16]byte{}, []byte("x"))); !errors.Is(err, ErrPeerUnsupported) {
		t.Fatalf("send to a host without a clipboard handler: %v", err)
	}
	if p.host.PeerFeatures()&wire.FeatureClipboard == 0 {
		t.Fatal("client with a clipboard handler did not offer FeatureClipboard")
	}
}
