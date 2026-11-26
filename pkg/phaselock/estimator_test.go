package phaselock

import (
	"math/rand/v2"
	"testing"
	"time"
)

const p120 = int64(time.Second / 120)

func TestWrap(t *testing.T) {
	for _, c := range []struct{ d, want int64 }{
		{0, 0},
		{1, 1},
		{p120/2 - 1, p120/2 - 1},
		{p120 / 2, p120/2 - p120},
		{-p120 / 2, -p120 / 2},
		{-p120/2 - 1, -p120/2 - 1 + p120},
		{p120 + 5, 5},
		{-3*p120 - 5, -5},
	} {
		if got := Wrap(c.d, p120); got != c.want {
			t.Errorf("Wrap(%d) = %d, want %d", c.d, got, c.want)
		}
	}
}

func TestGridNext(t *testing.T) {
	g := Grid{Period: 100, Anchor: 1000}
	for _, c := range []struct{ t, want int64 }{
		{1000, 1100}, {1001, 1100}, {1099, 1100}, {950, 1000}, {900, 1000}, {899, 900}, {-5, 0},
	} {
		if got := g.Next(c.t); got != c.want {
			t.Errorf("Next(%d) = %d, want %d", c.t, got, c.want)
		}
	}
}

// feed gives e pictures ready slack before a refresh, give or take
// jitter, drawn in render, shown extra refreshes after direct scan-out.
func feed(e *Estimator, g Grid, n int, slack, jitter, render int64, extra int, rng *rand.Rand) {
	for i := range n {
		v := g.Anchor + int64(i+10)*g.Period
		s := slack
		if jitter > 0 {
			s += rng.Int64N(2*jitter) - jitter
		}
		ready := v - s
		done := ready + render
		e.Observe(Sample{Frame: uint32(i), Ready: ready, GPUDone: done, Glass: g.Next(done) + int64(extra)*g.Period}, g)
	}
}

func TestEstimatorSlackAndError(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var e Estimator
	feed(&e, g, 64, 5*int64(time.Millisecond), 0, 300*int64(time.Microsecond), 0, nil)
	r := e.Report()
	if !r.Valid || r.Mode != ModeDirect {
		t.Fatalf("report %+v: want valid, direct", r)
	}
	if r.SlackNs != 5*int64(time.Millisecond) {
		t.Fatalf("slack %d, want 5 ms", r.SlackNs)
	}
	// Render 0.3 ms plus the starting 0.5 ms margin is under the floor.
	if r.TargetNs != minTarget || r.ErrorNs != 5*int64(time.Millisecond)-minTarget {
		t.Fatalf("target %d error %d, want %d and %d", r.TargetNs, r.ErrorNs, minTarget, 5*int64(time.Millisecond)-minTarget)
	}
	if r.Locked {
		t.Fatal("locked 4 ms off")
	}
	if r.Oldest != 32 || r.Newest != 63 {
		t.Fatalf("frames %d..%d, want 32..63", r.Oldest, r.Newest)
	}
}

// Pictures ready right at the refresh have slack just over 0 or just
// under a period: the median must stay at the refresh, not halfway.
func TestEstimatorSlackAcrossTheRefresh(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var e Estimator
	feed(&e, g, 64, 100*int64(time.Microsecond), 400*int64(time.Microsecond), 0, 0, rand.New(rand.NewPCG(1, 2)))
	r := e.Report()
	off := Wrap(r.SlackNs-100*int64(time.Microsecond), p120)
	if abs(off) > 300*int64(time.Microsecond) {
		t.Fatalf("slack %.2f ms, want about 0.1 ms", float64(r.SlackNs)/1e6)
	}
	if r.JitterNs > 500*int64(time.Microsecond) {
		t.Fatalf("jitter %.2f ms, want under 0.5 ms", float64(r.JitterNs)/1e6)
	}
	if r.ErrorNs >= 0 {
		t.Fatalf("error %.2f ms: pictures at the refresh are late, want below 0", float64(r.ErrorNs)/1e6)
	}
}

func TestEstimatorJitterRaisesTarget(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var quiet, noisy Estimator
	rng := rand.New(rand.NewPCG(3, 4))
	feed(&quiet, g, 64, 4*int64(time.Millisecond), 50*int64(time.Microsecond), 0, 0, rng)
	feed(&noisy, g, 64, 4*int64(time.Millisecond), 1500*int64(time.Microsecond), 0, 0, rng)
	q, n := quiet.Report(), noisy.Report()
	if n.JitterNs <= q.JitterNs || n.TargetNs <= q.TargetNs {
		t.Fatalf("noisy jitter %d target %d, quiet %d %d: want both higher", n.JitterNs, n.TargetNs, q.JitterNs, q.TargetNs)
	}
	if n.TargetNs > p120/2 {
		t.Fatalf("target %d past half a refresh", n.TargetNs)
	}
}

func TestEstimatorMode(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var e Estimator
	feed(&e, g, 20, 4*int64(time.Millisecond), 0, 0, 1, nil)
	if r := e.Report(); r.Mode != ModeUnknown {
		t.Fatalf("mode %v after 20 pictures, want unknown", r.Mode)
	}
	feed(&e, g, modeWindow, 4*int64(time.Millisecond), 0, 0, 1, nil)
	r := e.Report()
	if r.Mode != ModeComposited || r.CompositedPct != 100 || r.LatePct != 0 {
		t.Fatalf("report %+v: want composited, 100%% a refresh later, none late", r)
	}
	// Half the pictures direct is inside the hysteresis: still composited.
	feed(&e, g, modeWindow/2, 4*int64(time.Millisecond), 0, 0, 0, nil)
	if r := e.Report(); r.Mode != ModeComposited {
		t.Fatalf("mode %v at %.0f%%, want composited kept", r.Mode, r.CompositedPct)
	}
	feed(&e, g, modeWindow, 4*int64(time.Millisecond), 0, 0, 0, nil)
	if r := e.Report(); r.Mode != ModeDirect || r.CompositedPct != 0 {
		t.Fatalf("report %+v: want direct", r)
	}
}

func TestEstimatorNoGPUTimesNoMode(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var e Estimator
	for i := range 200 {
		v := g.Anchor + int64(i+10)*g.Period
		e.Observe(Sample{Frame: uint32(i), Ready: v - int64(time.Millisecond), Glass: v + g.Period}, g)
	}
	if r := e.Report(); !r.Valid || r.Mode != ModeUnknown {
		t.Fatalf("report %+v: want valid with the mode unknown", r)
	}
}

func TestEstimatorLatePicturesGrowMargin(t *testing.T) {
	g := Grid{Period: p120, Anchor: 1e9}
	var e Estimator
	feed(&e, g, modeWindow, 2*int64(time.Millisecond), 0, 500*int64(time.Microsecond), 0, nil)
	before := e.Report().TargetNs
	// Every tenth picture misses its refresh.
	for i := range modeWindow {
		v := g.Anchor + int64(i+500)*g.Period
		ready := v - 2*int64(time.Millisecond)
		done := ready + 500*int64(time.Microsecond)
		glass := g.Next(done)
		if i%10 == 0 {
			glass += g.Period
		}
		e.Observe(Sample{Frame: uint32(i + 500), Ready: ready, GPUDone: done, Glass: glass}, g)
	}
	r := e.Report()
	if r.LatePct < 5 || r.Mode != ModeDirect {
		t.Fatalf("report %+v: want direct with about 10%% late", r)
	}
	if r.TargetNs <= before {
		t.Fatalf("target %d, before %d: late pictures should raise it", r.TargetNs, before)
	}
}

func TestEstimatorDisplayChangeResets(t *testing.T) {
	var e Estimator
	feed(&e, Grid{Period: p120, Anchor: 1e9}, 64, 4*int64(time.Millisecond), 0, 0, 0, nil)
	p60 := int64(time.Second / 60)
	feed(&e, Grid{Period: p60, Anchor: 1e9}, 4, 4*int64(time.Millisecond), 0, 0, 0, nil)
	if r := e.Report(); r.Valid || r.Samples != 4 {
		t.Fatalf("report %+v: want the 120 Hz pictures gone", r)
	}
}
