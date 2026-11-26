package session

import (
	"slices"
	"sync"
	"time"
)

// Latency is a rolling p50/p95/p99 summary in milliseconds.
type Latency struct {
	P50, P95, P99 float64
	Samples       int
}

// window keeps the last n samples for percentiles.
type window struct {
	buf  []float64
	next int
	full bool
	tmp  []float64
}

func newWindow(n int) *window { return &window{buf: make([]float64, n), tmp: make([]float64, 0, n)} }

func (w *window) add(v float64) {
	w.buf[w.next] = v
	w.next++
	if w.next == len(w.buf) {
		w.next, w.full = 0, true
	}
}

func (w *window) reset() { w.next, w.full = 0, false }

func (w *window) len() int {
	if w.full {
		return len(w.buf)
	}
	return w.next
}

func (w *window) last() float64 {
	if w.len() == 0 {
		return 0
	}
	i := w.next - 1
	if i < 0 {
		i = len(w.buf) - 1
	}
	return w.buf[i]
}

// ordered appends the samples to dst, oldest first.
func (w *window) ordered(dst []float64) []float64 {
	if w.full {
		dst = append(dst, w.buf[w.next:]...)
	}
	return append(dst, w.buf[:w.next]...)
}

func (w *window) summary() Latency {
	n := w.len()
	if n == 0 {
		return Latency{}
	}
	w.tmp = append(w.tmp[:0], w.buf[:n]...)
	slices.Sort(w.tmp)
	at := func(p float64) float64 { return w.tmp[min(n-1, int(p*float64(n)))] }
	return Latency{P50: at(0.50), P95: at(0.95), P99: at(0.99), Samples: n}
}

// rate counts events and bytes over a sliding one-second span.
type rate struct {
	mu     sync.Mutex
	stamps []int64
	bytes  []int
}

func (r *rate) add(now int64, n int) {
	r.mu.Lock()
	r.stamps = append(r.stamps, now)
	r.bytes = append(r.bytes, n)
	r.trim(now)
	r.mu.Unlock()
}

func (r *rate) trim(now int64) {
	cut := 0
	for cut < len(r.stamps) && now-r.stamps[cut] > int64(time.Second) {
		cut++
	}
	if cut > 0 {
		r.stamps = append(r.stamps[:0], r.stamps[cut:]...)
		r.bytes = append(r.bytes[:0], r.bytes[cut:]...)
	}
}

// per returns events per second and kilobits per second.
func (r *rate) per(now int64) (float64, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trim(now)
	if len(r.stamps) == 0 {
		return 0, 0
	}
	span := float64(time.Second)
	if first := now - r.stamps[0]; len(r.stamps) > 1 && first < int64(time.Second) && first > 0 {
		span = float64(first)
	}
	total := 0
	for _, b := range r.bytes {
		total += b
	}
	secs := span / float64(time.Second)
	return float64(len(r.stamps)) / secs, float64(total) * 8 / 1000 / secs
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }
