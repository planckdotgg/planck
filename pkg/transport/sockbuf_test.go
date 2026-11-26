package transport

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
)

// TestMediaSocketBuffers: both ends' media sockets get at least 4 MiB each
// way. macOS refuses SO_RCVBUF = kern.ipc.maxsockbuf (8 MiB by default)
// with ENOBUFS, and a socket left at net.inet.udp.recvspace (768 KiB)
// holds about 380 datagrams, less than one 5K keyframe.
func TestMediaSocketBuffers(t *testing.T) {
	p := newPair(t, pairOpts{})
	for _, s := range []*Session{p.host, p.client} {
		got, err := readSocketBuffers(s.udp)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("host=%v: granted recv %d B (%.2f MiB), send %d B (%.2f MiB); session reports %+v",
			s.isHost, got.Recv, float64(got.Recv)/(1<<20), got.Send, float64(got.Send)/(1<<20), s.SocketBuffers())
		if runtime.GOOS == "linux" {
			// net.core.rmem_max and wmem_max cap these, often at 208 KiB.
			continue
		}
		if got.Recv < 4<<20 || got.Send < 4<<20 {
			t.Fatalf("host=%v: socket buffers recv %d, send %d; want at least %d each", s.isHost, got.Recv, got.Send, 4<<20)
		}
		if s.SocketBuffers() != got {
			t.Fatalf("host=%v: session reports %+v, socket has %+v", s.isHost, s.SocketBuffers(), got)
		}
	}
}

// TestSocketBufferStepDown: a kernel that refuses the larger sizes, as XNU
// builds that hold back mbuf headroom refuse 8 MiB with ENOBUFS, gets the
// largest size it accepts instead of none.
func TestSocketBufferStepDown(t *testing.T) {
	var asked []int
	limit := 6 << 20
	err := stepDown(func(n int) error {
		asked = append(asked, n)
		if n > limit {
			return syscall.ENOBUFS
		}
		return nil
	})
	if err != nil || asked[len(asked)-1] != limit {
		t.Fatalf("asked %v, err %v; want to stop at %d", asked, err, limit)
	}
	limit = 0
	asked = nil
	if err := stepDown(func(n int) error { asked = append(asked, n); return syscall.ENOBUFS }); !errors.Is(err, syscall.ENOBUFS) || len(asked) != len(socketBufferSteps) {
		t.Fatalf("every size refused: asked %v, err %v", asked, err)
	}
}
