// Package phaselock lines the host's frames up with the client display's
// scan-out. The client's Estimator measures, from the refresh times its
// pictures reached the screen, how long before the next refresh each
// picture was ready and whether the system scanned it out directly or
// composited it first. The host's Controller moves the phase of the frames
// it captures so that pictures are ready a small target before a refresh:
// a picture ready just after one waits nearly a whole refresh.
package phaselock

import (
	"math"
	"slices"
	"sync"
	"time"
)

// Grid is a display's refreshes: Anchor + k*Period, on the client's screen
// clock.
type Grid struct{ Period, Anchor int64 }

// Next is the first refresh after t.
func (g Grid) Next(t int64) int64 {
	return g.Anchor + (floorDiv(t-g.Anchor, g.Period)+1)*g.Period
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// Wrap brings d into [-period/2, period/2): a phase error, which is the
// same one a whole refresh earlier or later.
func Wrap(d, period int64) int64 {
	if period <= 0 {
		return d
	}
	d %= period
	switch {
	case d >= period/2:
		d -= period
	case d < -period/2:
		d += period
	}
	return d
}

// Mode is how the system shows the pictures.
type Mode uint8

const (
	ModeUnknown Mode = iota
	// ModeDirect: the display scans each picture out at the first refresh
	// after the GPU finished it.
	ModeDirect
	// ModeComposited: a compositor draws the picture into its own frame
	// first, a refresh or more later.
	ModeComposited
)

func (m Mode) String() string {
	switch m {
	case ModeDirect:
		return "direct"
	case ModeComposited:
		return "composited"
	}
	return "unknown"
}

// Sample is one picture that reached the screen, on the screen clock.
type Sample struct {
	Frame uint32
	// Ready is when the decoded picture was ready to present; GPUDone when
	// the GPU finished drawing it into the drawable, 0 where the platform
	// does not tell; Glass when it reached the screen.
	Ready, GPUDone, Glass int64
}

// Report is the Estimator's view of the last pictures.
type Report struct {
	// Valid is set once the refresh is known and enough pictures came.
	Valid bool
	// Locked is set while the phase error stays within LockTolerance.
	Locked bool
	Mode   Mode
	// PeriodNs is the display's refresh period.
	PeriodNs int64
	// SlackNs is the median time from a picture being ready to the next
	// refresh, TargetNs the slack aimed at, and ErrorNs their difference,
	// wrapped: above 0 pictures are ready too early and wait, so the host
	// should capture later; below 0 they risk missing their refresh.
	SlackNs, TargetNs, ErrorNs int64
	// JitterNs is half the spread of the slack between its 10th and 90th
	// percentiles.
	JitterNs int64
	// LatePct is the share of pictures that missed the refresh they were
	// ready for, CompositedPct the share shown a refresh or more after the
	// one direct scan-out would have made, in percent.
	LatePct, CompositedPct float64
	// Oldest and Newest are the frames the slack was measured over.
	Oldest, Newest uint32
	Samples        int
}

// RefreshHz is the display's refresh rate, 0 before it is known.
func (r Report) RefreshHz() float64 {
	if r.PeriodNs <= 0 {
		return 0
	}
	return 1e9 / float64(r.PeriodNs)
}

const (
	// window pictures make the phase estimate, modeWindow the
	// presentation mode and late share.
	window     = 32
	modeWindow = 120
	minSamples = 16
	minModeSet = 30
	// The target is the GPU's draw time plus twice the jitter plus a
	// margin that grows while pictures miss their refresh.
	minTarget     = int64(1200 * time.Microsecond)
	startMargin   = int64(500 * time.Microsecond)
	minMargin     = int64(250 * time.Microsecond)
	marginStep    = int64(250 * time.Microsecond)
	marginDecay   = int64(10 * time.Microsecond)
	LockTolerance = int64(500 * time.Microsecond)
	unlockAt      = int64(time.Millisecond)
	// compositedAbove and directBelow are the shares of late-by-a-refresh
	// pictures that switch the mode: between them it stays.
	compositedAbove = 0.6
	directBelow     = 0.3
	lateAbove       = 0.02
	// Where pictures reach the screen as soon as they are drawn, off any
	// refresh grid, a compositor shows as spread: the share of pictures
	// that reached the screen more than half a refresh after the quickest
	// tenth did. Measured at 5120x1440 on an M5 Pro: 0-5% direct, 12-30%
	// with an overlay composited, 30-70% in a window. spreadReports in a
	// row past spreadCompositedAbove turn direct composited, and
	// spreadDirectReports below spreadDirectBelow turn it back. Direct
	// surfaces have bursts of late pictures that a compositor did not
	// cause: streaming a 60 fps desktop full screen at 120 Hz, the Metal
	// HUD reading Direct throughout, they stayed past the bound for up to 8
	// reports (2 s at the client's 4 a second); compositing lasted longer.
	spreadCompositedAbove = 0.12
	spreadDirectBelow     = 0.06
	spreadReports         = 12
	spreadDirectReports   = 3
	// A picture shown exactly two or more refreshes after the one before
	// it, though ready at least half a refresh earlier and before that one
	// had been up two refreshes, waited for the display to let the one
	// before go, not for a compositor. Measured streaming a 60 fps desktop
	// full screen on a 120 Hz display at 5120x1440 on an M5 Pro, the
	// Metal HUD reading Direct throughout: macOS kept each picture up at
	// least two refreshes, and once one came early every later one queued
	// behind it for seconds, 13-24 ms from GPU end to screen, up to 52% of
	// a window's pictures. Such pictures, and stalls past stallRefreshes,
	// say nothing of the mode, unless past heldMost of the window are held:
	// a compositor that delays every picture by the same refreshes looks
	// held too.
	onRefreshTolerance = int64(350 * time.Microsecond)
	stallRefreshes     = 3
	heldMost           = 0.75
)

// Estimator measures the phase of pictures against the display's
// refreshes. Safe for concurrent use.
type Estimator struct {
	mu sync.Mutex

	slack  [window]int64
	render [window]int64
	frames [window]uint32
	n, i   int

	extra  [modeWindow]int8 // refreshes after the one direct scan-out makes; -1 unknown
	nm, im int
	// toGlass is each picture's time from the GPU finishing it to the
	// screen, -1 unknown; gridded whether the refresh grid was locked;
	// held whether it waited for the display to let the one before go.
	toGlass [modeWindow]int64
	gridded [modeWindow]bool
	held    [modeWindow]bool
	nominal int64
	streak  int
	// lastGlass is when the picture before reached the screen; holding
	// whether it came a whole number of refreshes, two or more, after the
	// one before it, and pending its slot plus one if it was early, 0 if not.
	lastGlass int64
	holding   bool
	pending   int

	period int64
	margin int64
	mode   Mode
	locked bool
}

// SetNominalPeriod gives the display's refresh period as the system
// states it, which the mode reads by where no refresh grid locks.
func (e *Estimator) SetNominalPeriod(ns int64) {
	e.mu.Lock()
	e.nominal = ns
	e.mu.Unlock()
}

// Reset forgets every picture measured and the mode, as when the
// pictures start reaching the screen another way, such as a surface going
// direct: those of the old path would hold its mode for a whole
// modeWindow. The nominal period stays.
func (e *Estimator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n, e.i, e.nm, e.im = 0, 0, 0, 0
	e.streak, e.period, e.margin, e.lastGlass = 0, 0, 0, 0
	e.mode, e.locked, e.holding, e.pending = ModeUnknown, false, false, 0
}

// holdGap reports whether a picture shown at glass came a whole number of
// refreshes p, two or more, after the one before it, and whether it was
// done at gpuDone half a refresh before it showed and before the one before
// had been up two refreshes: then it may have waited for the display to
// let that one go (see onRefreshTolerance). It keeps glass for the next.
func (e *Estimator) holdGap(gpuDone, glass, p int64) (gap, early bool) {
	prev := e.lastGlass
	e.lastGlass = glass
	if p <= 0 || prev <= 0 || glass <= prev {
		return false, false
	}
	d := glass - prev
	k := (d + p/2) / p
	if k < 2 || abs(d-k*p) > onRefreshTolerance {
		return false, false
	}
	return true, gpuDone > 0 && gpuDone < prev+min(2*k-1, 4)*p/2
}

// Observe takes a picture that reached the screen and the display's
// refreshes as known then: a zero Grid where they are not.
func (e *Estimator) Observe(s Sample, g Grid) {
	if s.Ready == 0 || s.Glass == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p := g.Period
	if p <= 0 {
		p = e.period
	}
	if p <= 0 {
		p = e.nominal
	}
	gap, early := e.holdGap(s.GPUDone, s.Glass, p)
	if g.Period > 0 && e.period != 0 && abs(g.Period-e.period)*100 > e.period {
		// The display changed: what was measured is of another one.
		e.n, e.i, e.nm, e.im, e.pending = 0, 0, 0, 0, 0
	}
	// A hold shows as two such gaps in a row; one among pictures a
	// refresh apart is a missed refresh.
	held := gap && early && e.holding
	if k := e.pending - 1; gap && k >= 0 {
		e.held[k] = true
	}
	e.holding, e.pending = gap, 0
	if gap && early && !held {
		e.pending = e.im + 1
	}
	e.toGlass[e.im] = -1
	if s.GPUDone > 0 && s.Glass >= s.GPUDone {
		e.toGlass[e.im] = s.Glass - s.GPUDone
	}
	e.gridded[e.im], e.held[e.im] = g.Period > 0, held
	if g.Period <= 0 {
		e.extra[e.im] = -1
		e.im = (e.im + 1) % modeWindow
		e.nm = min(e.nm+1, modeWindow)
		return
	}
	e.period = g.Period
	e.slack[e.i] = g.Next(s.Ready) - s.Ready
	e.render[e.i] = -1
	e.frames[e.i] = s.Frame
	extra := int8(-1)
	if s.GPUDone >= s.Ready && s.GPUDone-s.Ready < g.Period*4 {
		e.render[e.i] = s.GPUDone - s.Ready
		k := (s.Glass - g.Next(s.GPUDone) + g.Period/2) / g.Period
		extra = int8(min(max(k, 0), 3))
	}
	e.i = (e.i + 1) % window
	e.n = min(e.n+1, window)
	e.extra[e.im] = extra
	e.im = (e.im + 1) % modeWindow
	e.nm = min(e.nm+1, modeWindow)
}

// Report summarizes the last pictures. Each call also moves the margin
// with the share of late pictures, so call it at a steady rate.
func (e *Estimator) Report() Report {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := Report{PeriodNs: e.period, Samples: e.n}
	if e.margin == 0 {
		e.margin = startMargin
	}
	if !e.updateMode(&r) {
		// No refresh grid holds: pictures reach the screen off it, and
		// the phase measured before is stale.
		r.PeriodNs, r.Samples = e.nominal, 0
		r.Mode = e.mode
		return r
	}
	if e.n < minSamples || e.period <= 0 {
		r.Mode = e.mode
		return r
	}
	p := e.period
	start := (e.i - e.n + window) % window
	r.Oldest, r.Newest = e.frames[start], e.frames[(e.i-1+window)%window]
	// Slack wraps at the refresh: unwrap it around its circular mean.
	var sx, sy float64
	for k := 0; k < e.n; k++ {
		a := 2 * math.Pi * float64(e.slack[k]) / float64(p)
		sx, sy = sx+math.Cos(a), sy+math.Sin(a)
	}
	mean := int64(math.Atan2(sy, sx) / (2 * math.Pi) * float64(p))
	var un, rd [window]int64
	nr := 0
	for k := 0; k < e.n; k++ {
		un[k] = mean + Wrap(e.slack[k]-mean, p)
		if e.render[k] >= 0 {
			rd[nr] = e.render[k]
			nr++
		}
	}
	s := un[:e.n]
	slices.Sort(s)
	r.SlackNs = s[len(s)/2]
	r.JitterNs = (s[len(s)*9/10] - s[len(s)/10]) / 2
	if r.SlackNs <= 0 {
		r.SlackNs += p
	} else if r.SlackNs > p {
		r.SlackNs -= p
	}
	render := int64(0)
	if nr > 0 {
		rs := rd[:nr]
		slices.Sort(rs)
		render = rs[nr*9/10]
	}
	switch {
	case r.LatePct > 100*lateAbove:
		e.margin = min(e.margin+marginStep, p/3)
	case r.LatePct == 0:
		e.margin = max(e.margin-marginDecay, minMargin)
	}
	r.TargetNs = min(max(render+2*r.JitterNs+e.margin, minTarget), p/2)
	r.ErrorNs = Wrap(r.SlackNs-r.TargetNs, p)
	switch {
	case abs(r.ErrorNs) <= LockTolerance:
		e.locked = true
	case abs(r.ErrorNs) > unlockAt:
		e.locked = false
	}
	r.Valid, r.Locked, r.Mode = true, e.locked, e.mode
	return r
}

// updateMode reads the presentation mode and the late share from the
// pictures whose GPU times are known: by the refresh grid where it is
// locked, else by the spread of their time to the screen. It reports whether most of them came on a locked grid.
func (e *Estimator) updateMode(r *Report) bool {
	gridded := 0
	for k := 0; k < e.nm; k++ {
		if e.gridded[k] {
			gridded++
		}
	}
	if gridded*2 < e.nm {
		e.updateModeBySpread(r)
		return false
	}
	e.streak = 0
	skip := e.skipHeld()
	var known, comp int
	for k := 0; k < e.nm; k++ {
		if x := e.extra[k]; x >= 0 && !(skip && e.held[k]) {
			known++
			if x >= 1 {
				comp++
			}
		}
	}
	if known < minModeSet {
		return true
	}
	frac := float64(comp) / float64(known)
	switch {
	case frac > compositedAbove:
		e.mode = ModeComposited
	case frac < directBelow:
		e.mode = ModeDirect
	}
	r.CompositedPct = 100 * frac
	base := int8(0)
	if e.mode == ModeComposited {
		base = 1
	}
	late := 0
	for k := 0; k < e.nm; k++ {
		if e.extra[k] > base && !(skip && e.held[k]) {
			late++
		}
	}
	r.LatePct = 100 * float64(late) / float64(known)
	return true
}

func (e *Estimator) updateModeBySpread(r *Report) {
	p := e.period
	if p <= 0 {
		p = e.nominal
	}
	var vals [modeWindow]int64
	n, timed := 0, 0
	skip := e.skipHeld()
	for k := 0; k < e.nm; k++ {
		v := e.toGlass[k]
		if v < 0 {
			continue
		}
		timed++
		if v <= stallRefreshes*p && !(skip && e.held[k]) {
			vals[n] = v
			n++
		}
	}
	if p <= 0 || timed < minModeSet || n < minModeSet/2 {
		return
	}
	v := vals[:n]
	slices.Sort(v)
	floor := v[n/10]
	late := 0
	for _, x := range v {
		if x-floor > p/2 {
			late++
		}
	}
	frac := float64(late) / float64(n)
	r.CompositedPct = 100 * frac
	want := e.mode
	switch {
	case frac > spreadCompositedAbove:
		want = ModeComposited
	case frac < spreadDirectBelow:
		want = ModeDirect
	}
	switch {
	case e.mode == ModeUnknown:
		e.mode, e.streak = want, 0
	case want != e.mode:
		need := spreadReports
		if want == ModeDirect {
			need = spreadDirectReports
		}
		if e.streak++; e.streak >= need {
			e.mode, e.streak = want, 0
		}
	default:
		e.streak = 0
	}
}

// skipHeld reports whether to leave the held pictures out of the mode: in
// a window not yet full, or where they are few enough (see heldMost).
func (e *Estimator) skipHeld() bool {
	held := 0
	for k := 0; k < e.nm; k++ {
		if e.held[k] {
			held++
		}
	}
	return e.nm < modeWindow || float64(held) <= heldMost*float64(e.nm)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
