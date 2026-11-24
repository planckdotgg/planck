package transport

import (
	"errors"
	"net"
	"testing"
	"time"
)

// TestMediaReader reads datagrams with the platform's media reader on a
// goroutine of its own, as recvLoop does: the sender's address comes back,
// reading allocates nothing, and closing the socket ends a blocked read.
func TestMediaReader(t *testing.T) {
	rx, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := net.DialUDP("udp", nil, rx.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	const n = 200
	type result struct {
		allocs  float64
		badFrom int
		closed  error
		waited  time.Duration
	}
	ready, sent, done := make(chan struct{}), make(chan struct{}), make(chan result)
	go func() {
		read := newMediaReader(rx)
		buf := make([]byte, 2048)
		want := tx.LocalAddr().(*net.UDPAddr).AddrPort()
		var res result
		close(ready)
		<-sent
		res.allocs = testing.AllocsPerRun(n-1, func() {
			k, from, err := read(buf)
			if err != nil || k != 100 || from.Addr().Unmap() != want.Addr() || from.Port() != want.Port() {
				res.badFrom++
			}
		})
		start := time.Now()
		go func() {
			time.Sleep(20 * time.Millisecond)
			rx.Close()
		}()
		for {
			if _, _, err := read(buf); errors.Is(err, net.ErrClosed) {
				res.closed = err
				break
			}
			if time.Since(start) > 2*time.Second {
				break
			}
		}
		res.waited = time.Since(start)
		done <- res
	}()
	<-ready
	msg := make([]byte, 100)
	for range n {
		if _, err := tx.Write(msg); err != nil {
			t.Fatal(err)
		}
	}
	close(sent)
	res := <-done
	if res.badFrom != 0 {
		t.Fatalf("%d reads had the wrong size or sender", res.badFrom)
	}
	if res.allocs != 0 {
		t.Fatalf("%.1f allocations per datagram", res.allocs)
	}
	if res.closed == nil || res.waited > time.Second {
		t.Fatalf("closing the socket ended the read after %v with %v", res.waited, res.closed)
	}
	t.Logf("close ended a blocked read after %v", res.waited.Round(time.Millisecond))
}
