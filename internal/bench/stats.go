// Package bench collects latency samples and summarizes them.
package bench

import (
	"fmt"
	"math"
	"sort"
)

type Samples struct {
	ms []float64
}

func NewSamples(capacity int) *Samples { return &Samples{ms: make([]float64, 0, capacity)} }

func (s *Samples) Add(ms float64) { s.ms = append(s.ms, ms) }

func (s *Samples) Len() int { return len(s.ms) }

type Summary struct {
	N                        int
	Mean, P50, P95, P99, Max float64
}

// Summarize uses nearest-rank percentiles.
func (s *Samples) Summarize() Summary {
	n := len(s.ms)
	if n == 0 {
		return Summary{}
	}
	sorted := append([]float64(nil), s.ms...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	rank := func(p float64) float64 {
		i := int(math.Ceil(p/100*float64(n))) - 1
		return sorted[max(i, 0)]
	}
	return Summary{N: n, Mean: sum / float64(n), P50: rank(50), P95: rank(95), P99: rank(99), Max: sorted[n-1]}
}

func (s Summary) String() string {
	return fmt.Sprintf("p50=%.3f p95=%.3f p99=%.3f max=%.3f mean=%.3f n=%d", s.P50, s.P95, s.P99, s.Max, s.Mean, s.N)
}
