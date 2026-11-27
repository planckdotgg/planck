package player

import (
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/phaselock"
)

// TestPhaseFromScreenTimes: the player measures each picture from when it
// was decoded, not when its present started, against the refreshes the
// screen times give, and reads direct and composited scan-out from the
// GPU's end times.
func TestPhaseFromScreenTimes(t *testing.T) {
	const period = int64(time.Second / 120)
	for _, c := range []struct {
		name  string
		extra int64
		want  phaselock.Mode
	}{{"direct", 0, phaselock.ModeDirect}, {"composited", 1, phaselock.ModeComposited}} {
		var clock atomic.Int64
		dec := &fakeDecoder{out: map[uintptr]bool{}}
		p := startWith(dec, 0, func(decode.Frame) error { return nil }, func(int, int) error { return nil }, clock.Load)
		origin := int64(time.Second)
		for i := range int64(200) {
			v := origin + (i+2)*period
			ready := v - 3*int64(time.Millisecond)
			clock.Store(ready)
			if _, err := p.Submit(nil, i); err != nil {
				t.Fatal(err)
			}
			// The present starts a little after the picture came, which
			// slack must not count.
			start := ready + 200*int64(time.Microsecond)
			done := start + 400*int64(time.Microsecond)
			p.onScreenGPU(i, start, done, v+c.extra*period)
		}
		r := p.Phase()
		p.Close()
		if !r.Valid || r.Mode != c.want {
			t.Fatalf("%s: report %+v: want valid, %v", c.name, r, c.want)
		}
		if r.SlackNs != 3*int64(time.Millisecond) || r.PeriodNs != period {
			t.Fatalf("%s: slack %d period %d, want 3 ms and %d", c.name, r.SlackNs, r.PeriodNs, period)
		}
		if r.Newest != 199 || r.Oldest != 168 {
			t.Fatalf("%s: frames %d..%d, want 168..199", c.name, r.Oldest, r.Newest)
		}
	}
}

// TestPhaseResetOnPathChange: after ResetPhase the phase reads only the
// pictures presented on the new path once it settled; ones presented
// before it that reach the screen after it, and the compositor's while it
// hands the surface over, do not count.
func TestPhaseResetOnPathChange(t *testing.T) {
	ms := int64(time.Millisecond)
	var clock atomic.Int64
	p := startWith(&fakeDecoder{out: map[uintptr]bool{}}, 0, func(decode.Frame) error { return nil }, func(int, int) error { return nil }, clock.Load)
	defer p.Close()
	p.phase.SetNominalPeriod(int64(time.Second / 120))
	at := int64(time.Second)
	rng := rand.New(rand.NewPCG(3, 4))
	present := func(pts, toGlass int64) {
		// Off any refresh grid, as a direct surface presents.
		at += 8*ms + rng.Int64N(4*ms)
		clock.Store(at)
		p.onScreenGPU(pts, at, at+ms, at+ms+toGlass)
	}
	pts := int64(0)
	for ; pts < 120; pts++ {
		present(pts, 2*ms+(pts%2)*12*ms)
	}
	if r := p.Phase(); r.Mode != phaselock.ModeComposited {
		t.Fatalf("composited path: mode %v", r.Mode)
	}
	submitted := at
	p.ResetPhase()
	for i := range int64(10) {
		// Presented on the old path, they reach the screen after the swap.
		clock.Store(at + i)
		p.onScreenGPU(pts, submitted-1, submitted, submitted+15*ms+i)
		pts++
	}
	for start := at; at < start+400*ms; pts++ {
		present(pts, 14*ms)
	}
	for range 60 {
		present(pts, 2*ms)
		pts++
	}
	if r := p.Phase(); r.Mode != phaselock.ModeDirect {
		t.Fatalf("direct path: mode %v at %.0f%% late, want direct", r.Mode, r.CompositedPct)
	}
}
