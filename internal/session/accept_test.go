package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/transport"
)

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) logf(t *testing.T) func(string, ...any) {
	return func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		t.Log(line)
		l.mu.Lock()
		l.lines = append(l.lines, line)
		l.mu.Unlock()
	}
}

func (l *logBuf) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// wedgedPipe is a fakePipe whose Close blocks until release closes, as a
// capture or encoder teardown that hangs in the driver.
type wedgedPipe struct {
	fakePipe
	release <-chan struct{}
}

func (p *wedgedPipe) Close() error { <-p.release; return nil }

func newTestHost(t *testing.T, cfg HostConfig, logs *logBuf) *Host {
	t.Helper()
	cfg.ConfigDir, cfg.Name, cfg.ListenAddr, cfg.FPS, cfg.NoTailscale = t.TempDir(), "test-host", "127.0.0.1:0", 120, true
	if cfg.DiscoveryAddr == "" {
		cfg.DiscoveryAddr = "off"
	}
	if cfg.OpenVideo == nil {
		var forced, kbps atomic.Int64
		cfg.OpenVideo = func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		}
	}
	cfg.OpenAudio = func() (audio.Capturer, error) { return &fakeCapturer{}, nil }
	cfg.Logf = logs.logf(t)
	h, err := NewHost(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func startTestHost(t *testing.T, h *Host) {
	t.Helper()
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
}

// TestHostListensAgainAfterListenerDies: a control listener that stops
// under a running host comes back on the same port, and clients connect.
func TestHostListensAgainAfterListenerDies(t *testing.T) {
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{}, logs)
	startTestHost(t, h)
	addr := h.Addr().String()
	h.srv.Load().Close()

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, addr); err != nil {
		t.Fatalf("connect after the listener died: %v", err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 5 })
	if !logs.has("control listener on") || !logs.has("listening again") {
		t.Error("the host did not log that its listener died and came back")
	}
	if h.Err() != nil {
		t.Errorf("host stopped: %v", h.Err())
	}
}

// TestHostStopsWhenItCannotListenAgain: a host whose control port cannot
// come back stops, discovery included, rather than advertise a dead port.
func TestHostStopsWhenItCannotListenAgain(t *testing.T) {
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{DiscoveryAddr: "127.0.0.1:0"}, logs)
	var opened atomic.Int32
	h.listen = func(addr string, cfg transport.ServerConfig) (*transport.Server, error) {
		if opened.Add(1) > 1 {
			return nil, errors.New("port taken")
		}
		return transport.Listen(addr, cfg)
	}
	startTestHost(t, h)
	disc := netip.MustParseAddrPort(h.DiscoveryAddr().String())
	if _, _, err := discovery.Probe(context.Background(), disc); err != nil {
		t.Fatalf("discovery before: %v", err)
	}
	h.srv.Load().Close()

	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the host kept running without a control listener")
	}
	if h.Err() == nil || !logs.has("stopping the host") {
		t.Errorf("host err %v; want a logged reason", h.Err())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := discovery.Probe(ctx, disc); err == nil {
		t.Error("discovery still answers for a host that stopped")
	}
}

// TestTakeoverWithWedgedTeardown: a stream whose teardown hangs cannot
// hold up the next client's connect, nor the host's later accepts.
func TestTakeoverWithWedgedTeardown(t *testing.T) {
	release := make(chan struct{})
	var forced, kbps atomic.Int64
	var opens atomic.Int32
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{OpenVideo: func(p VideoParams) (VideoPipe, error) {
		fp := fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}
		if opens.Add(1) == 1 {
			return &wedgedPipe{fakePipe: fp, release: release}, nil
		}
		return &fp, nil
	}}, logs)
	startTestHost(t, h)
	t.Cleanup(func() { close(release) })
	addr := h.Addr().String()
	dir := t.TempDir()

	first := newClient(t, dir, h.PIN())
	if err := connect(t, first, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video", func() bool { return first.sink.Units.Load() >= 5 })

	second := newClient(t, dir, "")
	start := time.Now()
	if err := connect(t, second, addr); err != nil {
		t.Fatalf("takeover from a stream whose teardown hangs: %v after %v", err, time.Since(start))
	}
	waitFor(t, "video on the new stream", func() bool { return second.sink.Units.Load() >= 5 })
	if !logs.has("to stop; going on without it") {
		t.Error("the host did not log the slow teardown")
	}

	second.client.Disconnect()
	third := newClient(t, dir, "")
	if err := connect(t, third, addr); err != nil {
		t.Fatalf("connect after the takeover: %v", err)
	}
}

// TestImmediateReconnect: a client that leaves and connects again at once
// streams every time.
func TestImmediateReconnect(t *testing.T) {
	h := newTestHost(t, HostConfig{}, &logBuf{})
	startTestHost(t, h)
	addr := h.Addr().String()
	dir := t.TempDir()
	pin := h.PIN()
	for i := range 5 {
		r := newClient(t, dir, pin)
		pin = ""
		if err := connect(t, r, addr); err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 3 })
		r.client.Disconnect()
	}
}

// TestConnectFallsBackWhenRaceWinnerIsSilent: a route whose discovery
// answers but whose control port does not, as a host whose QUIC listener
// died, gives way to the next route.
func TestConnectFallsBackWhenRaceWinnerIsSilent(t *testing.T) {
	h := newTestHost(t, HostConfig{DiscoveryAddr: "127.0.0.1:0"}, &logBuf{})
	startTestHost(t, h)

	hole, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer hole.Close()
	stale, err := discovery.OpenPort("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = stale.Serve(ctx, func() discovery.Beacon {
			return discovery.Beacon{HostID: h.HostID(), Name: "test-host", ControlPort: uint16(hole.LocalAddr().(*net.UDPAddr).Port),
				CertFingerprint: h.Fingerprint()}
		})
	}()

	r := newClient(t, t.TempDir(), h.PIN())
	target := Target{HostID: h.HostID(), Fingerprint: h.Fingerprint(), Candidates: []discovery.Candidate{
		{HostID: h.HostID(), Kind: discovery.PathLANDirect, Addr: netip.MustParseAddrPort(stale.LocalAddr().String())},
		{HostID: h.HostID(), Kind: discovery.PathTailscaleDirect, Addr: netip.MustParseAddrPort(h.DiscoveryAddr().String())},
	}}
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer ccancel()
	if err := r.client.Connect(cctx, target); err != nil {
		t.Fatalf("connect with a silent first route: %v", err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 3 })
}

// TestConnectFallsBackWhenHandshakeTimeoutIsShort: a per-attempt handshake
// timeout shorter than QUIC's own gives way to the next route, not ends the
// connect, while the caller's budget lasts.
func TestConnectFallsBackWhenHandshakeTimeoutIsShort(t *testing.T) {
	h := newTestHost(t, HostConfig{DiscoveryAddr: "127.0.0.1:0"}, &logBuf{})
	startTestHost(t, h)

	hole, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer hole.Close()
	stale, err := discovery.OpenPort("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = stale.Serve(ctx, func() discovery.Beacon {
			return discovery.Beacon{HostID: h.HostID(), Name: "test-host", ControlPort: uint16(hole.LocalAddr().(*net.UDPAddr).Port),
				CertFingerprint: h.Fingerprint()}
		})
	}()

	r := newClient(t, t.TempDir(), h.PIN())
	r.client.cfg.HandshakeTimeout = 500 * time.Millisecond
	target := Target{HostID: h.HostID(), Fingerprint: h.Fingerprint(), Candidates: []discovery.Candidate{
		{HostID: h.HostID(), Kind: discovery.PathLANDirect, Addr: netip.MustParseAddrPort(stale.LocalAddr().String())},
		{HostID: h.HostID(), Kind: discovery.PathTailscaleDirect, Addr: netip.MustParseAddrPort(h.DiscoveryAddr().String())},
	}}
	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer ccancel()
	if err := r.client.Connect(cctx, target); err != nil {
		t.Fatalf("connect with a silent first route and a short handshake timeout: %v", err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 3 })
}

// TestStopDuringRelistenClosesReplacement: a listener opened again while
// Stop runs is closed, not left serving after Done.
func TestStopDuringRelistenClosesReplacement(t *testing.T) {
	h := newTestHost(t, HostConfig{}, &logBuf{})
	entered, release := make(chan struct{}), make(chan struct{})
	replaced := make(chan *transport.Server, 1)
	var opened atomic.Int32
	h.listen = func(addr string, cfg transport.ServerConfig) (*transport.Server, error) {
		if opened.Add(1) == 1 {
			return transport.Listen(addr, cfg)
		}
		close(entered)
		<-release
		srv, err := transport.Listen(addr, cfg)
		if err == nil {
			replaced <- srv
		}
		return srv, err
	}
	startTestHost(t, h)
	h.srv.Load().Close()
	<-entered

	go h.Stop()
	waitFor(t, "Stop to begin", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.stopping
	})
	close(release)

	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not finish")
	}
	var srv *transport.Server
	select {
	case srv = <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement listener was never opened")
	}
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := srv.Accept(ctx); !errors.Is(err, transport.ErrServerClosed) {
		t.Fatalf("replacement listener after Stop: Accept = %v; want ErrServerClosed", err)
	}
}

// TestHaltWithWedgedTeardown: a host that cannot listen again stops,
// discovery included, even when its stream's teardown hangs.
func TestHaltWithWedgedTeardown(t *testing.T) {
	release := make(chan struct{})
	var forced, kbps atomic.Int64
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{DiscoveryAddr: "127.0.0.1:0", OpenVideo: func(p VideoParams) (VideoPipe, error) {
		return &wedgedPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, release: release}, nil
	}}, logs)
	h.stopWait = 200 * time.Millisecond
	var opened atomic.Int32
	h.listen = func(addr string, cfg transport.ServerConfig) (*transport.Server, error) {
		if opened.Add(1) > 1 {
			return nil, errors.New("port taken")
		}
		return transport.Listen(addr, cfg)
	}
	startTestHost(t, h)
	t.Cleanup(func() { close(release) })
	disc := netip.MustParseAddrPort(h.DiscoveryAddr().String())

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 5 })
	h.srv.Load().Close()

	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a wedged stream teardown kept the host from stopping")
	}
	if h.Err() == nil {
		t.Error("host stopped with no error after its listener could not come back")
	}
	if !logs.has("stopping without them") {
		t.Error("the host did not log the slow teardown")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, _, err := discovery.Probe(ctx, disc); err == nil {
		t.Error("discovery still answers for a host that stopped")
	}
}

// TestHaltWithWedgedVDDClose: a host that cannot listen again stops, and
// Done closes with its error, even when the virtual display driver hangs
// on close.
func TestHaltWithWedgedVDDClose(t *testing.T) {
	logs := &logBuf{}
	h := newTestHost(t, HostConfig{}, logs)
	h.stopWait = 200 * time.Millisecond
	release, entered := make(chan struct{}), make(chan struct{})
	h.closeVDD = func() error {
		close(entered)
		<-release
		return nil
	}
	var opened atomic.Int32
	h.listen = func(addr string, cfg transport.ServerConfig) (*transport.Server, error) {
		if opened.Add(1) > 1 {
			return nil, errors.New("port taken")
		}
		return transport.Listen(addr, cfg)
	}
	startTestHost(t, h)
	t.Cleanup(func() { close(release) })
	h.srv.Load().Close()

	select {
	case <-h.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a wedged virtual display close kept the host from stopping")
	}
	select {
	case <-entered:
	default:
		t.Fatal("Stop never closed the virtual display driver")
	}
	if h.Err() == nil {
		t.Error("host stopped with no error after its listener could not come back")
	}
	if !logs.has("to close; stopping without them") {
		t.Error("the host did not log the slow virtual display close")
	}
}

// closeInjector records injects, and any after Close.
type closeInjector struct {
	recordInjector
	closed atomic.Bool
	late   atomic.Int32
}

func (c *closeInjector) Inject(ev input.Event) error {
	if c.closed.Load() {
		c.late.Add(1)
	}
	return c.recordInjector.Inject(ev)
}

func (c *closeInjector) Close() error { c.closed.Store(true); return nil }

// TestStopWithWedgedTeardownReleasesHeldInput: when Stop gives up on a
// stream whose teardown hangs, it still lets go of the held key before it
// closes its injector, and the stream injects nothing once it unwedges.
func TestStopWithWedgedTeardownReleasesHeldInput(t *testing.T) {
	release := make(chan struct{})
	var forced, kbps atomic.Int64
	h := newTestHost(t, HostConfig{InjectInput: true, OpenVideo: func(p VideoParams) (VideoPipe, error) {
		return &wedgedPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, release: release}, nil
	}}, &logBuf{})
	h.stopWait = 200 * time.Millisecond
	inj := &closeInjector{}
	h.openInjector = func() (input.Injector, error) { return inj, nil }
	startTestHost(t, h)
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})

	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video", func() bool { return r.sink.Units.Load() >= 5 })
	press := input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA, Pressed: true}}
	if err := r.client.SendInput(press); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the press at the host", func() bool { return len(inj.events()) == 1 })

	stopped := make(chan struct{})
	go func() {
		h.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited on a wedged stream teardown")
	}
	want := []input.Event{press, {Type: input.TypeKey, Key: input.KeyEvent{Usage: input.KeyA}}}
	if got := inj.events(); !slices.Equal(got, want) || !inj.closed.Load() {
		t.Fatalf("after Stop: injected %+v, closed %v; want %+v, closed", got, inj.closed.Load(), want)
	}

	close(release)
	released = true
	ended := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end after its teardown unwedged")
	}
	if n := inj.late.Load(); n != 0 {
		t.Fatalf("%d injects after the injector closed", n)
	}
	if got := inj.events(); !slices.Equal(got, want) {
		t.Fatalf("after the stream ended: injected %+v; want %+v", got, want)
	}
}
