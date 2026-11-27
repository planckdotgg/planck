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

// scanOut is how long after the GPU finished it a picture a direct surface
// flipped reached the screen, and wsDeadline how long before a refresh the
// GPU had to finish one for WindowServer to show it composited at that
// refresh, as measured at 60 fps on a 120 Hz display on an M5 Pro.
const (
	scanOut    = int64(1800 * time.Microsecond)
	wsDeadline = int64(1200 * time.Microsecond)
)

// feed gives e pictures ready slack before a refresh, give or take
// jitter, drawn in render, and shown direct, scanOut after the GPU finished
// them, or extra refreshes after the one direct scan-out would have made.
func feed(e *Estimator, g Grid, n int, slack, jitter, render int64, extra int, rng *rand.Rand) {
	first := int64(10)
	if e.lastGlass > g.Anchor {
		first = (e.lastGlass-g.Anchor)/g.Period + 2
	}
	for i := range n {
		v := g.Anchor + (int64(i)+first)*g.Period
		s := slack
		if jitter > 0 {
			s += rng.Int64N(2*jitter) - jitter
		}
		ready := v - s
		done := ready + render
		glass := done + scanOut
		if extra > 0 {
			glass = g.Next(done) + int64(extra)*g.Period
		}
		e.Observe(Sample{Frame: uint32(i), Ready: ready, GPUDone: done, Glass: glass}, g)
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
	// A third of the pictures direct: still composited.
	feed(&e, g, modeWindow/2, 4*int64(time.Millisecond), 0, 0, 0, nil)
	if r := e.Report(); r.Mode != ModeComposited {
		t.Fatalf("mode %v at %.0f%%, want composited kept", r.Mode, r.CompositedPct)
	}
	feed(&e, g, int(modeSpan/p120), 4*int64(time.Millisecond), 0, 0, 0, nil)
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

// feedFree gives e n pictures 10 ms apart, after the last it took, that
// reach the screen off any refresh grid, toGlass after the GPU finished
// them, give or take spread.
func feedFree(e *Estimator, n int, toGlass, spread int64, rng *rand.Rand) {
	start := max(e.lastGlass, int64(time.Second))
	for i := range n {
		ready := start + int64(i+1)*10_000_000
		done := ready + int64(time.Millisecond)
		d := toGlass
		if spread > 0 {
			d += rng.Int64N(spread)
		}
		e.Observe(Sample{Frame: uint32(i), Ready: ready, GPUDone: done, Glass: done + d}, Grid{})
	}
}

// TestEstimatorDirectSurfaceCuts: direct flips that come up to 4 ms late,
// a median of 55% of the refresh, read composited for a window's surface
// and direct for one that asked to scan out directly; a compositor that
// shows the pictures a refresh or two after the GPU reads composited for
// both.
func TestEstimatorDirectSurfaceCuts(t *testing.T) {
	ms := int64(time.Millisecond)
	for _, c := range []struct {
		name            string
		toGlass, spread int64
		want            [2]Mode
	}{
		{"late flips", 2600 * int64(time.Microsecond), 4 * ms, [2]Mode{ModeComposited, ModeDirect}},
		{"compositor", p120, p120, [2]Mode{ModeComposited, ModeComposited}},
	} {
		for i, direct := range []bool{false, true} {
			rng := rand.New(rand.NewPCG(31, 32))
			var e Estimator
			e.SetNominalPeriod(p120)
			e.SetDirectSurface(direct)
			e.Reset()
			feedFree(&e, int(modeSpan/int64(10*ms)), c.toGlass, c.spread, rng)
			if r := e.Report(); r.Mode != c.want[i] {
				t.Errorf("%s, direct surface %v: mode %v at %.0f%%, want %v", c.name, direct, r.Mode, r.CompositedPct, c.want[i])
			}
		}
	}
}

// Where pictures reach the screen as soon as they are drawn, the mode
// comes from the spread of their time to it, and a burst of late ones
// does not flip it.
func TestEstimatorModeWithoutGrid(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	var e Estimator
	e.SetNominalPeriod(p120)
	feedFree(&e, modeWindow, scanOut, 500*int64(time.Microsecond), rng)
	r := e.Report()
	if r.Mode != ModeDirect || r.Valid || r.PeriodNs != p120 {
		t.Fatalf("report %+v: want direct at the stated refresh, no phase", r)
	}
	// A second of a compositor: the mode holds.
	feedFree(&e, 100, scanOut, p120, rng)
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v after a second composited, want direct kept", r.Mode)
	}
	// A compositor through most of the span turns it.
	for range modeSpan / int64(300*time.Millisecond) {
		feedFree(&e, 30, scanOut, p120, rng)
		r = e.Report()
	}
	if r.Mode != ModeComposited || r.CompositedPct < 50 {
		t.Fatalf("report %+v: want composited", r)
	}
	for range modeSpan / int64(300*time.Millisecond) {
		feedFree(&e, 30, scanOut, 500*int64(time.Microsecond), rng)
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
	var r Report
	for range modeSpan / int64(150*time.Millisecond) {
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

// TestEstimatorWindowWithGridOnAndOff: in a window at 60 fps on 120 Hz the
// compositor shows some pictures off the refreshes, and the refresh grid
// locks only while it shows them on: for about half of a mode window at a
// time. Of the pictures on the grid most reach the screen at the first
// refresh after the GPU finished and a quarter a refresh later, which on
// its own reads direct; the spread of them all is a compositor's. The host
// does not steer the phase, so the GPU finishes them anywhere in the
// refresh before, as long as the compositor still takes them for it.
func TestEstimatorWindowWithGridOnAndOff(t *testing.T) {
	ms := int64(time.Millisecond)
	rng := rand.New(rand.NewPCG(21, 22))
	var e Estimator
	e.SetNominalPeriod(p120)
	origin := int64(time.Second)
	modes := map[Mode]int{}
	for i := range int64(3600) {
		v := origin + (2*i+1)*p120
		done := v - wsDeadline - rng.Int64N(p120-wsDeadline)
		var g Grid
		glass := done + 1500*int64(time.Microsecond) + rng.Int64N(p120)
		if (i/60)%2 == 0 {
			g = Grid{Period: p120, Anchor: origin}
			glass = v
			if rng.IntN(100) < 28 {
				glass += p120
			}
		}
		e.Observe(Sample{Frame: uint32(i), Ready: done - 2*ms, GPUDone: done, Glass: glass}, g)
		if i%15 == 14 && i >= modeWindow {
			modes[e.Report().Mode]++
		}
	}
	if modes[ModeDirect] > 0 || modes[ModeComposited] < 200 {
		t.Fatalf("reports by mode %v, want composited at every report", modes)
	}
}

// TestEstimatorIgnoresHarmonicGrid: a grid a third of the display's
// refresh, as a clock locked on pictures shown off its refreshes gives, is
// no refresh grid: the report keeps the display's 120 Hz, not 360 Hz.
func TestEstimatorIgnoresHarmonicGrid(t *testing.T) {
	ms := int64(time.Millisecond)
	rng := rand.New(rand.NewPCG(23, 24))
	var e Estimator
	e.SetNominalPeriod(p120)
	origin := int64(time.Second)
	g := Grid{Period: p120 / 3, Anchor: origin}
	for i := range int64(4 * modeWindow) {
		v := origin + (2*i+1)*p120
		done := v - 3*ms + rng.Int64N(ms)
		e.Observe(Sample{Frame: uint32(i), Ready: done - 2*ms, GPUDone: done, Glass: v}, g)
		if i%15 == 14 {
			if r := e.Report(); r.PeriodNs != p120 {
				t.Fatalf("picture %d: %.3f Hz, want 120", i, r.RefreshHz())
			}
		}
	}
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v at %.0f%% late, want direct", r.Mode, r.CompositedPct)
	}
}

// TestEstimatorFoldsSlowerHarmonic: a window at 60 fps on 120 Hz whose
// compositor shows each picture a refresh after the GPU finished it, with
// the refresh grid locked on every other refresh, as a clock locked on a
// stream at half the refresh gives. On that grid every picture reaches the
// screen at the first refresh after its GPU end, which read direct at
// 60 Hz; the report keeps the display's 120 Hz and reads composited.
func TestEstimatorFoldsSlowerHarmonic(t *testing.T) {
	ms := int64(time.Millisecond)
	rng := rand.New(rand.NewPCG(25, 26))
	var e Estimator
	e.SetNominalPeriod(p120)
	origin := int64(time.Second)
	g := Grid{Period: 2 * p120, Anchor: origin}
	for i := range int64(4 * modeWindow) {
		v := origin + (2*i+1)*p120
		done := v - wsDeadline - rng.Int64N(p120-wsDeadline)
		e.Observe(Sample{Frame: uint32(i), Ready: done - 2*ms, GPUDone: done, Glass: v + p120}, g)
		if i%15 == 14 && i >= modeWindow {
			r := e.Report()
			if r.PeriodNs != p120 || r.Mode != ModeComposited {
				t.Fatalf("picture %d: %.3f Hz, %v at %.0f%%; want 120 Hz, composited", i, r.RefreshHz(), r.Mode, r.CompositedPct)
			}
		}
	}
}

// TestEstimatorGridRelockKeepsMode: on a direct surface the refresh clock
// locks now and then on pictures flipped off the refreshes, at a period a
// few percent off the display's, measured 115-121 Hz on a 120 Hz display.
// That is not another display: the mode keeps the pictures before it, and
// a second of late pictures after the relock does not turn it.
func TestEstimatorGridRelockKeepsMode(t *testing.T) {
	rng := rand.New(rand.NewPCG(27, 28))
	var e Estimator
	e.SetNominalPeriod(p120)
	feedFree(&e, 300, scanOut, 600_000, rng)
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v, want direct", r.Mode)
	}
	at := e.lastGlass
	for i, period := range []int64{p120, p120 * 104 / 100} {
		g := Grid{Period: period, Anchor: at}
		for j := range 50 {
			done := at + int64(j+1)*2*p120
			e.Observe(Sample{Frame: uint32(1000*i + j), Ready: done - 2*int64(time.Millisecond), GPUDone: done, Glass: done + p120 + rng.Int64N(p120)}, g)
		}
		at = e.lastGlass
	}
	if r := e.Report(); r.Mode != ModeDirect {
		t.Fatalf("mode %v at %.0f%% after a relock, want direct kept", r.Mode, r.CompositedPct)
	}
}

// TestEstimatorSteeredCompositor: with the host steering the GPU to finish
// pictures just before a refresh, a direct surface still shows each one
// scanOut later, while a compositor over it, as an overlay makes, cannot
// take a picture finished under wsDeadline before the refresh for that one
// and shows it a refresh later.
func TestEstimatorSteeredCompositor(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 30))
	g := Grid{Period: p120, Anchor: int64(time.Second)}
	for _, c := range []struct {
		name  string
		extra int
		want  Mode
	}{{"direct", 0, ModeDirect}, {"overlay", 1, ModeComposited}} {
		var e Estimator
		e.SetNominalPeriod(p120)
		for range 20 {
			feed(&e, g, 30, wsDeadline, 300_000, 400_000, c.extra, rng)
			if r := e.Report(); r.Mode != c.want {
				t.Fatalf("%s: mode %v at %.0f%%, want %v", c.name, r.Mode, r.CompositedPct, c.want)
			}
		}
	}
}
