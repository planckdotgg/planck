package player

import (
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
