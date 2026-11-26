package transport

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func trainPair(t *testing.T) (rx, tx *net.UDPConn, peer netip.AddrPort) {
	t.Helper()
	var err error
	rx, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	_ = rx.SetReadBuffer(4 << 20)
	tx, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rx.Close(); tx.Close() })
	return rx, tx, rx.LocalAddr().(*net.UDPAddr).AddrPort()
}

// readAll returns the datagrams waiting on rx, in arrival order.
func readAll(t *testing.T, rx *net.UDPConn) [][]byte {
	t.Helper()
	var got [][]byte
	buf := make([]byte, 70_000)
	for {
		_ = rx.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := rx.ReadFromUDPAddrPort(buf)
		if err != nil {
			return got
		}
		got = append(got, append([]byte(nil), buf[:n]...))
	}
}

func datagram(i, n int) []byte {
	b := make([]byte, n)
	for j := range b {
		b[j] = byte(i)
	}
	return b
}

func checkDatagrams(t *testing.T, got [][]byte, sizes []int) {
	t.Helper()
	if len(got) != len(sizes) {
		t.Fatalf("received %d datagrams, want %d", len(got), len(sizes))
	}
	for i, g := range got {
		if len(g) != sizes[i] || g[0] != byte(i) || g[len(g)-1] != byte(i) {
			t.Fatalf("datagram %d: %d bytes starting %d, want %d bytes of %d", i, len(g), g[0], sizes[i], i)
		}
	}
}

// countCalls wraps w's offload send.
func countCalls(w *trainWriter, fail bool) *int {
	calls := 0
	send := w.sendMsg
	w.sendMsg = func(b, oob []byte, addr netip.AddrPort) (int, int, error) {
		calls++
		if fail {
			return 0, 0, errors.New("offload refused")
		}
		return send(b, oob, addr)
	}
	return &calls
}

func TestTrainWriterSegments(t *testing.T) {
	rx, tx, peer := trainPair(t)
	w := newTrainWriter(tx)
	if !w.gso {
		t.Skip("no UDP send offload on this OS")
	}
	calls := countCalls(&w, false)
	var sizes []int
	sent := 0
	// 50 full datagrams overflow one 63 KiB train; a short one ends the
	// second train, and the one after it starts a third.
	for i := 0; i < 52; i++ {
		size := 1452
		if i == 50 {
			size = 700
		}
		sizes = append(sizes, size)
		n, err := w.add(datagram(i, size), peer)
		if err != nil {
			t.Fatal(err)
		}
		sent += n
	}
	n, err := w.flush(peer)
	if err != nil {
		t.Fatal(err)
	}
	sent += n
	if sent != 52 {
		t.Fatalf("add and flush reported %d datagrams, want 52", sent)
	}
	// The last train holds one datagram, which goes out without offload.
	if *calls != 2 {
		t.Fatalf("%d offload calls, want 2", *calls)
	}
	checkDatagrams(t, readAll(t, rx), sizes)
}

func TestTrainWriterFallbackLatches(t *testing.T) {
	rx, tx, peer := trainPair(t)
	w := newTrainWriter(tx)
	if !w.gso {
		t.Skip("no UDP send offload on this OS")
	}
	calls := countCalls(&w, true)
	var sizes []int
	for train := 0; train < 2; train++ {
		for i := 0; i < 8; i++ {
			sizes = append(sizes, 1200)
			if _, err := w.add(datagram(len(sizes)-1, 1200), peer); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := w.flush(peer); err != nil {
			t.Fatal(err)
		} else if train == 0 && n != 8 {
			t.Fatalf("fallback sent %d of 8", n)
		}
	}
	if *calls != 1 || w.gso {
		t.Fatalf("%d offload calls, gso %v: want one failed call, then offload off", *calls, w.gso)
	}
	checkDatagrams(t, readAll(t, rx), sizes)
}

// A train whose per-datagram resend fails too is not offload's fault, so
// offload stays on.
func TestTrainWriterSendErrorKeepsOffload(t *testing.T) {
	_, tx, _ := trainPair(t)
	w := newTrainWriter(tx)
	if !w.gso {
		t.Skip("no UDP send offload on this OS")
	}
	countCalls(&w, true)
	bad := netip.MustParseAddrPort("[::1]:9") // IPv6 on an IPv4 socket
	for i := 0; i < 4; i++ {
		if _, err := w.add(datagram(i, 1000), bad); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.flush(bad); err == nil {
		t.Fatal("flush to an unreachable peer succeeded")
	}
	if !w.gso {
		t.Fatal("a send error turned offload off")
	}
}

func TestTrainWriterNoAllocs(t *testing.T) {
	rx, tx, peer := trainPair(t)
	w := newTrainWriter(tx)
	pkt := datagram(1, 1452)
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := rx.ReadFromUDPAddrPort(buf); err != nil {
				return
			}
		}
	}()
	allocs := testing.AllocsPerRun(200, func() {
		for i := 0; i < 16; i++ {
			_, _ = w.add(pkt, peer)
		}
		_, _ = w.flush(peer)
	})
	if allocs != 0 {
		t.Fatalf("%.1f allocations per 16-datagram train", allocs)
	}
}
