package phaselock

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// loop is a host capturing frames every hostPeriod, steered by a
// Controller, and a client display refreshing every displayPeriod that
// measures them with an Estimator and reports every reportEvery frames,
// delayed by reportLag frames. Both run on one clock.
type loop struct {
	ctl                       *Controller
	est                       Estimator
	hostPeriod, displayPeriod int64
	delay, jitter             int64 // capture to ready on the client
	reportEvery, reportLag    int
	rng                       *rand.Rand

	at        int64 // next capture
	steps     []int64
	slack     []int64
	reports   []Report
	errSigns  int
	lastSign  int
	inflight  []Report
	inflightF []int
}

func newLoop(hostPeriod, displayPeriod int64, steerable bool) *loop {
	return &loop{
		ctl:        NewController(ControllerConfig{FramePeriod: hostPeriod, Steerable: steerable}),
		hostPeriod: hostPeriod, displayPeriod: displayPeriod,
		delay: 5 * int64(time.Millisecond), jitter: 200 * int64(time.Microsecond),
		reportEvery: 30, reportLag: 3, rng: rand.New(rand.NewPCG(11, 12)),
		at: 1e9 + 2345678,
	}
}

func (l *loop) run(frames int) {
	g := Grid{Period: l.displayPeriod, Anchor: 1e9}
	for f := range frames {
		step := l.ctl.Advance(uint32(f))
		l.steps = append(l.steps, step)
		l.at += step
		ready := l.at + l.delay
		if l.jitter > 0 {
			ready += l.rng.Int64N(2*l.jitter) - l.jitter
		}
		done := ready + 300*int64(time.Microsecond)
		l.est.Observe(Sample{Frame: uint32(f), Ready: ready, GPUDone: done, Glass: g.Next(done)}, g)
		l.slack = append(l.slack, g.Next(ready)-ready)
		if f%l.reportEvery == l.reportEvery-1 {
			l.inflight = append(l.inflight, l.est.Report())
			l.inflightF = append(l.inflightF, f+l.reportLag)
		}
		for len(l.inflight) > 0 && l.inflightF[0] <= f {
			r := l.inflight[0]
			l.inflight, l.inflightF = l.inflight[1:], l.inflightF[1:]
			l.ctl.Update(r)
			l.reports = append(l.reports, r)
			if r.Valid {
				s := 0
				if r.ErrorNs > deadband {
					s = 1
				} else if r.ErrorNs < -deadband {
					s = -1
				}
				if s != 0 && l.lastSign != 0 && s != l.lastSign {
					l.errSigns++
				}
				if s != 0 {
					l.lastSign = s
				}
			}
		}
		l.at += l.hostPeriod
	}
}

func (l *loop) last() Report { return l.reports[len(l.reports)-1] }

func TestControllerConverges(t *testing.T) {
	for _, start := range []int64{0, 1, 3, 5, 7, 8} {
		l := newLoop(p120, p120+107, true) // the display 13 ppm slow
		l.at += start * int64(time.Millisecond)
		l.run(120 * 20)
		r := l.last()
		if !r.Locked || abs(r.ErrorNs) > LockTolerance {
			t.Errorf("start +%d ms: last report %+v: want locked", start, r)
			continue
		}
		if got := r.SlackNs; got < int64(time.Millisecond) || got > 3*int64(time.Millisecond) {
			t.Errorf("start +%d ms: slack %.2f ms, want 1-3 ms before the refresh", start, float64(got)/1e6)
		}
		st := l.ctl.State()
		if !st.Active || st.Corrections > 5 {
			t.Errorf("start +%d ms: state %+v: want active within 5 moves", start, st)
		}
		if l.errSigns > 1 {
			t.Errorf("start +%d ms: the error changed sign %d times: oscillating", start, l.errSigns)
		}
	}
}

func TestControllerSlewIsBounded(t *testing.T) {
	l := newLoop(p120, p120, true)
	l.at += 4 * int64(time.Millisecond)
	l.run(120 * 10)
	lim := p120/slewDiv + p120/trimDiv
	moved := false
	for i, s := range l.steps {
		if abs(s) > lim {
			t.Fatalf("frame %d moved %d ns, over %d", i, s, lim)
		}
		moved = moved || s != 0
	}
	if !moved {
		t.Fatal("nothing moved")
	}
}

// A drift between the clocks is taken up by the rate trim, not by a
// stream of moves.
func TestControllerTrimsDrift(t *testing.T) {
	l := newLoop(p120, p120+400, true) // 48 ppm
	l.jitter = 0
	l.run(120 * 60)
	st := l.ctl.State()
	if st.TrimNs <= 0 {
		t.Fatalf("trim %d: a slower display wants later frames", st.TrimNs)
	}
	if st.Corrections > 8 {
		t.Fatalf("%d moves in a minute: the trim is not taking up the drift", st.Corrections)
	}
	if r := l.last(); !r.Locked {
		t.Fatalf("last report %+v: want locked", r)
	}
}

func TestControllerStaysOff(t *testing.T) {
	cases := []struct {
		name         string
		host, disp   int64
		steerable    bool
		jitter       int64
		wantActive   bool
		reasonSubstr string
	}{
		{"display capture", p120, p120, false, 200 * int64(time.Microsecond), false, "display's refresh"},
		{"60 fps on 144 Hz", int64(time.Second / 60), int64(time.Second / 144), true, 200 * int64(time.Microsecond), false, "whole fraction"},
		{"144 fps on 120 Hz", int64(time.Second / 144), p120, true, 200 * int64(time.Microsecond), false, "whole fraction"},
		{"240 fps on 120 Hz", int64(time.Second / 240), p120, true, 200 * int64(time.Microsecond), false, "whole fraction"},
		{"jitter", p120, p120, true, 4 * int64(time.Millisecond), false, "jitter"},
		{"60 fps on 120 Hz", int64(time.Second / 60), p120, true, 200 * int64(time.Microsecond), true, ""},
	}
	for _, c := range cases {
		l := newLoop(c.host, c.disp, c.steerable)
		l.jitter = c.jitter
		l.run(120 * 5)
		st := l.ctl.State()
		if st.Active != c.wantActive {
			t.Errorf("%s: state %+v, want active %v", c.name, st, c.wantActive)
			continue
		}
		if !c.wantActive {
			for i, s := range l.steps {
				if s != 0 {
					t.Errorf("%s: frame %d moved %d ns while off", c.name, i, s)
					break
				}
			}
			if !contains(st.Reason, c.reasonSubstr) {
				t.Errorf("%s: reason %q, want %q in it", c.name, st.Reason, c.reasonSubstr)
			}
		}
	}
}

// Jitter that rises past the limit stops steering, and steering comes
// back only once it fell well under it.
func TestControllerJitterHysteresis(t *testing.T) {
	c := NewController(ControllerConfig{FramePeriod: p120, Steerable: true})
	r := Report{Valid: true, PeriodNs: p120, Oldest: 0, Newest: 31}
	r.JitterNs = p120/noisyDiv + 1
	c.Update(r)
	if c.State().Active {
		t.Fatal("active at high jitter")
	}
	r.JitterNs = p120/noisyDiv - 1
	c.Update(r)
	if c.State().Active {
		t.Fatal("active again just under the limit")
	}
	r.JitterNs = p120/quietDiv - 1
	c.Update(r)
	if !c.State().Active {
		t.Fatal("not active once quiet")
	}
}

// A report that holds frames captured before a move finished is ignored:
// acting on it would move twice for one error.
func TestControllerWaitsForMovedFrames(t *testing.T) {
	c := NewController(ControllerConfig{FramePeriod: p120, Steerable: true})
	c.Update(Report{Valid: true, PeriodNs: p120, ErrorNs: 2 * int64(time.Millisecond), Oldest: 0, Newest: 31})
	f := uint32(32)
	for c.Advance(f) != 0 {
		f++
	}
	moved := c.State().MovedNs
	c.Update(Report{Valid: true, PeriodNs: p120, ErrorNs: 2 * int64(time.Millisecond), Oldest: f - 5, Newest: f + 26})
	for range 40 {
		c.Advance(f)
		f++
	}
	if got := c.State().MovedNs; got != moved {
		t.Fatalf("moved %d after a stale report, want %d", got, moved)
	}
	if c.State().Corrections != 1 {
		t.Fatalf("%d corrections, want 1", c.State().Corrections)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
