package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
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
	// lanAddr is where LAN beacons are heard and from where the LAN is
	// probed; empty does neither.
	lanAddr string
	// port is the hosts' discovery port, probed on the LAN and the
	// tailnet.
	port uint16
	ts   tailnet
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
	paths  map[netip.AddrPort]foundPath
	seen   time.Time
}

type foundPath struct {
	discovery.Announcement
	seen time.Time
}

// newFinder listens on lanAddr; the discovery port is lanAddr's, or
// discovery.DefaultPort.
func newFinder(lanAddr string, ts tailnet, probe []netip.AddrPort, logf func(string, ...any)) *finder {
	port := uint16(discovery.DefaultPort)
	if ap, err := netip.ParseAddrPort(lanAddr); err == nil {
		port = ap.Port()
	} else if _, p, err := net.SplitHostPort(lanAddr); err == nil {
		if n, err := strconv.ParseUint(p, 10, 16); err == nil && n > 0 {
			port = uint16(n)
		}
	}
	return &finder{lanAddr: lanAddr, port: port, ts: ts, probe: probe, logf: logf, refresh: make(chan struct{}, 1), hosts: map[string]*found{}}
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
		port, err := discovery.OpenPort(f.lanAddr)
		if err != nil {
			// Another app holds the port alone; probes still find hosts.
			f.logf("planck: not listening for LAN beacons on %s: %v", f.lanAddr, err)
		} else {
			defer port.Close()
			go func() { _ = port.Listen(ctx, f.add) }()
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
	if f.lanAddr != "" {
		dst := discovery.BroadcastAddrs(f.port)
		slices.SortFunc(dst, netip.AddrPort.Compare)
		wg.Go(func() { _ = discovery.ProbeAll(ctx, slices.Compact(dst), f.add) })
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
			found, _ := discovery.ProbeTailscale(ctx, fixedStatus{st}, f.port)
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
	now := time.Now()
	f.mu.Lock()
	h := f.hosts[a.Beacon.HostID]
	if h == nil {
		h = &found{paths: map[netip.AddrPort]foundPath{}}
		f.hosts[a.Beacon.HostID] = h
	}
	old, known := h.paths[a.Addr]
	changed := !known || now.Sub(old.seen) >= onlineFor || !slices.Equal(h.beacon.GPUs, a.Beacon.GPUs) ||
		h.beacon.Name != a.Beacon.Name || h.beacon.OS != a.Beacon.OS || h.beacon.ControlPort != a.Beacon.ControlPort
	if a.RTT == 0 && known {
		// A beacon has no round trip; keep the last probe's.
		a.RTT = old.RTT
	}
	h.beacon, h.seen = a.Beacon, now
	h.paths[a.Addr] = foundPath{a, now}
	fn := f.onChange
	f.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}

// hostView is a found host as the client shows and dials it.
type hostView struct {
	Beacon discovery.Beacon
	Online bool
	// Best is the path a race would try first: the best kind, then the
	// lowest round trip, among the paths heard lately if any.
	Best       discovery.Announcement
	Candidates []discovery.Candidate
	// LAN and Tailscale are the best path of each kind, zero if none;
	// LANOnline and TailscaleOnline say the host answered on it lately.
	LAN, Tailscale             discovery.Announcement
	LANOnline, TailscaleOnline bool
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
	paths := make([]foundPath, 0, len(h.paths))
	for _, p := range h.paths {
		paths = append(paths, p)
	}
	// Online paths first, then by kind, round trip and address, so the
	// choices below do not depend on map order.
	slices.SortFunc(paths, func(a, b foundPath) int {
		ao, bo := time.Since(a.seen) < onlineFor, time.Since(b.seen) < onlineFor
		switch {
		case ao != bo && ao:
			return -1
		case ao != bo:
			return 1
		case a.Kind != b.Kind:
			return int(a.Kind) - int(b.Kind)
		case a.RTT != b.RTT:
			return cmp.Compare(a.RTT, b.RTT)
		}
		return a.Addr.Compare(b.Addr)
	})
	for i, p := range paths {
		online := time.Since(p.seen) < onlineFor
		if i == 0 {
			v.Best = p.Announcement
		}
		v.Candidates = append(v.Candidates, p.Candidate())
		if p.Kind == discovery.PathLANDirect {
			if !v.LAN.Addr.IsValid() {
				v.LAN, v.LANOnline = p.Announcement, online
			}
		} else if !v.Tailscale.Addr.IsValid() {
			v.Tailscale, v.TailscaleOnline = p.Announcement, online
		}
		if f.sameUser(p.Addr.Addr()) {
			v.SameUser = true
		}
	}
	slices.SortFunc(v.Candidates, func(a, b discovery.Candidate) int { return a.Addr.Compare(b.Addr) })
	return v
}

// controlAddr is the host's control address on path, or "" for none.
func (v hostView) controlAddr(path discovery.Announcement) string {
	if !path.Addr.IsValid() {
		return ""
	}
	return netip.AddrPortFrom(path.Addr.Addr(), v.Beacon.ControlPort).String()
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

// forget drops the host with this ID until it is heard again.
func (f *finder) forget(id string) {
	f.mu.Lock()
	delete(f.hosts, id)
	f.mu.Unlock()
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
// discovery on its IP and port says who it is, so the pin keys its ID and
// the pairing screen shows its fingerprint; the name is the host's, or the
// address.
func identify(ctx context.Context, addr string, port uint16) (session.Target, string) {
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
	b, _, err := discovery.Probe(ctx, netip.AddrPortFrom(ip, port))
	if err != nil {
		return t, addr
	}
	t.HostID, t.Fingerprint = b.HostID, b.CertFingerprint
	t.Addr = netip.AddrPortFrom(ip, b.ControlPort).String()
	return t, b.Name
}
