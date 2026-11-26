package session

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/transport"
)

// TestShardLossPerReport: the loss a client reports is that of the shards
// since its last report, so a burst early in a session is gone from the
// next report; any loss reads at least 0.1%, and a new session after a
// route switch starts over.
func TestShardLossPerReport(t *testing.T) {
	var l shardLoss
	for _, tc := range []struct {
		expected, received uint64
		want               uint16
	}{
		{10_000, 9_970, 3},
		{20_000, 19_970, 0},
		{30_000, 29_969, 1},
		{2_000, 1_990, 5},
	} {
		if got := l.permille(transport.SessionStats{ShardsExpected: tc.expected, ShardsReceived: tc.received}); got != tc.want {
			t.Errorf("at %d of %d shards: %d per mille, want %d", tc.received, tc.expected, got, tc.want)
		}
	}
}
