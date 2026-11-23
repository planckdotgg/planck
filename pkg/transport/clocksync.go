package transport

import (
	"sync/atomic"
	"time"

	"github.com/watzon/planck/pkg/wire"
)

const clockWindow = 16

type clockSample struct {
	rtt    int64
	offset int64
}

// ClockEstimator turns ping/pong exchanges into a round-trip time and the
// offset of the peer's monotonic clock from ours. The offset comes from the
// lowest-RTT sample of the last 16, which has the least queueing asymmetry.
// Add must be called from one goroutine; the readers are safe from any.
type ClockEstimator struct {
	samples [clockWindow]clockSample
	n, head int

	srtt   atomic.Int64
	minRTT atomic.Int64
	offset atomic.Int64
	count  atomic.Uint64
}

// Add folds in a pong received at local time t3.
func (c *ClockEstimator) Add(p wire.Pong, t3 int64) {
	rtt := (t3 - p.T0) - (p.T2 - p.T1)
	if rtt < 0 || p.T2 < p.T1 {
		return
	}
	offset := ((p.T1 - p.T0) + (p.T2 - t3)) / 2
	c.samples[c.head] = clockSample{rtt: rtt, offset: offset}
	c.head = (c.head + 1) % clockWindow
	c.n = min(c.n+1, clockWindow)
	best := c.samples[0]
	for _, s := range c.samples[1:c.n] {
		if s.rtt < best.rtt {
			best = s
		}
	}
	c.minRTT.Store(best.rtt)
	c.offset.Store(best.offset)
	if c.count.Add(1) == 1 {
		c.srtt.Store(rtt)
	} else {
		old := c.srtt.Load()
		c.srtt.Store(old + (rtt-old)/8)
	}
}

// RTT is the smoothed round-trip time, or 0 before the first pong.
func (c *ClockEstimator) RTT() time.Duration { return time.Duration(c.srtt.Load()) }

// MinRTT is the lowest RTT in the recent window.
func (c *ClockEstimator) MinRTT() time.Duration { return time.Duration(c.minRTT.Load()) }

// Offset is the peer's clock minus ours, in nanoseconds.
func (c *ClockEstimator) Offset() int64 { return c.offset.Load() }

// ToLocal maps a peer monotonic timestamp to our clock.
func (c *ClockEstimator) ToLocal(peerTS int64) int64 { return peerTS - c.offset.Load() }

// Samples is the number of pongs folded in so far.
func (c *ClockEstimator) Samples() uint64 { return c.count.Load() }
