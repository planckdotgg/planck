package session

import (
	"context"
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
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// lanResponder answers discovery probes with the host's beacon while up,
// standing in for the host's LAN address: on loopback both routes reach
// the same control port, and the candidate's kind decides the route.
type lanResponder struct {
	conn   net.PacketConn
	beacon func() discovery.Beacon
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newLANResponder(t *testing.T, h *Host) *lanResponder {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctrl := uint16(h.Addr().(*net.UDPAddr).Port)
	l := &lanResponder{conn: conn, beacon: func() discovery.Beacon {
		return discovery.Beacon{HostID: h.HostID(), Name: h.Name(), ControlPort: ctrl, CertFingerprint: h.Fingerprint()}
	}}
	t.Cleanup(func() { l.down(); conn.Close() })
	return l
}

func (l *lanResponder) addr() netip.AddrPort { return l.conn.LocalAddr().(*net.UDPAddr).AddrPort() }

func (l *lanResponder) up() {
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.done = cancel, make(chan struct{})
	done := l.done
	go func() {
		defer close(done)
		discovery.ServeFunc(ctx, l.conn, l.beacon)
	}()
}

func (l *lanResponder) down() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		<-l.done
		l.cancel = nil
		l.conn.SetReadDeadline(time.Time{})
	}
}

// startRouteHost takes whatever bitrate a client asks for, so the route's
// ceiling shows in the accepted config.
func startRouteHost(t *testing.T) *Host {
	t.Helper()
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "route-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "127.0.0.1:0",
		FPS: 120, NoTailscale: true, PIN: "112233",
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		Logf:      t.Logf,
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

func newRouteClient(t *testing.T, dir string, route RouteConfig) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	r.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			r.corrupt.Add(1)
		}
	}
	c, err := NewClient(ClientConfig{
		ConfigDir: dir, Name: "route-client", PIN: "112233",
		Quality:         Quality{Codec: encode.HEVC, FPS: 120},
		OpenSink:        NullSinkOpener(r.sink),
		OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale:       noTailscale{},
		Route:           route,
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

func routeTarget(h *Host, lan *lanResponder) Target {
	disc := h.DiscoveryAddr().(*net.UDPAddr).AddrPort()
	return Target{HostID: h.HostID(), Candidates: []discovery.Candidate{
		{HostID: h.HostID(), Kind: discovery.PathLANDirect, Addr: lan.addr()},
		{HostID: h.HostID(), Kind: discovery.PathTailscaleDirect, Addr: disc},
	}}
}

type routeWant struct {
	path     discovery.PathKind
	dgram    int
	kbps     int
	switches uint64
}

func checkRoute(t *testing.T, r *clientRig, want routeWant) {
	t.Helper()
	waitFor(t, "the stream on "+want.path.String(), func() bool {
		cs := r.client.Stats()
		return cs.Path == want.path.String() && cs.RouteSwitches == want.switches
	})
	cs := r.client.Stats()
	if !cs.Connected || cs.DatagramBytes != want.dgram || cs.MaxBitrateKbps != want.kbps {
		t.Fatalf("on %s: datagrams %d B, ceiling %d kbps; want %d B, %d kbps", cs.Path, cs.DatagramBytes, cs.MaxBitrateKbps, want.dgram, want.kbps)
	}
	units := r.sink.Units.Load()
	waitFor(t, "video on "+want.path.String(), func() bool { return r.sink.Units.Load() >= units+30 })
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d corrupt units", n)
	}
	select {
	case <-r.client.Done():
		t.Fatalf("stream ended: %v", r.client.Err())
	default:
	}
}

var (
	onLAN       = routeWant{discovery.PathLANDirect, wire.MaxLANDatagram, discovery.LANMaxBitrateKbps, 0}
	onTailscale = routeWant{discovery.PathTailscaleDirect, wire.MaxDatagram, tailscale.DirectMaxBitrateKbps, 0}
)

func (w routeWant) after(n uint64) routeWant { w.switches = n; return w }

// TestRouteUpgradeToLAN: a stream that starts on the tailnet because the
// LAN does not answer moves to the LAN once it answers twice, taking LAN
// datagrams and the LAN bitrate ceiling, without ending the stream.
func TestRouteUpgradeToLAN(t *testing.T) {
	h := startRouteHost(t)
	lan := newLANResponder(t, h)
	dir := t.TempDir()
	r := newRouteClient(t, dir, RouteConfig{PreferLAN: true, ProbeInterval: 100 * time.Millisecond, StallTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	checkRoute(t, r, onTailscale)

	lan.up()
	checkRoute(t, r, onLAN.after(1))
	if n := h.Stats().Sessions; n != 2 {
		t.Fatalf("host saw %d sessions, want 2", n)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := stored.PinnedHosts[h.HostID()]
	if p.LastLANAddr == "" || p.LastTailscaleAddr == "" || p.LastAddr != p.LastLANAddr || p.Name != "route-host" {
		t.Fatalf("pin after the move: %+v", p)
	}
}

// TestRouteUpgradeNeedsPreferLAN: without PreferLAN the stream stays on
// the tailnet route it raced to.
func TestRouteUpgradeNeedsPreferLAN(t *testing.T) {
	h := startRouteHost(t)
	lan := newLANResponder(t, h)
	r := newRouteClient(t, t.TempDir(), RouteConfig{ProbeInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	checkRoute(t, r, onTailscale)
	lan.up()
	time.Sleep(500 * time.Millisecond)
	checkRoute(t, r, onTailscale)
}

// TestRouteFailoverOnDrop: a LAN stream whose session drops moves to the
// tailnet, with tailnet datagrams and ceiling.
func TestRouteFailoverOnDrop(t *testing.T) {
	h := startRouteHost(t)
	lan := newLANResponder(t, h)
	lan.up()
	r := newRouteClient(t, t.TempDir(), RouteConfig{PreferLAN: true, ProbeInterval: time.Hour, StallTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	checkRoute(t, r, onLAN)

	lan.down()
	r.client.mu.Lock()
	cc := r.client.conn
	r.client.mu.Unlock()
	// Closing from the client side ends the session without the host's
	// goodbye, as a lost connection does.
	cc.session().Close()
	checkRoute(t, r, onTailscale.after(1))
}

// TestRouteFailoverOnStall: a LAN route that goes quiet loses the stream
// to the tailnet even while the LAN still answers discovery.
func TestRouteFailoverOnStall(t *testing.T) {
	h := startRouteHost(t)
	lan := newLANResponder(t, h)
	lan.up()
	r := newRouteClient(t, t.TempDir(), RouteConfig{PreferLAN: true, ProbeInterval: time.Hour, StallTimeout: 300 * time.Millisecond})
	var frozen atomic.Pointer[transport.Session]
	r.client.activity = func(s *transport.Session) uint64 {
		if frozen.Load() == s {
			return 0
		}
		return s.Stats().PacketsReceived
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	checkRoute(t, r, onLAN)
	r.client.mu.Lock()
	cc := r.client.conn
	r.client.mu.Unlock()
	frozen.Store(cc.session())
	checkRoute(t, r, onTailscale.after(1))
}

// TestRouteHostGoodbyeEnds: a host that stops ends the stream; the client
// does not try other routes back to it.
func TestRouteHostGoodbyeEnds(t *testing.T) {
	h := startRouteHost(t)
	lan := newLANResponder(t, h)
	lan.up()
	r := newRouteClient(t, t.TempDir(), RouteConfig{PreferLAN: true, StallTimeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.client.Connect(ctx, routeTarget(h, lan)); err != nil {
		t.Fatal(err)
	}
	checkRoute(t, r, onLAN)
	done := r.client.Done()
	h.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream still running after the host stopped")
	}
	if n := h.Stats().Sessions; n != 1 {
		t.Fatalf("host saw %d sessions, want 1", n)
	}
}
