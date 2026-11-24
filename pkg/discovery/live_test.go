package discovery

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestServeFuncAnnounceFuncLiveBeacon: a probe and the next beacon round
// carry a name changed after both started.
func TestServeFuncAnnounceFuncLiveBeacon(t *testing.T) {
	var cur atomic.Pointer[Beacon]
	b := testBeacon
	cur.Store(&b)
	get := func() Beacon { return *cur.Load() }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := listenLoopback(t)
	go ServeFunc(ctx, srv, get)
	addr := srv.LocalAddr().(*net.UDPAddr).AddrPort()

	listener := listenLoopback(t)
	go AnnounceFunc(ctx, listenLoopback(t), get, []netip.AddrPort{listener.LocalAddr().(*net.UDPAddr).AddrPort()}, 20*time.Millisecond)

	renamed := testBeacon
	renamed.Name = "renamed"
	cur.Store(&renamed)

	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	got, _, err := Probe(pctx, addr)
	if err != nil || got.Name != "renamed" {
		t.Fatalf("probe: %+v, %v", got, err)
	}
	lctx, lcancel := context.WithTimeout(ctx, 2*time.Second)
	defer lcancel()
	var heard atomic.Bool
	Listen(lctx, listener, func(a Announcement) {
		if a.Beacon.Name == "renamed" && heard.CompareAndSwap(false, true) {
			lcancel()
		}
	})
	if !heard.Load() {
		t.Fatal("no beacon with the new name")
	}
}

// TestProbeAll hears every host that answers, and nothing from a dead
// address.
func TestProbeAll(t *testing.T) {
	a := startHost(t, testBeacon)
	other := testBeacon
	other.HostID, other.Name = "host-2", "studio"
	b := startHost(t, other)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var mu sync.Mutex
	seen := map[string]netip.AddrPort{}
	err := ProbeAll(ctx, []netip.AddrPort{a, b, deadUDPAddr(t)}, func(an Announcement) {
		mu.Lock()
		seen[an.Beacon.HostID] = an.Addr
		mu.Unlock()
		if an.Kind != PathLANDirect || an.RTT <= 0 {
			t.Errorf("announcement %+v", an)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen["host-1"] != a || seen["host-2"] != b {
		t.Fatalf("heard %v, want host-1 at %v and host-2 at %v", seen, a, b)
	}
}
