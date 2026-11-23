// Package fec protects video slices with Reed-Solomon parity sized from the
// receiver's measured loss, and rebuilds them on the receiving side.
package fec

import (
	"math"
	"sync/atomic"

	"github.com/watzon/planck/pkg/wire"
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
	// while loss is falling. Rising loss is tracked four times faster.
	Window float64

	loss atomic.Uint64
}

// NewPolicy returns a Policy with Target 1e-3, no loss floor, parity capped
// at 100% of data, and a 2000-shard averaging window.
func NewPolicy() *Policy {
	return &Policy{Target: 1e-3, MaxRatio: 1, Window: 2000}
}

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
