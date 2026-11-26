package transport

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/fec"
)

// TestLosslessShardTotalsMatch: Stats read while frames finalize sees the
// expected and received shard totals of the same frame, so a lossless
// stream never shows pre-FEC loss.
func TestLosslessShardTotalsMatch(t *testing.T) {
	s := &Session{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 300000 {
			s.onFrameFinal(fec.FrameStats{SentShards: 100, RecvShards: 100, Complete: true})
		}
	}()
	defer func() { <-done }()
	for {
		snap := s.Stats()
		if snap.ShardsExpected != snap.ShardsReceived {
			t.Fatalf("lossless stream: expected %d shards, received %d; want equal", snap.ShardsExpected, snap.ShardsReceived)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}
