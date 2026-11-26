package transport

import (
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

func TestLANDatagramNegotiation(t *testing.T) {
	lanShard := wire.MaxLANDatagram - AEADOverhead - wire.VideoHeaderLen
	safeShard := wire.MaxDatagram - AEADOverhead - wire.VideoHeaderLen
	cases := []struct {
		name     string
		lan      bool
		explicit int
		want     int
	}{
		{"client on a LAN path", true, 0, lanShard},
		{"client path not LAN", false, 0, safeShard},
		{"host sets MaxDatagram", true, wire.MaxDatagram, safeShard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := make(chan []byte, 1)
			p := newPair(t, pairOpts{
				server: func(c *ServerConfig) { c.MaxDatagram = tc.explicit },
				client: func(c *ClientConfig) {
					c.LANPath = tc.lan
					c.Session.Slices = 1
					c.Handlers.VideoFrame = func(_ wire.VideoInfo, f []byte) {
						select {
						case got <- append([]byte(nil), f...):
						default:
						}
					}
				},
			})
			if s := p.client.ShardSize(); s != tc.want {
				t.Fatalf("client shard size %d, want %d", s, tc.want)
			}
			if s := p.host.ShardSize(); s != tc.want {
				t.Fatalf("host shard size %d, want %d", s, tc.want)
			}
			frame := make([]byte, 100_000)
			fillFrame(frame, 1)
			if err := p.host.SendVideoFrame(wire.VideoInfo{FrameID: 1, Keyframe: true}, frame); err != nil {
				t.Fatal(err)
			}
			select {
			case f := <-got:
				if !frameOK(f, 1) || len(f) != len(frame) {
					t.Fatal("frame corrupted")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("frame never arrived")
			}
		})
	}
}

// TestRedundantInput sends input while the client has seen loss: each
// event goes out twice and the host hands each Seq on once, in order.
func TestRedundantInput(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		var mu sync.Mutex
		var seqs []uint32
		p := newPair(t, pairOpts{server: func(c *ServerConfig) {
			c.Handlers.Input = func(_ *Session, in wire.Input) {
				mu.Lock()
				seqs = append(seqs, in.Seq)
				mu.Unlock()
			}
		}})
		if lossy {
			p.client.lossAt.Store(Mono())
		}
		const n = 50
		for i := uint32(1); i <= n; i++ {
			if err := p.client.SendInput(wire.Input{Type: wire.InputMouseRel, Seq: i, Timestamp: Mono(), X: 1}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			mu.Lock()
			got := len(seqs)
			mu.Unlock()
			if got >= n || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		got := append([]uint32(nil), seqs...)
		mu.Unlock()
		if len(got) != n {
			t.Fatalf("lossy=%v: host handled %d events, want %d", lossy, len(got), n)
		}
		for i, s := range got {
			if s != uint32(i+1) {
				t.Fatalf("lossy=%v: event %d has Seq %d", lossy, i, s)
			}
		}
		cs, hs := p.client.Stats(), p.host.Stats()
		wantResent := uint64(0)
		if lossy {
			wantResent = n - 1
		}
		if cs.InputResent != wantResent || hs.InputDuplicates != wantResent {
			t.Fatalf("lossy=%v: client resent %d, host dropped %d duplicates, want %d", lossy, cs.InputResent, hs.InputDuplicates, wantResent)
		}
	}
}

func TestSeqWindow(t *testing.T) {
	var w seqWindow
	steps := []struct {
		seq  uint32
		seen bool
	}{
		{5, false}, {5, true}, {4, false}, {4, true}, {6, false}, {10, false}, {8, false}, {8, true}, {6, true},
		{0, false}, {0, false},
		{10 + seqWindowLen, false}, {10, true}, {11, false}, {11, true}, {11 + seqWindowLen, false}, {11, true},
		{^uint32(0), true}, {7000, false}, {6999, false}, {6999, true},
	}
	for i, s := range steps {
		if got := w.seen(s.seq); got != s.seen {
			t.Fatalf("step %d: seen(%d) = %v, want %v", i, s.seq, got, s.seen)
		}
	}
}
