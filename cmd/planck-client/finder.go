package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/tailscale"
)

// tailnet is the part of *tailscale.Client the finder uses.
type tailnet interface {
	Status(ctx context.Context) (*tailscale.Status, error)
}

const (
	refreshEvery = 10 * time.Second
	probeTimeout = 2 * time.Second
	// onlineFor is how long a host counts as online after it was last
	// heard: LAN beacons come every 2 s, tailnet probes every refreshEvery.
	onlineFor = 3 * refreshEvery / 2
)

// finder keeps the hosts heard on the LAN, found on the tailnet, or
// probed at fixed discovery addresses, with every path to each.
type finder struct {
	// lanAddr is where LAN beacons are heard; empty listens nowhere.
	lanAddr string
	ts      tailnet
	// probe are discovery addresses probed on each refresh.
	probe []netip.AddrPort
	logf  func(string, ...any)

	refresh chan struct{}

	mu       sync.Mutex
	hosts    map[string]*found
	status   *tailscale.Status
	onChange func()
}

// found is one host: its last beacon and the paths it answered on.
type found struct {
	beacon discovery.Beacon
	paths  map[netip.AddrPort]discovery.Announcement
	seen   time.Time
}

func newFinder(lanAddr string, ts tailnet, probe []netip.AddrPort, logf func(string, ...any)) *finder {
	return &finder{lanAddr: lanAddr, ts: ts, probe: probe, logf: logf, refresh: make(chan struct{}, 1), hosts: map[string]*found{}}
}

// subscribe sets what runs whenever the hosts or the tailnet change.
func (f *finder) subscribe(fn func()) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

func (f *finder) changed() {
	f.mu.Lock()
	fn := f.onChange
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// run listens and probes until ctx ends.
func (f *finder) run(ctx context.Context) {
	if f.lanAddr != "" {
		conn, err := net.ListenPacket("udp4", f.lanAddr)
		if err != nil {
			// A host on this machine holds the port; probes still find it.
			f.logf("planck: not listening for LAN beacons on %s: %v", f.lanAddr, err)
		} else {
			go func() {
				_ = discovery.Listen(ctx, conn, f.add)
				conn.Close()
			}()
		}
	}
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		f.probeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-f.refresh:
		}
	}
}

// Refresh probes again soon.
func (f *finder) Refresh() {
	select {
	case f.refresh <- struct{}{}:
	default:
	}
}

func (f *finder) probeAll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, addr := range f.probe {
		wg.Go(func() {
			b, rtt, err := discovery.Probe(ctx, addr)
			if err == nil {
				f.add(discovery.Announcement{Beacon: b, Addr: addr, Kind: kindOf(addr.Addr()), RTT: rtt})
			}
		})
	}
	if f.ts != nil {
		wg.Go(func() {
			st, err := f.ts.Status(ctx)
			if err != nil {
				st = nil
			}
			f.mu.Lock()
			f.status = st
			f.mu.Unlock()
			if st == nil {
				f.changed()
				return
			}
			found, _ := discovery.ProbeTailscale(ctx, fixedStatus{st}, discovery.DefaultPort)
			for _, a := range found {
				f.add(a)
			}
			f.changed()
		})
	}
	wg.Wait()
}

// kindOf classifies a probed address: tailnet addresses as Tailscale
// direct, which the race and the client then check, the rest as LAN.
func kindOf(ip netip.Addr) discovery.PathKind {
	if tailscaleRange.Contains(ip.Unmap()) || tailscaleRange6.Contains(ip) {
		return discovery.PathTailscaleDirect
	}
	return discovery.PathLANDirect
}

var (
	tailscaleRange  = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleRange6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

type fixedStatus struct{ st *tailscale.Status }

func (s fixedStatus) Status(context.Context) (*tailscale.Status, error) { return s.st, nil }

func (f *finder) add(a discovery.Announcement) {
	if a.Beacon.HostID == "" {
		return
	}
	f.mu.Lock()
	h := f.hosts[a.Beacon.HostID]
	if h == nil {
		h = &found{paths: map[netip.AddrPort]discovery.Announcement{}}
		f.hosts[a.Beacon.HostID] = h
	}
	_, known := h.paths[a.Addr]
	h.beacon, h.seen = a.Beacon, time.Now()
	h.paths[a.Addr] = a
	fn := f.onChange
	f.mu.Unlock()
	if !known && fn != nil {
		fn()
	}
}

// hostView is a found host as the client shows and dials it.
type hostView struct {
	Beacon discovery.Beacon
	Online bool
	// Best is the path a race would try first: the best kind, then the
	// lowest round trip.
	Best       discovery.Announcement
	Candidates []discovery.Candidate
	// SameUser tells that the host is a device of this machine's own
	// Tailscale user.
	SameUser bool
}

// list returns every host found, by name.
func (f *finder) list() []hostView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hostView, 0, len(f.hosts))
	for _, h := range f.hosts {
		out = append(out, f.view(h))
	}
	slices.SortFunc(out, func(a, b hostView) int {
		return strings.Compare(a.Beacon.Name+a.Beacon.HostID, b.Beacon.Name+b.Beacon.HostID)
	})
	return out
}

func (f *finder) view(h *found) hostView {
	v := hostView{Beacon: h.beacon, Online: time.Since(h.seen) < onlineFor}
	first := true
	for _, a := range h.paths {
		v.Candidates = append(v.Candidates, a.Candidate())
		if first || a.Kind < v.Best.Kind || a.Kind == v.Best.Kind && a.RTT < v.Best.RTT {
			v.Best, first = a, false
		}
		if f.sameUser(a.Addr.Addr()) {
			v.SameUser = true
		}
	}
	slices.SortFunc(v.Candidates, func(a, b discovery.Candidate) int { return a.Addr.Compare(b.Addr) })
	return v
}

func (f *finder) sameUser(ip netip.Addr) bool {
	st := f.status
	if st == nil {
		return false
	}
	for _, p := range st.Peers {
		if slices.Contains(p.TailscaleIPs, ip.Unmap()) {
			return p.UserID == st.Self.UserID
		}
	}
	return false
}

// lookup returns the host with this ID.
func (f *finder) lookup(id string) (hostView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[id]
	if !ok {
		return hostView{}, false
	}
	return f.view(h), true
}

// tailnetStatus is the last status the tailnet gave, nil when it is not
// running.
func (f *finder) tailnetStatus() *tailscale.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

var errHostNotFound = errors.New("host not found")

// find waits until a host whose name or ID is query has answered on a
// path that keep accepts, then gives the other paths settle to answer.
func (f *finder) find(ctx context.Context, query string, keep func(discovery.PathKind) bool, settle time.Duration) (hostView, error) {
	var deadline <-chan time.Time
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, ok := f.match(query, keep); ok && deadline == nil {
			deadline = time.After(settle)
		}
		select {
		case <-ctx.Done():
			return hostView{}, fmt.Errorf("%w: %q: %v", errHostNotFound, query, ctx.Err())
		case <-deadline:
			v, _ := f.match(query, keep)
			return v, nil
		case <-tick.C:
		}
	}
}

func (f *finder) match(query string, keep func(discovery.PathKind) bool) (hostView, bool) {
	for _, v := range f.list() {
		if !strings.EqualFold(v.Beacon.Name, query) && v.Beacon.HostID != query {
			continue
		}
		var cands []discovery.Candidate
		for _, c := range v.Candidates {
			if keep(c.Kind) {
				cands = append(cands, c)
			}
		}
		if len(cands) > 0 {
			v.Candidates = cands
			return v, true
		}
	}
	return hostView{}, false
}

// identify makes a target of an address typed in. A host that answers
// discovery on its IP says who it is, so the pin keys its ID and the
// pairing screen shows its fingerprint; the name is the host's, or the
// address.
func identify(ctx context.Context, addr string) (session.Target, string) {
	addr = withDefaultPort(strings.TrimSpace(addr))
	t := session.Target{Addr: addr}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return t, addr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return t, addr
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	b, _, err := discovery.Probe(ctx, netip.AddrPortFrom(ip, discovery.DefaultPort))
	if err != nil {
		return t, addr
	}
	t.HostID, t.Fingerprint = b.HostID, b.CertFingerprint
	t.Addr = netip.AddrPortFrom(ip, b.ControlPort).String()
	return t, b.Name
}
