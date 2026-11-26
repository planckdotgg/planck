package transport

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/wire"
)

const testPIN = "4821"

type pairOpts struct {
	server func(*ServerConfig)
	client func(*ClientConfig)
}

type pair struct {
	host, client *Session
	hostCert     tls.Certificate
}

func identities(t testing.TB) (host, client tls.Certificate) {
	t.Helper()
	host, err := NewIdentity("test-host")
	if err != nil {
		t.Fatal(err)
	}
	client, err = NewIdentity("test-client")
	if err != nil {
		t.Fatal(err)
	}
	return host, client
}

func startServer(t testing.TB, cfg ServerConfig) *Server {
	t.Helper()
	sv, err := Listen("127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sv.Close() })
	return sv
}

func pinServerConfig(cert tls.Certificate) ServerConfig {
	return ServerConfig{
		Certificate: cert,
		Name:        "host",
		MediaAddr:   "127.0.0.1:0",
		PIN:         func() string { return testPIN },
		Authorize: func(_ context.Context, info AuthInfo) (string, error) {
			if !info.PINVerified {
				return "", errors.New("pairing PIN required")
			}
			return "paired:" + info.DeviceName, nil
		},
	}
}

func newPair(t testing.TB, o pairOpts) pair {
	t.Helper()
	hostCert, clientCert := identities(t)
	scfg := pinServerConfig(hostCert)
	if o.server != nil {
		o.server(&scfg)
	}
	sv := startServer(t, scfg)
	ccfg := ClientConfig{
		Certificate: clientCert,
		Name:        "mac",
		VerifyHost:  PinnedHost(CertFingerprint(hostCert)),
		Auth:        wire.AuthPIN,
		PIN:         testPIN,
		Session: wire.SessionConfig{
			Codec: wire.CodecHEVC, Chroma: wire.Chroma444, Width: 1280, Height: 720, FPS: 120,
			BitrateKbps: 50_000, Slices: 4, SubFrame: true, AudioSampleRate: 48000, AudioChannels: 2,
		},
	}
	if o.client != nil {
		o.client(&ccfg)
	}
	type acc struct {
		s   *Session
		err error
	}
	hostc := make(chan acc, 1)
	go func() {
		s, err := sv.Accept(context.Background())
		hostc <- acc{s, err}
	}()
	client, err := Dial(context.Background(), sv.Addr().String(), ccfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h := <-hostc
	if h.err != nil {
		t.Fatalf("accept: %v", h.err)
	}
	t.Cleanup(func() {
		client.Close()
		h.s.Close()
	})
	return pair{host: h.s, client: client, hostCert: hostCert}
}

func fillFrame(b []byte, id uint32) {
	for i := range b {
		b[i] = byte(uint32(i)*31 + id*7)
	}
	if len(b) >= 4 {
		binary.BigEndian.PutUint32(b, id)
	}
}

func frameOK(b []byte, id uint32) bool {
	if len(b) < 4 || binary.BigEndian.Uint32(b) != id {
		return false
	}
	for i := 4; i < len(b); i++ {
		if b[i] != byte(uint32(i)*31+id*7) {
			return false
		}
	}
	return true
}

// TestSessionEndToEnd streams video, audio, rumble and input over loopback
// with 2% simulated loss on the host's media path, and checks FEC recovery,
// PLI-driven keyframes, rate adaptation, loss estimation and clock sync.
func TestSessionEndToEnd(t *testing.T) {
	var (
		mu          sync.Mutex
		framesOK    int
		framesBad   int
		keyframesRx int
		audioRx     int
		rumbleRx    int
		inputSeqs   = map[uint32]bool{}
		slicesSeen  atomic.Int64
	)
	p := newPair(t, pairOpts{
		server: func(c *ServerConfig) {
			c.SimulateLoss = 0.02
			c.PingInterval = 50 * time.Millisecond
			c.Handlers.Input = func(_ *Session, in wire.Input) {
				mu.Lock()
				inputSeqs[in.Seq] = true
				mu.Unlock()
			}
		},
		client: func(c *ClientConfig) {
			c.PingInterval = 50 * time.Millisecond
			c.Handlers = Handlers{
				VideoSlice: func(VideoSlice) { slicesSeen.Add(1) },
				VideoFrame: func(info wire.VideoInfo, frame []byte) {
					mu.Lock()
					defer mu.Unlock()
					if frameOK(frame, info.FrameID) {
						framesOK++
					} else {
						framesBad++
					}
					if info.Keyframe {
						keyframesRx++
					}
				},
				Audio: func(h wire.AudioHeader, opus []byte) {
					mu.Lock()
					audioRx++
					mu.Unlock()
				},
				Rumble: func(wire.Rumble) {
					mu.Lock()
					rumbleRx++
					mu.Unlock()
				},
			}
		},
	})
	host, client := p.host, p.client

	if host.Identity() != "paired:mac" {
		t.Fatalf("host identity %q", host.Identity())
	}
	if client.PeerFingerprint() != CertFingerprint(p.hostCert) {
		t.Fatal("client recorded the wrong host fingerprint")
	}
	if host.Config().Slices != 4 || client.ShardSize() != wire.MaxDatagram-AEADOverhead-wire.VideoHeaderLen {
		t.Fatalf("negotiated config %+v shard %d", host.Config(), client.ShardSize())
	}
	startTarget := host.TargetBitrateKbps()

	const inputs = 100
	inputDone := make(chan error, 1)
	go func() {
		for i := 1; i <= inputs; i++ {
			in := wire.Input{Type: wire.InputMouseRel, Seq: uint32(i), Timestamp: Mono(), X: int32(i), Y: -1}
			if err := client.SendInput(in); err != nil {
				inputDone <- err
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		inputDone <- nil
	}()

	const frames = 360
	keyframesTx := 0
	audioTx := 0
	buf := make([]byte, 256<<10)
	opus := make([]byte, 160)
	interval := time.Second / 120
	next := time.Now()
	for id := uint32(1); id <= frames; id++ {
		size := max(host.TargetBitrateKbps()*1000/8/120, 4000)
		frame := buf[:size]
		fillFrame(frame, id)
		info := wire.VideoInfo{FrameID: id, CaptureTS: Mono(), SliceCount: 4}
		if id == 1 || host.KeyframeRequested() {
			info.Keyframe = true
			keyframesTx++
		}
		info.EncodeTS = Mono()
		if err := host.SendVideoFrame(info, frame); err != nil {
			t.Fatalf("frame %d: %v", id, err)
		}
		if id%2 == 0 {
			audioTx++
			if err := host.SendAudio(uint32(audioTx), Mono(), opus); err != nil {
				t.Fatal(err)
			}
		}
		if id%60 == 0 {
			if err := host.SendRumble(wire.Rumble{Seq: id, Low: 1000, High: 2000, DurationMs: 50}); err != nil {
				t.Fatal(err)
			}
		}
		next = next.Add(interval)
		time.Sleep(time.Until(next))
	}
	if err := <-inputDone; err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	hs, cs := host.Stats(), client.Stats()
	t.Logf("frames ok %d bad %d of %d; keyframes tx %d rx %d; audio %d/%d; rumble %d; input %d/%d",
		framesOK, framesBad, frames, keyframesTx, keyframesRx, audioRx, audioTx, rumbleRx, len(inputSeqs), inputs)
	t.Logf("host: %+v", hs)
	t.Logf("client: %+v", cs)
	t.Logf("target %d -> %d kbps, FEC loss estimate %.4f, rtt %v, clock offset %d ns, slices %d",
		startTarget, host.TargetBitrateKbps(), host.FECLoss(), client.RTT(), client.Clock().Offset(), slicesSeen.Load())

	if framesBad != 0 {
		t.Errorf("%d frames arrived corrupted", framesBad)
	}
	if framesOK < frames*90/100 {
		t.Errorf("only %d of %d frames delivered", framesOK, frames)
	}
	if hs.SimulatedDrops == 0 {
		t.Error("loss simulation dropped nothing")
	}
	if cs.SlicesRecovered == 0 {
		t.Error("FEC recovered no slices under 2% loss")
	}
	if l := host.FECLoss(); l < 0.005 || l > 0.06 {
		t.Errorf("host loss estimate %.4f, want near 2%%", l)
	}
	if cs.PLIsSent == 0 || hs.PLIsReceived == 0 || keyframesTx < 2 || keyframesRx < 1 {
		t.Errorf("PLI loop: client sent %d, host got %d, keyframes tx %d rx %d", cs.PLIsSent, hs.PLIsReceived, keyframesTx, keyframesRx)
	}
	if got := host.TargetBitrateKbps(); got == startTarget || got > 50_000 {
		t.Errorf("rate target %d kbps (start %d): want adapted and at most the 50000 negotiated", got, startTarget)
	}
	if hs.FeedbackReceived < frames/2 {
		t.Errorf("host got %d feedback datagrams", hs.FeedbackReceived)
	}
	if audioRx < audioTx*90/100 || rumbleRx == 0 {
		t.Errorf("audio %d/%d, rumble %d", audioRx, audioTx, rumbleRx)
	}
	if len(inputSeqs) < inputs*98/100 {
		t.Errorf("host got %d of %d input events", len(inputSeqs), inputs)
	}
	if rtt := client.RTT(); rtt <= 0 || rtt > 50*time.Millisecond || host.RTT() <= 0 {
		t.Errorf("rtt client %v host %v", rtt, host.RTT())
	}
	if off := client.Clock().Offset(); off < -int64(2*time.Millisecond) || off > int64(2*time.Millisecond) {
		t.Errorf("clock offset %d ns between two clocks of one process", off)
	}
	if hs.AuthFailures != 0 || cs.AuthFailures != 0 {
		t.Errorf("auth failures host %d client %d", hs.AuthFailures, cs.AuthFailures)
	}

	host.Close()
	select {
	case <-client.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not notice the host closing")
	}
}

func TestDialRejectsUnpinnedHost(t *testing.T) {
	hostCert, clientCert := identities(t)
	sv := startServer(t, pinServerConfig(hostCert))
	go sv.Accept(context.Background())
	_, err := Dial(context.Background(), sv.Addr().String(), ClientConfig{
		Certificate:      clientCert,
		VerifyHost:       PinnedHost([32]byte{1}),
		Auth:             wire.AuthPIN,
		PIN:              testPIN,
		HandshakeTimeout: 3 * time.Second,
	})
	if err == nil || !errors.Is(err, ErrHostNotTrusted) && !strings.Contains(err.Error(), ErrHostNotTrusted.Error()) {
		t.Fatalf("dial with wrong pin: %v, want ErrHostNotTrusted", err)
	}
}

func TestWrongPINDenied(t *testing.T) {
	hostCert, clientCert := identities(t)
	sv := startServer(t, pinServerConfig(hostCert))
	accepted := make(chan error, 1)
	go func() {
		_, err := sv.Accept(context.Background())
		accepted <- err
	}()
	_, err := Dial(context.Background(), sv.Addr().String(), ClientConfig{
		Certificate: clientCert,
		VerifyHost:  PinnedHost(CertFingerprint(hostCert)),
		Auth:        wire.AuthPIN,
		PIN:         "0000",
	})
	if !errors.Is(err, ErrAuthDenied) {
		t.Fatalf("client: %v, want ErrAuthDenied", err)
	}
	var he *HandshakeError
	if err := <-accepted; !errors.As(err, &he) || !errors.Is(err, ErrAuthDenied) {
		t.Fatalf("server: %v, want a HandshakeError wrapping ErrAuthDenied", err)
	}
}

func TestPairedFingerprintAuth(t *testing.T) {
	hostCert, clientCert := identities(t)
	_, stranger := identities(t)
	known := CertFingerprint(clientCert)
	cfg := pinServerConfig(hostCert)
	cfg.Authorize = func(_ context.Context, info AuthInfo) (string, error) {
		if info.Method == wire.AuthPaired && info.ClientFingerprint == known {
			return "known-device", nil
		}
		return "", errors.New("unknown device")
	}
	sv := startServer(t, cfg)
	go func() {
		for {
			s, err := sv.Accept(context.Background())
			var he *HandshakeError
			if errors.As(err, &he) {
				continue
			}
			if err != nil {
				return
			}
			t.Cleanup(func() { s.Close() })
		}
	}()
	dial := func(cert tls.Certificate) (*Session, error) {
		return Dial(context.Background(), sv.Addr().String(), ClientConfig{
			Certificate: cert,
			VerifyHost:  PinnedHost(CertFingerprint(hostCert)),
			Session:     wire.SessionConfig{Codec: wire.CodecH264, Slices: 1},
		})
	}
	s, err := dial(clientCert)
	if err != nil {
		t.Fatalf("paired device refused: %v", err)
	}
	if s.Identity() != "known-device" {
		t.Fatalf("identity %q", s.Identity())
	}
	s.Close()
	if _, err := dial(stranger); !errors.Is(err, ErrAuthDenied) {
		t.Fatalf("unpaired device: %v, want ErrAuthDenied", err)
	}
}

func TestHostWithoutPINIsDetected(t *testing.T) {
	hostCert, clientCert := identities(t)
	cfg := pinServerConfig(hostCert)
	cfg.Authorize = func(context.Context, AuthInfo) (string, error) { return "anyone", nil }
	cfg.PIN = nil
	sv := startServer(t, cfg)
	go sv.Accept(context.Background())
	_, err := Dial(context.Background(), sv.Addr().String(), ClientConfig{
		Certificate: clientCert,
		VerifyHost:  func([32]byte) error { return nil },
		Auth:        wire.AuthPIN,
		PIN:         testPIN,
	})
	if !errors.Is(err, ErrHostPINMismatch) {
		t.Fatalf("host that accepted without proving the PIN: %v, want ErrHostPINMismatch", err)
	}
}

func TestClientTLSConfigRequiresVerifier(t *testing.T) {
	_, c := identities(t)
	if _, err := ClientTLSConfig(c, nil); err == nil {
		t.Fatal("ClientTLSConfig accepted a nil verifier")
	}
}

func TestClockEstimator(t *testing.T) {
	var c ClockEstimator
	const offset = int64(5_000_000)
	for i, oneWay := range []int64{3e6, 1e6, 2e6} {
		t0 := int64(i) * 1e9
		t1 := t0 + oneWay + offset
		t2 := t1 + 100_000
		t3 := t2 - offset + oneWay
		c.Add(wire.Pong{Seq: uint32(i), T0: t0, T1: t1, T2: t2}, t3)
	}
	if c.MinRTT() != 2*time.Millisecond || c.Offset() != offset || c.ToLocal(offset+7) != 7 {
		t.Fatalf("min rtt %v offset %d", c.MinRTT(), c.Offset())
	}
	if c.RTT() <= 2*time.Millisecond || c.RTT() >= 6*time.Millisecond {
		t.Fatalf("smoothed rtt %v", c.RTT())
	}
}

// rawMediaPath is the bare data plane: seal, unconnected UDP write, read,
// open.
type rawMediaPath struct {
	a, b    *net.UDPConn
	dst     netip.AddrPort
	sealer  *Sealer
	opener  *Opener
	tx, rx  []byte
	payload int
}

func newRawMediaPath(tb testing.TB, payload int) *rawMediaPath {
	a, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	b, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { a.Close(); b.Close() })
	k := DirectionKey{Suite: SuiteAES128GCM, Key: make([]byte, 16)}
	sealer, _ := NewSealer(k)
	opener, _ := NewOpener(k)
	return &rawMediaPath{
		a: a, b: b, dst: b.LocalAddr().(*net.UDPAddr).AddrPort(),
		sealer: sealer, opener: opener,
		tx: make([]byte, wire.MaxDatagram), rx: make([]byte, 2048), payload: payload,
	}
}

func (p *rawMediaPath) roundTrip(tb testing.TB) {
	pkt := p.tx[:AEADHeaderLen+p.payload]
	sealed := p.sealer.Seal(pkt, pkt[AEADHeaderLen:])
	if _, err := p.a.WriteToUDPAddrPort(sealed, p.dst); err != nil {
		tb.Fatal(err)
	}
	n, _, err := p.b.ReadFromUDPAddrPort(p.rx)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := p.opener.Open(p.rx[:n]); err != nil {
		tb.Fatal(err)
	}
}

func TestRawMediaPathDoesNotAllocate(t *testing.T) {
	p := newRawMediaPath(t, wire.MaxDatagram-AEADOverhead)
	p.roundTrip(t)
	if allocs := testing.AllocsPerRun(1000, func() { p.roundTrip(t) }); allocs != 0 {
		t.Fatalf("seal+send+recv+open allocates %.2f per packet", allocs)
	}
}

// BenchmarkRawMediaPath is one full-size media datagram through seal, UDP
// send, UDP receive and open on loopback.
func BenchmarkRawMediaPath(b *testing.B) {
	p := newRawMediaPath(b, wire.MaxDatagram-AEADOverhead)
	b.ReportAllocs()
	b.SetBytes(int64(wire.MaxDatagram))
	for i := 0; i < b.N; i++ {
		p.roundTrip(b)
	}
}

// BenchmarkSessionVideo sends 78 KB frames (4 slices) host to client
// through a live session and reports process-wide allocations per media
// datagram, including the receive goroutine's reassembly.
func BenchmarkSessionVideo(b *testing.B) {
	for _, loss := range []float64{0, 0.02} {
		b.Run(fmt.Sprintf("loss%.0fpct", loss*100), func(b *testing.B) {
			var got atomic.Int64
			p := newPair(b, pairOpts{
				server: func(c *ServerConfig) { c.SimulateLoss = loss },
				client: func(c *ClientConfig) {
					c.Handlers.VideoFrame = func(wire.VideoInfo, []byte) { got.Add(1) }
				},
			})
			frame := make([]byte, 78_000)
			send := func(id uint32) {
				info := wire.VideoInfo{FrameID: id, SliceCount: 4}
				if err := p.host.SendVideoFrame(info, frame); err != nil {
					b.Fatal(err)
				}
				deadline := time.Now().Add(20 * time.Millisecond)
				for got.Load() < int64(id)-fec.ReassemblyWindow && time.Now().Before(deadline) {
					runtime.Gosched()
				}
			}
			for i := uint32(1); i <= 64; i++ {
				send(i)
			}
			before := p.host.Stats().PacketsSent
			var ms0, ms1 runtime.MemStats
			runtime.ReadMemStats(&ms0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				send(uint32(65 + i))
			}
			b.StopTimer()
			runtime.ReadMemStats(&ms1)
			pkts := p.host.Stats().PacketsSent - before
			b.ReportMetric(float64(pkts)/float64(b.N), "pkts/frame")
			b.ReportMetric(float64(ms1.Mallocs-ms0.Mallocs)/float64(pkts), "allocs/pkt")
		})
	}
}
