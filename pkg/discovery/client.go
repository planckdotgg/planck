package discovery

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
)

type PathKind uint8

const (
	PathLANDirect PathKind = iota
	PathTailscaleDirect
	PathTailscaleDERP
)

func (k PathKind) String() string {
	switch k {
	case PathLANDirect:
		return "lan-direct"
	case PathTailscaleDirect:
		return "tailscale-direct"
	case PathTailscaleDERP:
		return "tailscale-derp"
	}
	return fmt.Sprintf("PathKind(%d)", uint8(k))
}

// LANMaxBitrateKbps is the highest rate spike 3 sustained on wired LAN with
// 100% delivery (150 Mbps at 120 and 240 fps).
const LANMaxBitrateKbps = 150_000

// RecommendedMaxBitrateKbps is the bitrate ceiling for a path kind.
func (k PathKind) RecommendedMaxBitrateKbps() int {
	switch k {
	case PathTailscaleDirect:
		return tailscale.DirectMaxBitrateKbps
	case PathTailscaleDERP:
		return tailscale.RelayMaxBitrateKbps
	}
	return LANMaxBitrateKbps
}

// Announcement is a host found by a beacon or a probe. Addr is the host's
// discovery address; the control address is Addr's IP with Beacon.ControlPort.
type Announcement struct {
	Beacon Beacon
	Addr   netip.AddrPort
	Kind   PathKind
	RTT    time.Duration
}

func (a Announcement) Candidate() Candidate {
	return Candidate{HostID: a.Beacon.HostID, Kind: a.Kind, Addr: a.Addr}
}

// Listen reports LAN beacons received on conn until ctx ends. Bind conn to
// DefaultPort on all interfaces to hear broadcasts.
func Listen(ctx context.Context, conn net.PacketConn, found func(Announcement)) error {
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	buf := make([]byte, maxMessageLen+1)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			continue
		}
		m, err := decode(buf[:n])
		if err != nil || m.Type != msgBeacon {
			continue
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		ap := ua.AddrPort()
		found(Announcement{
			Beacon: *m.Beacon,
			Addr:   netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()),
			Kind:   PathLANDirect,
		})
	}
}

const probeRetry = 100 * time.Millisecond

// Probe asks the host at addr for its beacon and measures the round trip.
// It resends every 100 ms until a reply arrives or ctx ends. Replies are
// matched by nonce, not source address: a multi-homed host may answer from
// another interface. RTT uses transport.Mono because Go's monotonic clock on
// Windows steps in about 0.5 ms, which reads a LAN round trip as zero.
func Probe(ctx context.Context, addr netip.AddrPort) (Beacon, time.Duration, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	network := "udp4"
	if addr.Addr().Is6() && !addr.Addr().Is4In6() {
		network = "udp6"
	}
	conn, err := net.ListenUDP(network, nil)
	if err != nil {
		return Beacon{}, 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()

	dst := net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()))
	sent := newProbeSet()
	defer sent.forget()
	sendOne := func() error {
		nonce := rand.Uint64() | 1
		pkt, err := encode(message{Type: msgProbe, Nonce: nonce})
		if err != nil {
			return err
		}
		if !sent.add(nonce) {
			return net.ErrClosed
		}
		_, err = conn.WriteTo(pkt, dst)
		return err
	}
	if err := sendOne(); err != nil {
		return Beacon{}, 0, err
	}
	resend := time.NewTicker(probeRetry)
	defer resend.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-resend.C:
				if sendOne() != nil {
					return
				}
			}
		}
	}()

	buf := make([]byte, maxMessageLen+1)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return Beacon{}, 0, ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return Beacon{}, 0, err
			}
			continue
		}
		m, err := decode(buf[:n])
		if err != nil || m.Type != msgReply {
			continue
		}
		if at, ok := sent.at(m.Nonce); ok {
			return *m.Beacon, time.Duration(transport.Mono() - at), nil
		}
	}
}

// ownProbes are the nonces of the probes this process has out, which a
// Port with no host on it hears without yielding (Port.yield): the
// client's own LAN probes loop back to its port.
var ownProbes sync.Map

// probeSet is the probes one call has out, by nonce, with when each went.
type probeSet struct {
	mu   sync.Mutex
	sent map[uint64]int64
	done bool
}

func newProbeSet() *probeSet { return &probeSet{sent: map[uint64]int64{}} }

// add records a probe about to go, unless the call has ended.
func (s *probeSet) add(nonce uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	s.sent[nonce] = transport.Mono()
	ownProbes.Store(nonce, struct{}{})
	return true
}

func (s *probeSet) at(nonce uint64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.sent[nonce]
	return at, ok
}

func (s *probeSet) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	for n := range s.sent {
		ownProbes.Delete(n)
	}
}

// ProbeAll probes every address in dst at once, such as the LAN's
// BroadcastAddrs, and reports each reply until ctx ends: every host that
// answers, from the address it answered from. Each probe goes out three
// times, as broadcasts are easily lost on Wi-Fi.
func ProbeAll(ctx context.Context, dst []netip.AddrPort, found func(Announcement)) error {
	if len(dst) == 0 {
		return nil
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()

	sent := newProbeSet()
	defer sent.forget()
	go func() {
		for i := 0; i < 3; i++ {
			for _, d := range dst {
				nonce := rand.Uint64() | 1
				pkt, err := encode(message{Type: msgProbe, Nonce: nonce})
				if err != nil {
					return
				}
				if !sent.add(nonce) {
					return
				}
				conn.WriteTo(pkt, net.UDPAddrFromAddrPort(d))
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(i+1) * probeRetry):
			}
		}
	}()

	buf := make([]byte, maxMessageLen+1)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		m, err := decode(buf[:n])
		if err != nil || m.Type != msgReply {
			continue
		}
		if at, ok := sent.at(m.Nonce); ok {
			found(Announcement{Beacon: *m.Beacon, Addr: netip.AddrPortFrom(from.Addr().Unmap(), from.Port()),
				Kind: PathLANDirect, RTT: time.Duration(transport.Mono() - at)})
		}
	}
}

// StatusSource is satisfied by *tailscale.Client.
type StatusSource interface {
	Status(ctx context.Context) (*tailscale.Status, error)
}

// ProbeTailscale probes every online tailnet peer on port and returns the
// Planck hosts that answered before ctx ended, fastest first. Peers that do
// not answer are not errors; most tailnet devices are not Planck hosts, so
// ctx must carry a deadline.
func ProbeTailscale(ctx context.Context, ts StatusSource, port uint16) ([]Announcement, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("discovery: ProbeTailscale needs a context deadline")
	}
	st, err := ts.Status(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu    sync.Mutex
		found []Announcement
		wg    sync.WaitGroup
	)
	for _, p := range st.Peers {
		ip, ok := preferIPv4(p.TailscaleIPs)
		if !p.Online || !ok {
			continue
		}
		kind := PathTailscaleDERP
		if p.Direct {
			kind = PathTailscaleDirect
		}
		addr := netip.AddrPortFrom(ip, port)
		wg.Go(func() {
			b, rtt, err := Probe(ctx, addr)
			if err != nil {
				return
			}
			mu.Lock()
			found = append(found, Announcement{Beacon: b, Addr: addr, Kind: kind, RTT: rtt})
			mu.Unlock()
		})
	}
	wg.Wait()
	sort.Slice(found, func(i, j int) bool { return found[i].RTT < found[j].RTT })
	return found, nil
}

func preferIPv4(ips []netip.Addr) (netip.Addr, bool) {
	for _, ip := range ips {
		if ip.Is4() {
			return ip, true
		}
	}
	if len(ips) > 0 {
		return ips[0], true
	}
	return netip.Addr{}, false
}
