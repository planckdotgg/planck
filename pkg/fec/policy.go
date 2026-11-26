// Package fec protects video slices with Reed-Solomon parity sized from the
// receiver's measured loss, and rebuilds them on the receiving side.
package fec

import (
	"math"
	"sync/atomic"

	"github.com/planckdotgg/planck/pkg/wire"
)

// ParityPolicy chooses the parity shard count for a block of data shards.
type ParityPolicy interface {
	Parity(data int) int
}

// Fixed is a parity percentage, rounded up so any non-zero percentage yields
// at least one parity shard.
type Fixed int

func (f Fixed) Parity(data int) int {
	if f <= 0 {
		return 0
	}
	return min((data*int(f)+99)/100, wire.MaxShards-data)
}

// Policy sizes parity from a smoothed loss estimate: the smallest k such that,
// with independent loss at that rate, a block of data+k shards loses more
// than k with probability below Target. No loss gives no parity; about 1%
// loss gives 10 to 20%; 5% gives about 40% on small blocks.
//
// Observe and SetLoss may run concurrently with Parity.
type Policy struct {
	// Target is the acceptable probability that a block is unrecoverable.
	Target float64
	// MinLoss is a floor on the loss estimate, to keep some parity on a
	// clean path.
	MinLoss float64
	// MaxRatio caps parity as a fraction of data shards.
	MaxRatio float64
	// Window is roughly how many shards the loss estimate averages over
	// while loss is falling. Rising loss is tracked four times faster, and
	// the first loss on a clean path (estimate under cleanLoss) sets the
	// estimate to its report at once, so the next frames carry parity
	// before a second loss.
	Window float64

	loss atomic.Uint64
}

// NewPolicy returns a Policy with Target 1e-3, no loss floor, parity capped
// at 100% of data, and a 2000-shard averaging window.
func NewPolicy() *Policy {
	return &Policy{Target: 1e-3, MaxRatio: 1, Window: 2000}
}

// cleanLoss is the estimate below which a path counts as clean.
const cleanLoss = 0.001

func (p *Policy) SetLoss(l float64) { p.loss.Store(math.Float64bits(l)) }
func (p *Policy) Loss() float64     { return math.Float64frombits(p.loss.Load()) }

// Observe folds a receiver report of recv out of sent shards into the loss
// estimate.
func (p *Policy) Observe(recv, sent int) {
	if sent <= 0 {
		return
	}
	sample := 1 - float64(min(recv, sent))/float64(sent)
	window := p.Window
	if window <= 0 {
		window = 2000
	}
	for {
		old := p.loss.Load()
		cur := math.Float64frombits(old)
		w := float64(sent) / window
		if sample > cur {
			w *= 4
		}
		next := cur + min(w, 1)*(sample-cur)
		if cur < cleanLoss && sample > next {
			next = sample
		}
		if p.loss.CompareAndSwap(old, math.Float64bits(next)) {
			return
		}
	}
}

func (p *Policy) Parity(data int) int {
	limit := wire.MaxShards - data
	if p.MaxRatio > 0 {
		limit = min(limit, int(math.Ceil(float64(data)*p.MaxRatio)))
	}
	loss := max(p.Loss(), p.MinLoss)
	if loss <= 0 || limit <= 0 {
		return 0
	}
	if loss >= 1 {
		return limit
	}
	for k := 0; k < limit; k++ {
		if binomialTailAbove(data+k, k, loss) < p.Target {
			return k
		}
	}
	return limit
}

// BurstFloor is the parity a block carries whatever the loss estimate, for
// the bursts a clean path still has: a NIC, switch or socket queue that
// overflows drops consecutive datagrams, which an estimate of independent
// loss does not foresee. A lost keyframe costs a round trip and the largest
// frame again; a block of a multi-block slice, interleaved on the wire,
// takes a third of a six-datagram burst. Small P frames stay bare: one lost
// costs a reference invalidation, not a keyframe.
func BurstFloor(data, blocks int, keyframe bool) int {
	switch {
	case keyframe && data >= 16:
		return max(4, ceilPercent(data, 5))
	case keyframe:
		return 1
	case blocks > 1:
		return max(3, ceilPercent(data, 3))
	case data >= 64:
		return 2
	}
	return 0
}

func ceilPercent(n, pct int) int { return (n*pct + 99) / 100 }

// binomialTailAbove returns P(X > k) for X ~ Binomial(n, p).
func binomialTailAbove(n, k int, p float64) float64 {
	pmf := math.Pow(1-p, float64(n))
	cdf := pmf
	for i := 1; i <= k; i++ {
		pmf *= float64(n-i+1) / float64(i) * p / (1 - p)
		cdf += pmf
	}
	return max(0, 1-cdf)
}
