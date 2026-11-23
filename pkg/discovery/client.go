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

	"github.com/watzon/planck/pkg/tailscale"
	"github.com/watzon/planck/pkg/transport"
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
	var mu sync.Mutex
	sent := map[uint64]int64{}
	sendOne := func() error {
		nonce := rand.Uint64() | 1
		pkt, err := encode(message{Type: msgProbe, Nonce: nonce})
		if err != nil {
			return err
		}
		mu.Lock()
		sent[nonce] = transport.Mono()
		mu.Unlock()
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
		mu.Lock()
		at, ok := sent[m.Nonce]
		mu.Unlock()
		if ok {
			return *m.Beacon, time.Duration(transport.Mono() - at), nil
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
