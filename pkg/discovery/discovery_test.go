package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/tailscale"
)

var testBeacon = Beacon{
	HostID:          "host-1",
	Name:            "bigboi",
	ControlPort:     47995,
	CertFingerprint: strings.Repeat("ab", 32),
	OS:              "windows",
	GPUs:            []string{"NVIDIA GeForce RTX 4080", "AMD Radeon RX 6700 XT"},
	TailscaleIP:     "100.104.23.37",
}

func listenLoopback(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func startHost(t *testing.T, b Beacon) netip.AddrPort {
	t.Helper()
	conn := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, conn, b) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Serve returned %v, want context.Canceled", err)
		}
	})
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

func deadUDPAddr(t *testing.T) netip.AddrPort {
	t.Helper()
	c := listenLoopback(t)
	ap := c.LocalAddr().(*net.UDPAddr).AddrPort()
	c.Close()
	return ap
}

func TestMessageRoundTripAndRejects(t *testing.T) {
	pkt, err := encode(message{Type: msgReply, Nonce: 9, Beacon: &testBeacon})
	if err != nil {
		t.Fatal(err)
	}
	m, err := decode(pkt)
	if err != nil || m.Nonce != 9 || m.Beacon.Name != "bigboi" || len(m.Beacon.GPUs) != 2 {
		t.Fatalf("decode = %+v, %v", m, err)
	}

	for name, raw := range map[string]string{
		"garbage":         "\x00\x01hello",
		"other version":   `{"v":"planck-disc-v2","t":"probe","n":1}`,
		"unknown type":    `{"v":"planck-disc-v1","t":"hello"}`,
		"reply no beacon": `{"v":"planck-disc-v1","t":"reply","n":1}`,
		"beacon no id":    `{"v":"planck-disc-v1","t":"beacon","b":{"name":"x"}}`,
		"oversized":       `{"v":"planck-disc-v1","t":"probe","x":"` + strings.Repeat("a", maxMessageLen) + `"}`,
	} {
		if _, err := decode([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	big := testBeacon
	big.GPUs = []string{strings.Repeat("g", maxMessageLen)}
	if _, err := encode(message{Type: msgBeacon, Beacon: &big}); err == nil {
		t.Fatal("encoded a beacon larger than one datagram")
	}
}

func TestProbeLoopback(t *testing.T) {
	addr := startHost(t, testBeacon)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, rtt, err := Probe(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if b.HostID != testBeacon.HostID || b.CertFingerprint != testBeacon.CertFingerprint || rtt <= 0 || rtt > time.Second {
		t.Fatalf("beacon %+v rtt %v", b, rtt)
	}
}

func TestProbeNoHostTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, _, err := Probe(ctx, deadUDPAddr(t)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestAnnounceListen(t *testing.T) {
	listener := listenLoopback(t)
	sender := listenLoopback(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got := make(chan Announcement, 4)
	go Listen(ctx, listener, func(a Announcement) { got <- a })
	go Announce(ctx, sender, testBeacon, []netip.AddrPort{listener.LocalAddr().(*net.UDPAddr).AddrPort()}, 20*time.Millisecond)

	select {
	case a := <-got:
		want := sender.LocalAddr().(*net.UDPAddr).AddrPort()
		if a.Kind != PathLANDirect || a.Addr != want || a.Beacon.HostID != "host-1" {
			t.Fatalf("announcement = %+v, want from %v", a, want)
		}
		if c := a.Candidate(); c.HostID != "host-1" || c.Addr != want || c.Kind != PathLANDirect {
			t.Fatalf("candidate = %+v", c)
		}
	case <-ctx.Done():
		t.Fatal("no beacon heard")
	}
}

func TestDirectedBroadcast(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.188.165/24": "192.168.188.255",
		"10.1.2.3/8":         "10.255.255.255",
		"172.16.5.4/20":      "172.16.15.255",
	} {
		if got := directedBroadcast(netip.MustParsePrefix(in)); got.String() != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, ap := range BroadcastAddrs(DefaultPort) {
		if !ap.Addr().Is4() || ap.Port() != DefaultPort {
			t.Errorf("bad broadcast addr %v", ap)
		}
	}
}

type fakeStatus struct{ st *tailscale.Status }

func (f fakeStatus) Status(context.Context) (*tailscale.Status, error) { return f.st, nil }

func TestProbeTailscale(t *testing.T) {
	host := startHost(t, testBeacon)
	lo := host.Addr()
	src := fakeStatus{&tailscale.Status{Peers: []tailscale.Node{
		{HostName: "bigboi", Online: true, Direct: true, TailscaleIPs: []netip.Addr{netip.MustParseAddr("fd7a:115c:a1e0::1"), lo}},
		{HostName: "offline", Online: false, TailscaleIPs: []netip.Addr{lo}},
		{HostName: "phone", Online: true, TailscaleIPs: nil},
	}}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	found, err := ProbeTailscale(ctx, src, host.Port())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("found %d hosts, want 1: %+v", len(found), found)
	}
	if f := found[0]; f.Kind != PathTailscaleDirect || f.Addr != host || f.Beacon.HostID != "host-1" {
		t.Fatalf("found = %+v", f)
	}

	src.st.Peers[0].Direct = false
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	found, err = ProbeTailscale(ctx2, src, host.Port())
	if err != nil || len(found) != 1 || found[0].Kind != PathTailscaleDERP {
		t.Fatalf("relayed peer: %+v, %v", found, err)
	}

	if _, err := ProbeTailscale(context.Background(), src, host.Port()); err == nil {
		t.Fatal("ProbeTailscale without a deadline did not fail")
	}
}

func TestRaceLoopbackLANWins(t *testing.T) {
	lan := startHost(t, testBeacon)
	ts := startHost(t, testBeacon)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := Racer{}.Race(ctx, []Candidate{
		{HostID: "host-1", Kind: PathTailscaleDirect, Addr: ts},
		{HostID: "host-1", Kind: PathLANDirect, Addr: lan},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != PathLANDirect || res.Addr != lan || res.SafeShardPayload != 1152 ||
		res.RecommendedMaxBitrateKbps != LANMaxBitrateKbps || res.ControlAddr != netip.AddrPortFrom(lan.Addr(), 47995) || res.RTT <= 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRaceLoopbackFallsBackToDERP(t *testing.T) {
	derp := startHost(t, testBeacon)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	res, err := Racer{HeadStart: 40 * time.Millisecond}.Race(ctx, []Candidate{
		{Kind: PathLANDirect, Addr: deadUDPAddr(t)},
		{Kind: PathTailscaleDirect, Addr: deadUDPAddr(t)},
		{Kind: PathTailscaleDERP, Addr: derp},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != PathTailscaleDERP || res.RecommendedMaxBitrateKbps != 20_000 {
		t.Fatalf("result = %+v", res)
	}
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("DERP won after %v, before both head starts elapsed", el)
	}
}

// scriptedProbe answers each address after a fixed delay, or fails.
type scriptedProbe struct {
	mu      sync.Mutex
	started map[netip.AddrPort]time.Duration
	t0      time.Time
	delay   map[netip.AddrPort]time.Duration
	fail    map[netip.AddrPort]bool
	beacon  map[netip.AddrPort]Beacon
}

func newScripted() *scriptedProbe {
	return &scriptedProbe{
		started: map[netip.AddrPort]time.Duration{},
		t0:      time.Now(),
		delay:   map[netip.AddrPort]time.Duration{},
		fail:    map[netip.AddrPort]bool{},
		beacon:  map[netip.AddrPort]Beacon{},
	}
}

func (s *scriptedProbe) startedAt(a netip.AddrPort) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.started[a]
	return d, ok
}

func (s *scriptedProbe) probe(ctx context.Context, a netip.AddrPort) (Beacon, time.Duration, error) {
	s.mu.Lock()
	s.started[a] = time.Since(s.t0)
	d, fail := s.delay[a], s.fail[a]
	b, ok := s.beacon[a]
	s.mu.Unlock()
	if !ok {
		b = testBeacon
	}
	if fail {
		return Beacon{}, 0, errors.New("unreachable")
	}
	select {
	case <-time.After(d):
		return b, d, nil
	case <-ctx.Done():
		return Beacon{}, 0, ctx.Err()
	}
}

var (
	addrLAN  = netip.MustParseAddrPort("192.168.188.126:47997")
	addrTS   = netip.MustParseAddrPort("100.104.23.37:47997")
	addrDERP = netip.MustParseAddrPort("100.104.23.38:47997")
	allKinds = []Candidate{
		{HostID: "host-1", Kind: PathTailscaleDERP, Addr: addrDERP},
		{HostID: "host-1", Kind: PathTailscaleDirect, Addr: addrTS},
		{HostID: "host-1", Kind: PathLANDirect, Addr: addrLAN},
	}
)

func raceScripted(t *testing.T, s *scriptedProbe, cands []Candidate) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := Racer{HeadStart: 50 * time.Millisecond, Probe: s.probe}.Race(ctx, cands)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRaceLANSlowerButInsideHeadStartWins(t *testing.T) {
	s := newScripted()
	s.delay[addrLAN] = 30 * time.Millisecond
	res := raceScripted(t, s, allKinds)
	if res.Kind != PathLANDirect {
		t.Fatalf("winner %s, want lan-direct", res.Kind)
	}
	if _, started := s.startedAt(addrTS); started {
		t.Fatal("tailscale probe started before the LAN head start ran out")
	}
}

func TestRaceTailscaleBeatsSlowLAN(t *testing.T) {
	s := newScripted()
	s.delay[addrLAN] = 500 * time.Millisecond
	res := raceScripted(t, s, allKinds)
	if res.Kind != PathTailscaleDirect || res.RecommendedMaxBitrateKbps != 100_000 {
		t.Fatalf("result = %+v", res)
	}
	if st, _ := s.startedAt(addrTS); st < 45*time.Millisecond {
		t.Fatalf("tailscale probe started at %v, before the 50ms head start", st)
	}
}

func TestRaceFailedTierPromotesNextImmediately(t *testing.T) {
	s := newScripted()
	s.fail[addrLAN] = true
	start := time.Now()
	res := raceScripted(t, s, allKinds)
	if res.Kind != PathTailscaleDirect {
		t.Fatalf("winner %s", res.Kind)
	}
	if el := time.Since(start); el > 40*time.Millisecond {
		t.Fatalf("took %v; a failed LAN tier should not hold back tailscale", el)
	}
}

func TestRaceRejectsWrongHost(t *testing.T) {
	s := newScripted()
	other := testBeacon
	other.HostID = "someone-else"
	s.beacon[addrLAN] = other
	res := raceScripted(t, s, allKinds)
	if res.Kind != PathTailscaleDirect || res.Beacon.HostID != "host-1" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRaceAllFail(t *testing.T) {
	s := newScripted()
	s.fail[addrLAN], s.fail[addrTS], s.fail[addrDERP] = true, true, true
	_, err := Racer{Probe: s.probe}.Race(context.Background(), allKinds)
	if err == nil || !strings.Contains(err.Error(), "every path failed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (Racer{}).Race(context.Background(), nil); err == nil {
		t.Fatal("empty candidate list did not fail")
	}
}
