package transport

import (
	"runtime"
	"sync/atomic"
)

// shardCount holds the pre-FEC shard totals as a pair that a reader sees
// from the same frame: read apart, a lossless stream could show more
// shards expected than received. One goroutine adds (the receive loop's
// FEC decoder); seq is odd while it does.
type shardCount struct {
	seq, expected, received atomic.Uint64
}

func (c *shardCount) add(expected, received uint64) {
	c.seq.Add(1)
	c.expected.Add(expected)
	c.received.Add(received)
	c.seq.Add(1)
}

func (c *shardCount) load() (expected, received uint64) {
	for {
		seq := c.seq.Load()
		if seq&1 == 0 {
			expected, received = c.expected.Load(), c.received.Load()
			if c.seq.Load() == seq {
				return expected, received
			}
		}
		runtime.Gosched()
	}
}
