package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func probeLocal(t *testing.T, port int) (Beacon, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, _, err := Probe(ctx, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)))
	return b, err
}

// foreignProbe probes as another machine does, its nonces not this
// process's own, resending every probeRetry until a reply or 2 s.
func foreignProbe(t *testing.T, port int) (Beacon, error) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	pkt, err := encode(message{Type: msgProbe, Nonce: 7})
	if err != nil {
		t.Fatal(err)
	}
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	buf := make([]byte, maxMessageLen+1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.WriteTo(pkt, dst)
		conn.SetReadDeadline(time.Now().Add(probeRetry))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		if m, err := decode(buf[:n]); err == nil && m.Type == msgReply && m.Nonce == 7 {
			return *m.Beacon, nil
		}
	}
	return Beacon{}, context.DeadlineExceeded
}

func serve(p *Port, name string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = p.Serve(ctx, func() Beacon { return Beacon{HostID: name, Name: name} })
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestOpenPortSharesInProcess: a browser and a host of one process share
// the port's socket; the host answers probes and the browser hears
// beacons sent to it.
func TestOpenPortSharesInProcess(t *testing.T) {
	addr := fmt.Sprintf(":%d", freePort(t))
	browser, err := OpenPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	host, err := OpenPort(addr)
	if err != nil {
		t.Fatalf("second open in one process: %v", err)
	}
	defer host.Close()
	if host != browser {
		t.Fatal("OpenPort bound a second socket for one address")
	}
	heard := make(chan Announcement, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go browser.Listen(ctx, func(a Announcement) { heard <- a })
	stop := serve(host, "here")
	defer stop()

	port := browser.LocalAddr().(*net.UDPAddr).Port
	if b, err := probeLocal(t, port); err != nil || b.Name != "here" {
		t.Fatalf("probe: %+v, %v", b, err)
	}
	sender, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	go Announce(ctx, sender, Beacon{HostID: "other", Name: "other"},
		[]netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))}, 50*time.Millisecond)
	select {
	case a := <-heard:
		if a.Beacon.Name != "other" || a.Kind != PathLANDirect {
			t.Fatalf("heard %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the browser heard no beacon")
	}
}

// TestOpenPortStartStopCycles: hosting starts and stops again and again
// while the browser keeps the port, and the port is free once both let
// go.
func TestOpenPortStartStopCycles(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf(":%d", port)
	browser, err := OpenPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		host, err := OpenPort(addr)
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		stop := serve(host, fmt.Sprint("host-", i))
		if b, err := probeLocal(t, port); err != nil || b.Name != fmt.Sprint("host-", i) {
			t.Fatalf("round %d: probe: %+v, %v", i, b, err)
		}
		stop()
		if err := host.Close(); err != nil {
			t.Fatalf("round %d: close: %v", i, err)
		}
	}
	if err := browser.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatalf("port still held after the last Close: %v", err)
	}
	c.Close()
	again, err := OpenPort(addr)
	if err != nil {
		t.Fatalf("open after the last Close: %v", err)
	}
	again.Close()
}

// TestPortYieldsUnicastToAnotherProcess: a browser that bound the port
// first, as a client app started before a separate host, would take every
// unicast probe; hearing one with no host of its own, it binds anew and
// the other process's host answers. Both still hear broadcasts.
func TestPortYieldsUnicastToAnotherProcess(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf(":%d", port)
	// Two sockets apart from the registry stand for two processes.
	browser, err := newPort(addr, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	host, err := newPort(addr, addr)
	if err != nil {
		t.Fatalf("a second process could not bind the port: %v", err)
	}
	defer host.Close()
	stop := serve(host, "other-process")
	defer stop()
	heard := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go browser.Listen(ctx, func(a Announcement) { heard <- a.Beacon.Name })

	if b, err := foreignProbe(t, port); err != nil || b.Name != "other-process" {
		t.Fatalf("probe the other process's host: %+v, %v", b, err)
	}
	browser.mu.Lock()
	yielded := !browser.lastYield.IsZero()
	browser.mu.Unlock()
	if !yielded {
		t.Fatal("the host answered without the browser yielding: the test no longer covers the oldest-socket case")
	}

	dst := BroadcastAddrs(uint16(port))
	if len(dst) == 0 {
		t.Skip("no broadcast interface to check the browser still hears beacons")
	}
	go AnnounceFunc(ctx, host, func() Beacon { return Beacon{HostID: "b", Name: "broadcast"} }, dst, 50*time.Millisecond)
	select {
	case n := <-heard:
		if n != "broadcast" {
			t.Fatalf("heard %q", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the browser heard no broadcast beacon after yielding")
	}
}

// TestPortKeepsOwnProbes: a browser hearing its own process's probe, as
// its LAN broadcasts loop back, keeps its socket.
func TestPortKeepsOwnProbes(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf(":%d", port)
	browser, err := OpenPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = ProbeAll(ctx, []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))}, func(Announcement) {})
	browser.mu.Lock()
	defer browser.mu.Unlock()
	if !browser.lastYield.IsZero() {
		t.Fatal("the browser yielded to its own probe")
	}
	if n := countOwnProbes(); n != 0 {
		t.Fatalf("%d probes still recorded after ProbeAll returned", n)
	}
}

func countOwnProbes() int {
	n := 0
	ownProbes.Range(func(any, any) bool { n++; return true })
	return n
}
