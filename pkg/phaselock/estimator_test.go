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

// feedFree gives e n pictures that reach the screen off any refresh grid,
// toGlass after the GPU finished them, give or take spread.
func feedFree(e *Estimator, n int, toGlass, spread int64, rng *rand.Rand) {
	for i := range n {
		ready := 1e9 + int64(i)*10_000_000
		done := ready + int64(time.Millisecond)
		d := toGlass
		if spread > 0 {
			d += rng.Int64N(spread)
		}
		e.Observe(Sample{Frame: uint32(i), Ready: ready, GPUDone: done, Glass: done + d}, Grid{})
	}
}

// Where pictures reach the screen as soon as they are drawn, the mode
// comes from the spread of their time to it, and a burst of late ones
// does not flip it.
func TestEstimatorModeWithoutGrid(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	var e Estimator
	e.SetNominalPeriod(p120)
	feedFree(&e, modeWindow, 3*int64(time.Millisecond), 500*int64(time.Microsecond), rng)
	r := e.Report()
	if r.Mode != ModeDirect || r.Valid || r.PeriodNs != p120 {
		t.Fatalf("report %+v: want direct at the stated refresh, no phase", r)
	}
	// One report's worth of a compositor: the mode holds.
	feedFree(&e, modeWindow, 3*int64(time.Millisecond), p120, rng)
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v after one composited report, want direct kept", r.Mode)
	}
	for range spreadReports {
		feedFree(&e, 30, 3*int64(time.Millisecond), p120, rng)
		r = e.Report()
	}
	if r.Mode != ModeComposited || r.CompositedPct < 20 {
		t.Fatalf("report %+v: want composited", r)
	}
	for range spreadReports + 1 {
		feedFree(&e, modeWindow, 3*int64(time.Millisecond), 500*int64(time.Microsecond), rng)
		r = e.Report()
	}
	if r.Mode != ModeDirect {
		t.Fatalf("mode %v, want direct again", r.Mode)
	}
}

func TestEstimatorModeWithoutGridNeedsAPeriod(t *testing.T) {
	var e Estimator
	feedFree(&e, modeWindow, 3*int64(time.Millisecond), p120, rand.New(rand.NewPCG(7, 8)))
	if r := e.Report(); r.Mode != ModeUnknown {
		t.Fatalf("mode %v with no refresh period known, want unknown", r.Mode)
	}
}

// TestEstimatorResetStartsANewPath: pictures of the present path before
// a Reset do not hold the mode of the one after it. Measured going full
// screen on an M5 Pro at 5120x1440, without Reset the composited window's
// pictures held the verdict for two seconds, and a burst of composited
// pictures after the switch kept it there.
func TestEstimatorResetStartsANewPath(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	ms := int64(time.Millisecond)
	var e Estimator
	e.SetNominalPeriod(p120)
	feedFree(&e, modeWindow, 6*ms, 2*p120, rng)
	if r := e.Report(); r.Mode != ModeComposited {
		t.Fatalf("window: mode %v, want composited", r.Mode)
	}
	e.Reset()
	if r := e.Report(); r.Mode != ModeUnknown || r.Valid {
		t.Fatalf("after reset: report %+v, want unknown", r)
	}
	feedFree(&e, minModeSet, 2*ms, ms, rng)
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("full screen: mode %v at %.0f%% late, want direct from the first report", r.Mode, r.CompositedPct)
	}
	e.Reset()
	feedFree(&e, minModeSet, 6*ms, 2*p120, rng)
	if r := e.Report(); r.Mode != ModeComposited {
		t.Fatalf("back in a window: mode %v, want composited", r.Mode)
	}

	g := Grid{Period: p120, Anchor: 1e9}
	var gr Estimator
	feed(&gr, g, modeWindow, 4*ms, 0, 0, 1, nil)
	if r := gr.Report(); r.Mode != ModeComposited {
		t.Fatalf("gridded window: mode %v", r.Mode)
	}
	gr.Reset()
	feed(&gr, g, minModeSet, 4*ms, 0, 0, 0, nil)
	if r := gr.Report(); r.Mode != ModeDirect {
		t.Fatalf("gridded full screen: mode %v at %.0f%%, want direct", r.Mode, r.CompositedPct)
	}
}

// holdDisplay stands for a 120 Hz display showing a 60 fps stream direct:
// a picture reaches the screen 1.8 ms after its GPU end, except in a queue,
// where the display keeps each picture up two refreshes or more and the
// next waits for the refresh that lets it go. A queue starts with a picture
// a hiccup shows 10 ms late.
type holdDisplay struct {
	at, glass int64
	queued    bool
	rng       *rand.Rand
}

func (d *holdDisplay) next(queue bool) Sample {
	ms := int64(time.Millisecond)
	d.at += 2*p120 + d.rng.Int64N(600_000) - 300_000
	done := d.at + 4*ms
	glass := done + 1800*int64(time.Microsecond) + d.rng.Int64N(600_000)
	switch {
	case queue && !d.queued:
		glass = done + 10*ms + d.rng.Int64N(ms)
	case queue:
		k := int64(2)
		for d.glass+k*p120 < done+1800*int64(time.Microsecond) {
			k++
		}
		glass = d.glass + k*p120
	}
	d.queued, d.glass = queue, glass
	return Sample{Ready: d.at, GPUDone: done, Glass: glass}
}

// TestEstimatorHeldQueueStaysDirect: on a direct surface, pictures queued
// behind one the display keeps up are late by a refresh or two without a
// compositor; the mode stays direct at every report, also where the queue
// lasts long enough for the refresh grid to lock on it. Queues held up to
// 52% of a window's pictures in the stream measured.
func TestEstimatorHeldQueueStaysDirect(t *testing.T) {
	for _, c := range []struct {
		name         string
		queue, every int
		grid         bool
	}{{"off the grid", 60, 180, false}, {"grid locked in queues", 66, 150, true}} {
		var e Estimator
		e.SetNominalPeriod(p120)
		d := &holdDisplay{at: int64(time.Second), rng: rand.New(rand.NewPCG(11, 12))}
		modes := map[Mode]int{}
		for i := range 3600 {
			queue := i%c.every >= c.every-c.queue
			s := d.next(queue)
			s.Frame = uint32(i)
			var g Grid
			if c.grid && queue {
				g = Grid{Period: p120, Anchor: s.Glass}
			}
			e.Observe(s, g)
			if i%15 == 14 {
				modes[e.Report().Mode]++
			}
		}
		if modes[ModeComposited] > 0 || modes[ModeDirect] < 200 {
			t.Errorf("%s: reports by mode %v, want direct at every report", c.name, modes)
		}
	}
}

// TestEstimatorSteadyCompositor: a compositor that shows every picture of a
// 60 fps stream a refresh late, two refreshes apart on the locked grid, as
// pictures paced to the refresh come in a window, looks held picture by
// picture; with nearly all of them so, it is read as composited.
func TestEstimatorSteadyCompositor(t *testing.T) {
	var e Estimator
	e.SetNominalPeriod(p120)
	g := Grid{Period: p120, Anchor: int64(time.Second)}
	for i := range int64(4 * modeWindow) {
		v := int64(time.Second) + i*2*p120
		done := v - p120 + 2*int64(time.Millisecond)
		e.Observe(Sample{Frame: uint32(i), Ready: done - 3*int64(time.Millisecond), GPUDone: done, Glass: v + p120}, g)
		if i%15 == 14 {
			e.Report()
		}
	}
	if r := e.Report(); r.Mode != ModeComposited {
		t.Fatalf("report %+v: want composited", r)
	}
}

// TestEstimatorBurstKeepsDirect: two seconds of late pictures on a direct
// surface do not turn the mode composited; a compositor that stays does.
func TestEstimatorBurstKeepsDirect(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	ms := int64(time.Millisecond)
	var e Estimator
	e.SetNominalPeriod(p120)
	feedFree(&e, modeWindow, 2*ms, ms, rng)
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v, want direct", r.Mode)
	}
	for range 8 {
		feedFree(&e, 15, 2*ms, 2*p120, rng)
		if r := e.Report(); r.Mode != ModeDirect {
			t.Fatalf("mode %v at %.0f%% late during a 2 s burst, want direct", r.Mode, r.CompositedPct)
		}
	}
	feedFree(&e, modeWindow, 2*ms, ms, rng)
	e.Report()
	// A window's worth of reports fills it, then spreadReports in a row.
	var r Report
	for range modeWindow/15 + spreadReports {
		feedFree(&e, 15, 2*ms, 2*p120, rng)
		r = e.Report()
	}
	if r.Mode != ModeComposited {
		t.Fatalf("mode %v at %.0f%% late, want composited", r.Mode, r.CompositedPct)
	}
}

// TestEstimatorQueueStartIsHeld: the first picture of a queue comes a
// whole number of refreshes after the one before like the rest of it; it
// waited for the display too, which only the next picture shows.
func TestEstimatorQueueStartIsHeld(t *testing.T) {
	ms := int64(time.Millisecond)
	var e Estimator
	e.SetNominalPeriod(p120)
	at, glass := int64(time.Second), int64(time.Second)
	for i := range 960 {
		// Off the refresh grid between queues.
		at += 2*p120 + ms + int64(i%3)*200_000
		done := at + 4*ms
		switch i % 8 {
		case 6, 7:
			// Done 5 ms after the one before reached the screen, shown two
			// refreshes after it.
			done = glass + 5*ms
			glass += 2 * p120
		default:
			glass = max(done, glass+ms) + 1800*int64(time.Microsecond)
		}
		e.Observe(Sample{Frame: uint32(i), Ready: done - 4*ms, GPUDone: done, Glass: glass}, Grid{})
		if i%15 == 14 {
			if r := e.Report(); r.Mode != ModeDirect && i >= minModeSet {
				t.Fatalf("picture %d: mode %v at %.0f%% late, want direct", i, r.Mode, r.CompositedPct)
			}
		}
	}
}
