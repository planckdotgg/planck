package session

import (
	"bytes"
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/clipboard"
	"github.com/planckdotgg/planck/pkg/encode"
)

// memBoard is a clipboard in memory. Like a real one, it reports Planck's
// own writes to its watcher too, which the guard must not send back.
type memBoard struct {
	mu     sync.Mutex
	item   clipboard.Item
	watch  func(clipboard.Item)
	writes []clipboard.Item
}

func (b *memBoard) Watch(ctx context.Context, fn func(clipboard.Item)) error {
	b.mu.Lock()
	b.watch = fn
	b.mu.Unlock()
	return nil
}

func (b *memBoard) Read() (clipboard.Item, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.item, b.item.Format != 0, nil
}

func (b *memBoard) Write(it clipboard.Item) error {
	b.mu.Lock()
	b.writes = append(b.writes, it)
	b.mu.Unlock()
	b.copy(it)
	return nil
}

// copy is a user copying it in another app.
func (b *memBoard) copy(it clipboard.Item) {
	b.mu.Lock()
	b.item = it
	fn := b.watch
	b.mu.Unlock()
	if fn != nil {
		fn(it)
	}
}

func (b *memBoard) written() []clipboard.Item {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]clipboard.Item(nil), b.writes...)
}

func (b *memBoard) Close() error { return nil }

// fakeMic records a 440 Hz voice in 10 ms chunks.
type fakeMic struct{ wg sync.WaitGroup }

func (m *fakeMic) Start(ctx context.Context, onPCM func([]int16, int64)) error {
	m.wg.Go(func() {
		pcm := make([]int16, 480)
		var phase float64
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for i := range pcm {
				pcm[i] = int16(8000 * math.Sin(phase))
				phase += 2 * math.Pi * 440 / audio.SampleRate
			}
			onPCM(pcm, 0)
		}
	})
	return nil
}

func (m *fakeMic) Close() error { m.wg.Wait(); return nil }

type fakeMicSink struct {
	fakePlayer
	opened atomic.Int64
}

func (s *fakeMicSink) Device() string { return "Fake Mic" }

type duplexRig struct {
	host                  *Host
	client                *Client
	hostBoard, clientBoad *memBoard
	sink                  *fakeMicSink
}

func startDuplex(t *testing.T, virtualMic bool) *duplexRig {
	t.Helper()
	d := &duplexRig{hostBoard: &memBoard{}, clientBoad: &memBoard{}, sink: &fakeMicSink{}}
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "duplex-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 60, BitrateKbps: 20_000, Slices: 1, NoTailscale: true, PIN: "246810",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		FindVirtualMic: func(string) (audio.VirtualMic, error) {
			if !virtualMic {
				return audio.VirtualMic{}, audio.ErrNoVirtualMic
			}
			return audio.VirtualMic{ID: "fake", Name: "Fake Out", Input: "Fake Mic"}, nil
		},
		OpenMicSink: func(audio.VirtualMic) (audio.MicSink, error) {
			d.sink.opened.Add(1)
			return d.sink, nil
		},
		Clipboard:     ClipboardConfig{Send: true, Receive: true},
		OpenClipboard: func() (clipboard.Board, error) { return d.hostBoard, nil },
		Logf:          t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "duplex-client", PIN: "246810",
		Quality:         Quality{Codec: encode.HEVC, FPS: 60},
		OpenSink:        NullSinkOpener(&NullSink{}),
		OpenAudioPlayer: func() (audio.Player, error) { return &fakePlayer{}, nil },
		OpenMic:         func() (audio.MicCapturer, error) { return &fakeMic{}, nil },
		Clipboard:       ClipboardConfig{Send: true, Receive: true},
		OpenClipboard:   func() (clipboard.Board, error) { return d.clientBoad, nil },
		Tailscale:       noTailscale{},
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := connect(t, &clientRig{client: c}, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	d.host, d.client = h, c
	return d
}

func TestMicPassthroughStartsMuted(t *testing.T) {
	d := startDuplex(t, true)
	waitFor(t, "the host's mic state", func() bool { return d.client.Stats().Mic.Device == "Fake Mic" })
	if st := d.client.Stats().Mic; !st.Muted {
		t.Fatalf("a new stream's mic is not muted: %+v", st)
	}
	time.Sleep(300 * time.Millisecond)
	if d.sink.frames.Load() != 0 || d.sink.opened.Load() != 0 {
		t.Fatalf("a muted mic reached the host: sink opened %d, %d frames", d.sink.opened.Load(), d.sink.frames.Load())
	}
	if err := d.client.SetMicMuted(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the voice in the host's virtual mic", func() bool { return d.sink.peak.Load() > 6000 })
	hs := d.host.Stats().Mic
	if hs.Device != "Fake Mic" || hs.Packets == 0 {
		t.Fatalf("host mic stats %+v", hs)
	}
	if err := d.client.SetMicMuted(true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	before := d.host.Stats().Mic.Packets
	time.Sleep(300 * time.Millisecond)
	if after := d.host.Stats().Mic.Packets; after != before {
		t.Fatalf("muted mic still sent %d packets", after-before)
	}
	t.Logf("client mic %+v, host mic %+v", d.client.Stats().Mic, d.host.Stats().Mic)
}

func TestMicWithoutVirtualMicSaysWhy(t *testing.T) {
	d := startDuplex(t, false)
	waitFor(t, "the host's mic hint", func() bool { return d.client.Stats().Mic.Hint == audio.VirtualMicHint })
	err := d.client.SetMicMuted(false)
	if err == nil || !strings.Contains(err.Error(), "Virtual mic not detected") {
		t.Fatalf("unmute without a virtual mic: %v", err)
	}
	if !d.client.Stats().Mic.Muted {
		t.Fatal("the mic unmuted with nowhere to go")
	}
}

func TestClipboardBothWaysWithoutEcho(t *testing.T) {
	d := startDuplex(t, false)
	waitFor(t, "the host to hear the client receives", func() bool {
		s := d.host.liveSession()
		return s != nil && s.PeerState().ClipboardRecv
	})
	waitFor(t, "the client to hear the host receives", func() bool {
		s := d.client.liveSession()
		return s != nil && s.PeerState().ClipboardRecv
	})

	d.clientBoad.copy(clipboard.Item{Format: clipboard.Text, Data: []byte("from the client")})
	waitFor(t, "text on the host", func() bool { return len(d.hostBoard.written()) == 1 })
	if w := d.hostBoard.written()[0]; w.Format != clipboard.Text || string(w.Data) != "from the client" {
		t.Fatalf("host got format %d %q", w.Format, w.Data)
	}
	img := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 300_000)
	d.hostBoard.copy(clipboard.Item{Format: clipboard.PNG, Data: img})
	waitFor(t, "the image on the client", func() bool { return len(d.clientBoad.written()) == 1 })
	if w := d.clientBoad.written()[0]; w.Format != clipboard.PNG || !bytes.Equal(w.Data, img) {
		t.Fatalf("client got format %d, %d bytes", w.Format, len(w.Data))
	}
	time.Sleep(300 * time.Millisecond)
	if n, m := len(d.hostBoard.written()), len(d.clientBoad.written()); n != 1 || m != 1 {
		t.Fatalf("items bounced: host has %d writes, client %d", n, m)
	}

	// Each direction stops with its toggle on either end.
	d.client.SetClipboard(ClipboardConfig{Receive: true})
	d.clientBoad.copy(clipboard.Item{Format: clipboard.Text, Data: []byte("kept on the client")})
	d.host.SetClipboard(ClipboardConfig{Receive: true})
	waitFor(t, "the client to hear the host stopped sending", func() bool { return !d.client.liveSession().PeerState().ClipboardSend })
	d.hostBoard.copy(clipboard.Item{Format: clipboard.Text, Data: []byte("kept on the host")})
	d.clientBoad.copy(clipboard.Item{Format: clipboard.PNG, Size: clipboard.MaxSize + 1})
	time.Sleep(400 * time.Millisecond)
	if n, m := len(d.hostBoard.written()), len(d.clientBoad.written()); n != 1 || m != 1 {
		t.Fatalf("a direction that was turned off still shared: host has %d writes, client %d", n, m)
	}

	d.client.SetClipboard(ClipboardConfig{Send: true, Receive: true})
	d.clientBoad.copy(clipboard.Item{Format: clipboard.PNG, Size: clipboard.MaxSize + 1})
	waitFor(t, "the oversize item to be skipped", func() bool { return d.client.Stats().Clipboard.Skipped == 1 })
	if st := d.host.Stats().Clipboard; st.Sent != 1 || st.Received != 1 {
		t.Fatalf("host clipboard stats %+v", st)
	}
}
