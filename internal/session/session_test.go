package session

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
)

// fakePipe stands in for capture and encode: paced frames whose bytes are a
// function of their index, so the client can check every byte.
type fakePipe struct {
	fps    int
	next   int64
	n      uint32
	forced *atomic.Int64
	kbps   *atomic.Int64
}

func frameBytes(dst []byte, n uint32, key bool) []byte {
	size := 6000 + int(n%7)*500
	if key {
		size = 40_000
	}
	dst = dst[:0]
	for i := range size {
		dst = append(dst, byte(int(n)*31+i))
	}
	binary.BigEndian.PutUint32(dst, n)
	return dst
}

func checkFrame(au []byte) bool {
	if len(au) < 4 {
		return false
	}
	n := binary.BigEndian.Uint32(au)
	for i := 4; i < len(au); i++ {
		if au[i] != byte(int(n)*31+i) {
			return false
		}
	}
	return true
}

func (p *fakePipe) Next(timeout time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	if p.next == 0 {
		p.next = transport.Mono()
	}
	transport.SleepUntilMono(p.next)
	p.next += int64(time.Second) / int64(p.fps)
	captured := transport.Mono()
	if forceKey {
		p.forced.Add(1)
	}
	data := frameBytes(dst, p.n, forceKey)
	pkt := encode.Packet{Data: data, Keyframe: forceKey, PTS: int64(p.n)}
	p.n++
	return pkt, captured, nil
}

func (p *fakePipe) SetBitrate(kbps int) error      { p.kbps.Store(int64(kbps)); return nil }
func (p *fakePipe) InvalidateRefs(pts int64) error { return encode.ErrUnsupported }
func (p *fakePipe) Info() VideoInfo {
	return VideoInfo{Width: 1920, Height: 1080, FPS: p.fps, Source: "fake"}
}
func (p *fakePipe) Close() error { return nil }

// fakeCapturer plays a 440 Hz tone in 20 ms chunks, as ScreenCaptureKit
// delivers them.
type fakeCapturer struct{ wg sync.WaitGroup }

func (c *fakeCapturer) Start(ctx context.Context, onPCM func([]int16, int64)) error {
	c.wg.Go(func() {
		pcm := make([]int16, 960*2)
		var phase float64
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for i := 0; i < 960; i++ {
				v := int16(8000 * math.Sin(phase))
				pcm[2*i], pcm[2*i+1] = v, v
				phase += 2 * math.Pi * 440 / audio.SampleRate
			}
			onPCM(pcm, 0)
		}
	})
	return nil
}

func (c *fakeCapturer) Close() error { c.wg.Wait(); return nil }

type fakePlayer struct {
	frames atomic.Int64
	peak   atomic.Int64
}

func (p *fakePlayer) Write(pcm []int16) error {
	p.frames.Add(1)
	for _, v := range pcm {
		a := int64(v)
		if a < 0 {
			a = -a
		}
		if a > p.peak.Load() {
			p.peak.Store(a)
		}
	}
	return nil
}

func (p *fakePlayer) Close() error { return nil }

type fakeWhoIs struct {
	sameUser bool
	calls    atomic.Int64
}

func (w *fakeWhoIs) WhoIs(ctx context.Context, addr string) (*tailscale.PeerIdentity, error) {
	w.calls.Add(1)
	return &tailscale.PeerIdentity{LoginName: "chris@example.com", IsSameUser: w.sameUser}, nil
}

type noTailscale struct{}

func (noTailscale) PathInfo(context.Context, netip.Addr) (tailscale.PathInfo, error) {
	return tailscale.PathInfo{}, tailscale.ErrNotRunning
}

type harness struct {
	host     *Host
	forced   atomic.Int64
	kbps     atomic.Int64
	inputsMu sync.Mutex
	inputs   []input.Event
}

func startHost(t *testing.T, dir, listen string, whois WhoIser, discoveryAddr string) *harness {
	t.Helper()
	hs := &harness{}
	h, err := NewHost(HostConfig{
		ConfigDir: dir, Name: "test-host", ListenAddr: listen, DiscoveryAddr: discoveryAddr,
		FPS: 120, BitrateKbps: 40_000, Slices: 2, WhoIs: whois, NoTailscale: whois == nil,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &hs.forced, kbps: &hs.kbps}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		OnInput: func(ev input.Event) {
			hs.inputsMu.Lock()
			hs.inputs = append(hs.inputs, ev)
			hs.inputsMu.Unlock()
		},
		Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.cfg.InjectInput || h.inject != nil {
		t.Fatal("tests must never inject real input")
	}
	// A port a stopped host just freed can take a moment to come back.
	deadline := time.Now().Add(5 * time.Second)
	for err = h.Start(context.Background()); err != nil; err = h.Start(context.Background()) {
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(h.Stop)
	hs.host = h
	return hs
}

func (hs *harness) received() []input.Event {
	hs.inputsMu.Lock()
	defer hs.inputsMu.Unlock()
	return append([]input.Event(nil), hs.inputs...)
}

type clientRig struct {
	client  *Client
	sink    *NullSink
	player  *fakePlayer
	corrupt atomic.Int64
}

func newClient(t *testing.T, dir, pin string) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	r.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			r.corrupt.Add(1)
		}
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: dir, Name: "test-client", PIN: pin,
		Quality:         Quality{Codec: encode.HEVC, FPS: 240, BitrateKbps: 30_000},
		OpenSink:        NullSinkOpener(r.sink),
		OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale:       noTailscale{},
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func connect(t *testing.T, r *clientRig, addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.client.Connect(ctx, Target{Addr: addr})
}

// TestLoopbackPINPairing streams video, audio and input between an
// in-process host and client over real QUIC and AEAD UDP, pairs by PIN,
// reconnects on the pin alone, and checks the stats.
func TestLoopbackPINPairing(t *testing.T) {
	hostDir, clientDir := t.TempDir(), t.TempDir()
	hs := startHost(t, hostDir, "127.0.0.1:0", &fakeWhoIs{sameUser: false}, "off")
	addr := hs.host.Addr().String()
	pin := hs.host.PIN()

	if err := connect(t, newClient(t, t.TempDir(), "000000"), addr); err == nil && pin != "000000" {
		t.Fatal("a wrong PIN connected")
	}

	r := newClient(t, clientDir, pin)
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	if hs.host.PIN() == pin {
		t.Error("PIN did not rotate after pairing")
	}
	if n := len(hs.host.PairedClients()); n != 1 {
		t.Fatalf("host paired %d clients, want 1", n)
	}
	stored, err := config.LoadClientConfig(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stored.CheckPin("addr:"+addr, hs.host.Fingerprint()); err != nil {
		t.Fatalf("client did not pin the host: %v", err)
	}

	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 60 })
	waitFor(t, "audible audio", func() bool { return r.player.peak.Load() > 2000 })
	if r.corrupt.Load() != 0 {
		t.Fatalf("%d corrupted frames", r.corrupt.Load())
	}

	sent := []input.Event{
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: 0x04, Pressed: true, Modifiers: 0x02, SystemKeyCapture: true}},
		{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: 32768, Y: 65535}},
		{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: -5, DY: 7}},
		{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonRight, Pressed: true}},
		{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DY: -120}},
		{Type: input.TypeGamepad, Gamepad: input.GamepadState{Index: 1, Buttons: input.PadA | input.PadStart, LeftTrigger: 200, LeftX: -32768, RightY: 1234}},
	}
	for _, ev := range sent {
		if err := r.client.SendInput(ev); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "input events", func() bool { return len(hs.received()) >= len(sent) })
	if got := hs.received(); len(got) != len(sent) {
		t.Fatalf("host got %d events, want %d", len(got), len(sent))
	} else {
		for i := range sent {
			if got[i] != sent[i] {
				t.Errorf("event %d: got %+v, want %+v", i, got[i], sent[i])
			}
		}
	}

	before := hs.forced.Load()
	if err := r.client.RequestKeyframe(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a forced keyframe", func() bool { return hs.forced.Load() > before })

	waitFor(t, "latency samples", func() bool { return r.client.Stats().E2E.Samples > 30 })
	cs := r.client.Stats()
	t.Logf("client: %.0f fps %.0f kbps rtt %.2f ms net %.2f ms e2e p50/p95/p99 %.2f/%.2f/%.2f ms fec %.1f%% audio %.0f ms",
		cs.FPS, cs.Kbps, cs.RTTMs, cs.NetworkMs, cs.E2E.P50, cs.E2E.P95, cs.E2E.P99, cs.FECParityPct, cs.AudioBufferMs)
	switch {
	case !cs.Connected || cs.Path != "lan-direct" || cs.Codec != "HEVC":
		t.Errorf("stats: connected=%v path=%q codec=%q", cs.Connected, cs.Path, cs.Codec)
	case cs.FPS < 60 || cs.Kbps <= 0:
		t.Errorf("stats: %.1f fps %.0f kbps", cs.FPS, cs.Kbps)
	case cs.E2E.P50 <= 0 || cs.E2E.P99 < cs.E2E.P50 || cs.E2E.P99 > 1000:
		t.Errorf("stats: e2e %+v", cs.E2E)
	case cs.AudioPackets == 0 || cs.InputSent != uint64(len(sent)):
		t.Errorf("stats: audio %d input %d", cs.AudioPackets, cs.InputSent)
	}
	waitFor(t, "a client stats report", func() bool { return hs.host.Stats().Report.FramesReceived > 0 })
	st := hs.host.Stats()
	if !st.Connected || st.Frames == 0 || st.Keyframes < 2 || st.AudioPackets == 0 || st.InputEvents != uint64(len(sent)) || st.Video.Source != "fake" {
		t.Errorf("host stats: %+v", st)
	}

	r.client.Disconnect()
	waitFor(t, "the host to see the goodbye", func() bool { return !hs.host.Stats().Connected })

	again := newClient(t, clientDir, "")
	if err := connect(t, again, addr); err != nil {
		t.Fatalf("paired reconnect: %v", err)
	}
	waitFor(t, "video after reconnect", func() bool { return again.sink.Units.Load() >= 10 })
	again.client.Disconnect()

	stranger := newClient(t, t.TempDir(), "")
	if err := connect(t, stranger, addr); !errors.Is(err, transport.ErrAuthDenied) {
		t.Fatalf("unpaired client without a PIN: %v, want ErrAuthDenied", err)
	}
}

// TestPinnedHostMismatch: a client that pinned one host refuses another
// host that answers on the same address.
func TestPinnedHostMismatch(t *testing.T) {
	clientDir := t.TempDir()
	first := startHost(t, t.TempDir(), "127.0.0.1:0", nil, "off")
	addr := first.host.Addr().String()
	r := newClient(t, clientDir, first.host.PIN())
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	r.client.Disconnect()
	first.host.Stop()

	impostor := startHost(t, t.TempDir(), addr, nil, "off")
	again := newClient(t, clientDir, impostor.host.PIN())
	err := connect(t, again, addr)
	if !errors.Is(err, config.ErrFingerprintMismatch) {
		t.Fatalf("connect to another host on a pinned address: %v, want ErrFingerprintMismatch", err)
	}
	if n := len(impostor.host.PairedClients()); n != 0 {
		t.Fatalf("impostor paired %d clients", n)
	}
}

// TestTailscaleSameUser: a device of the host's own tailnet user connects
// with no PIN and is paired; another user's device is refused.
func TestTailscaleSameUser(t *testing.T) {
	other := startHost(t, t.TempDir(), "127.0.0.1:0", &fakeWhoIs{sameUser: false}, "off")
	if err := connect(t, newClient(t, t.TempDir(), ""), other.host.Addr().String()); !errors.Is(err, transport.ErrAuthDenied) {
		t.Fatalf("another tailnet user: %v, want ErrAuthDenied", err)
	}

	who := &fakeWhoIs{sameUser: true}
	hs := startHost(t, t.TempDir(), "127.0.0.1:0", who, "off")
	r := newClient(t, t.TempDir(), "")
	if err := connect(t, r, hs.host.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if who.calls.Load() == 0 {
		t.Fatal("host never asked Tailscale WhoIs")
	}
	for _, c := range hs.host.PairedClients() {
		if c.Method != "tailscale:chris@example.com" {
			t.Errorf("paired by %q", c.Method)
		}
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 10 })
}

// TestDiscoveryRace connects through a LAN discovery candidate and learns
// the host ID and fingerprint from its beacon.
func TestDiscoveryRace(t *testing.T) {
	hs := startHost(t, t.TempDir(), "127.0.0.1:0", nil, "127.0.0.1:0")
	disc := hs.host.DiscoveryAddr().(*net.UDPAddr).AddrPort()
	clientDir := t.TempDir()
	r := newClient(t, clientDir, hs.host.PIN())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.client.Connect(ctx, Target{Candidates: []discovery.Candidate{{HostID: hs.host.HostID(), Kind: discovery.PathLANDirect, Addr: disc}}})
	if err != nil {
		t.Fatal(err)
	}
	cs := r.client.Stats()
	if cs.HostID != hs.host.HostID() || cs.HostName != "test-host" || cs.Path != "lan-direct" {
		t.Fatalf("stats: %+v", cs)
	}
	stored, _ := config.LoadClientConfig(clientDir)
	if _, err := stored.CheckPin(hs.host.HostID(), hs.host.Fingerprint()); err != nil {
		t.Fatal(err)
	}
}

func TestInputRoundTrip(t *testing.T) {
	for _, ev := range []input.Event{
		{Type: input.TypeKey, Key: input.KeyEvent{Usage: 0xe3, Modifiers: 0xff}},
		{Type: input.TypeMouseMoveAbs},
		{Type: input.TypeGamepad, Gamepad: input.GamepadState{Index: 15, Buttons: input.PadGuide, RightTrigger: 255, LeftY: 32767}},
	} {
		in, err := toWire(ev)
		if err != nil {
			t.Fatal(err)
		}
		got, err := fromWire(in)
		if err != nil || got != ev {
			t.Errorf("%+v: got %+v, %v", ev, got, err)
		}
	}
	bad := []struct {
		name string
		ev   func() (input.Event, error)
	}{
		{"absolute out of range", func() (input.Event, error) {
			in, _ := toWire(input.Event{Type: input.TypeMouseMoveAbs})
			in.X = 70000
			return fromWire(in)
		}},
		{"unknown key mod bits", func() (input.Event, error) {
			in, _ := toWire(input.Event{Type: input.TypeKey})
			in.Mods = 0x8000
			return fromWire(in)
		}},
		{"unknown button", func() (input.Event, error) {
			in, _ := toWire(input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.ButtonLeft}})
			in.Button = 99
			return fromWire(in)
		}},
	}
	for _, c := range bad {
		if _, err := c.ev(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if _, err := toWire(input.Event{Type: input.TypeRumble}); err == nil {
		t.Error("rumble sent as input")
	}
}

// TestColdStartDecode: a decoder whose first access unit takes long, as
// VideoToolbox's does, must never get a frame whose reference the client
// dropped meanwhile: after a gap the next unit it decodes is a keyframe.
func TestColdStartDecode(t *testing.T) {
	hs := startHost(t, t.TempDir(), "127.0.0.1:0", nil, "off")
	r := newClient(t, t.TempDir(), hs.host.PIN())
	var orphans atomic.Int64
	last, broken := int64(-1), false
	r.sink.OnUnit = func(au []byte, pts int64) {
		if last < 0 {
			time.Sleep(300 * time.Millisecond)
		}
		switch key := len(au) == 40_000; {
		case key:
			broken = false
		case broken || pts != last+1:
			broken = true
			orphans.Add(1)
		}
		last = pts
	}
	if err := connect(t, r, hs.host.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 120 })
	cs := r.client.Stats()
	t.Logf("%d units decoded, %d frames dropped, %d orphaned", r.sink.Units.Load(), cs.FramesDropped, orphans.Load())
	if n := orphans.Load(); n != 0 {
		t.Fatalf("the decoder got %d frames after a gap, not a keyframe", n)
	}
}
