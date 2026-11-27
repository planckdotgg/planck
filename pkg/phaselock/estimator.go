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
	// ready for, CompositedPct the share that took longer from the GPU
	// finishing them to the screen than direct scan-out does, in percent.
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
	// window pictures make the phase estimate, modeWindow the late share;
	// the presentation mode reads the pictures of the last modeSpan, at most
	// modeRing of them.
	window     = 32
	modeWindow = 120
	modeRing   = 512
	modeSpan   = int64(4 * time.Second)
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
	lateAbove     = 0.02
	// The only platform that tells GPU end times, macOS, presents with
	// display sync off: a direct surface flips as soon as the GPU is done,
	// and the picture reaches the screen a fixed scan-out time later, about
	// a fifth of a refresh. A compositor takes it at its next refresh and
	// shows it at that one or the one after, which spreads the time over a
	// refresh or two. The mode is the median of that time, in percent of
	// the refresh: direct below directBelow, composited above
	// compositedAbove, kept between them. CompositedPct counts the pictures
	// past compositedFrom. Streaming a 60 fps desktop to a 120 Hz display on
	// an M5 Pro, in two runs, the median of the last 4 s was 21-36% full
	// screen with the Metal HUD reading Direct and 46-183% in a window; over
	// 2 s the two overlapped, as the display held pictures for seconds (see
	// below). A host steering the GPU to finish pictures within about 4 ms
	// of the refresh would bring a compositor's median down too; it was not
	// measured.
	directBelow     = 38
	compositedFrom  = 41
	compositedAbove = 44
	// A picture shown exactly two or more refreshes after the one before
	// it, though ready at least half a refresh earlier and before that one
	// had been up two refreshes, waited for the display to let the one
	// before go, not for a compositor. Measured streaming a 60 fps desktop
	// full screen on a 120 Hz display at 5120x1440 on an M5 Pro, the
	// Metal HUD reading Direct throughout: macOS kept each picture up at
	// least two refreshes, and once one came early every later one queued
	// behind it for seconds, 13-24 ms from GPU end to screen, up to 69% of
	// two seconds' pictures. Such pictures, and stalls past stallRefreshes,
	// say nothing of the mode, unless past heldMost of them are held: a
	// compositor that delays every picture by the same refreshes looks held
	// too.
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

	// The last modeRing pictures, im the next slot and nm how many there
	// are: extra is the refreshes after the one direct scan-out makes, -1
	// unknown; toGlass the time from the GPU finishing it to the screen, -1
	// unknown; glass when it reached the screen; gridded whether the
	// refresh grid was locked; held whether it waited for the display to
	// let the one before go.
	extra   [modeRing]int8
	toGlass [modeRing]int64
	glass   [modeRing]int64
	gridded [modeRing]bool
	held    [modeRing]bool
	nm, im  int
	nominal int64
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

// SetNominalPeriod gives the display's shortest refresh period as the
// system states it. The mode is read against it, and a grid of a shorter
// period, or of a whole multiple of it, is taken for a harmonic of the
// refresh.
func (e *Estimator) SetNominalPeriod(ns int64) {
	e.mu.Lock()
	e.nominal = ns
	e.mu.Unlock()
}

// Reset forgets every picture measured and the mode, as when the
// pictures start reaching the screen another way, such as a surface going
// direct: those of the old path would hold its mode for a whole
// modeSpan. The nominal period stays.
func (e *Estimator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n, e.i, e.nm, e.im = 0, 0, 0, 0
	e.period, e.margin, e.lastGlass = 0, 0, 0
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

// Harmonic brings a refresh period measured from screen times to the
// display's stated one, nominal: a period a whole number of times nominal
// is that many refreshes, as a 60 fps stream on a 120 Hz display shows
// every other one. It reports false for a period shorter than nominal,
// which no display refreshes at. With nominal 0 it keeps period.
func Harmonic(period, nominal, tolerance int64) (int64, bool) {
	if nominal <= 0 || period <= 0 {
		return period, true
	}
	if period < nominal-tolerance {
		return 0, false
	}
	if k := (period + nominal/2) / nominal; k >= 2 && abs(period-k*nominal) <= k*tolerance {
		return period / k, true
	}
	return period, true
}

// Observe takes a picture that reached the screen and the display's
// refreshes as known then: a zero Grid where they are not.
func (e *Estimator) Observe(s Sample, g Grid) {
	if s.Ready == 0 || s.Glass == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := Harmonic(g.Period, e.nominal, e.nominal/100); ok {
		g.Period = p
	} else {
		g = Grid{}
	}
	p := g.Period
	if p <= 0 {
		p = e.nominal
	}
	if p <= 0 {
		p = e.period
	}
	gap, early := e.holdGap(s.GPUDone, s.Glass, p)
	if g.Period > 0 && e.period != 0 && abs(g.Period-e.period)*100 > e.period {
		// The display changed, or the grid locked afresh off pictures shown
		// off it: the phase measured is of another grid. The mode reads
		// against the stated refresh where there is one, which a grid does
		// not change.
		e.n, e.i = 0, 0
		if e.nominal <= 0 {
			e.nm, e.im, e.pending = 0, 0, 0
		}
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
	e.glass[e.im], e.gridded[e.im], e.held[e.im], e.extra[e.im] = s.Glass, g.Period > 0, held, -1
	if g.Period > 0 {
		e.period = g.Period
		e.slack[e.i] = g.Next(s.Ready) - s.Ready
		e.render[e.i] = -1
		e.frames[e.i] = s.Frame
		if s.GPUDone >= s.Ready && s.GPUDone-s.Ready < g.Period*4 {
			e.render[e.i] = s.GPUDone - s.Ready
			k := (s.Glass - g.Next(s.GPUDone) + g.Period/2) / g.Period
			e.extra[e.im] = int8(min(max(k, 0), 3))
		}
		e.i = (e.i + 1) % window
		e.n = min(e.n+1, window)
	}
	e.im = (e.im + 1) % modeRing
	e.nm = min(e.nm+1, modeRing)
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
	e.updateMode(&r)
	if !e.updateLate(&r) {
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

// slot is the ring slot of the picture back pictures before the newest.
func (e *Estimator) slot(back int) int {
	return (e.im - 1 - back + 2*modeRing) % modeRing
}

// updateMode reads the presentation mode from the time the pictures of
// the last modeSpan took from the GPU finishing them to the screen (see
// directBelow), against the display's stated refresh where it is known.
func (e *Estimator) updateMode(r *Report) {
	p := e.nominal
	if p <= 0 {
		p = e.period
	}
	if p <= 0 || e.nm == 0 {
		return
	}
	from := e.glass[e.slot(0)] - modeSpan
	n := 0
	for n < e.nm && e.glass[e.slot(n)] > from {
		n++
	}
	skip := e.skipHeld(n)
	var vals [modeRing]int64
	kept, timed := 0, 0
	for b := range n {
		k := e.slot(b)
		v := e.toGlass[k]
		if v < 0 {
			continue
		}
		timed++
		if v <= stallRefreshes*p && !(skip && e.held[k]) {
			vals[kept] = v
			kept++
		}
	}
	if timed < minModeSet || kept < minModeSet/2 {
		return
	}
	v := vals[:kept]
	slices.Sort(v)
	over := 0
	for _, x := range v {
		if x*100 > compositedFrom*p {
			over++
		}
	}
	r.CompositedPct = 100 * float64(over) / float64(kept)
	switch med := v[kept/2] * 100; {
	case med > compositedAbove*p:
		e.mode = ModeComposited
	case med < directBelow*p:
		e.mode = ModeDirect
	}
}

// updateLate reads the share of late pictures from the last modeWindow
// whose GPU times are known on the refresh grid. It reports whether most
// of them came on a locked grid.
func (e *Estimator) updateLate(r *Report) bool {
	n := min(e.nm, modeWindow)
	gridded := 0
	for b := range n {
		if e.gridded[e.slot(b)] {
			gridded++
		}
	}
	if gridded*2 < n {
		return false
	}
	skip := e.skipHeld(n)
	base := int8(0)
	if e.mode == ModeComposited {
		base = 1
	}
	known, late := 0, 0
	for b := range n {
		k := e.slot(b)
		if x := e.extra[k]; x >= 0 && !(skip && e.held[k]) {
			known++
			if x > base {
				late++
			}
		}
	}
	if known >= minModeSet {
		r.LatePct = 100 * float64(late) / float64(known)
	}
	return true
}

// skipHeld reports whether to leave the held pictures among the last n
// out: where they are few enough (see heldMost), or too few pictures came
// to tell.
func (e *Estimator) skipHeld(n int) bool {
	held := 0
	for b := range n {
		if e.held[e.slot(b)] {
			held++
		}
	}
	return n < modeWindow || float64(held) <= heldMost*float64(n)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
