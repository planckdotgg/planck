package player

// PaceSmooth locks only on screen times, which only Core Animation gives
// (Windows has none: PaceSmooth is PaceLowestLatency there), so its
// simulated display runs where the feature does, on Go's darwin timers.

import (
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/decode"
)

// display is a 120 Hz screen: a picture presented at t shows at the first
// refresh at least latch after t, and its time comes back as Core
// Animation's presented handler does.
type display struct {
	p      *Player
	origin int64
	latch  int64
	mu     sync.Mutex
	shows  map[int64][]int64 // refresh → PTS shown on it
	landed map[int64]int64   // PTS → refresh
}

func (d *display) show(f decode.Frame) error {
	now := d.p.now()
	at := d.origin + ((now+d.latch-d.origin)/refresh120+1)*refresh120
	d.mu.Lock()
	d.shows[at] = append(d.shows[at], f.PTS)
	d.landed[f.PTS] = at
	d.mu.Unlock()
	time.AfterFunc(time.Duration(at-now), func() { d.p.onScreen(f.PTS, now, at) })
	return nil
}

type pacingRun struct {
	superseded, hidden, repeats, shown int
	meanLatency                        time.Duration
}

// runPacing feeds frames 120 a second with arrival jitter up to ±jitter
// and counts the judder each mode leaves: frames never shown (superseded
// unshown, or hidden behind another on the same refresh), and refreshes
// that show no new frame while frames keep coming.
func runPacing(t *testing.T, m Pacing, jitter time.Duration) pacingRun {
	dec := &fakeDecoder{out: map[uintptr]bool{}}
	d := &display{latch: int64(1500 * time.Microsecond), shows: map[int64][]int64{}, landed: map[int64]int64{}}
	var p *Player
	p = startWith(dec, 0, func(f decode.Frame) error { return d.show(f) }, func(int, int) error { return nil }, monoNow)
	d.p = p
	d.origin = monoNow()
	p.SetPacing(m)
	rng := rand.New(rand.NewPCG(7, 9))
	const frames = 480
	arrived := make([]int64, frames)
	// Arrivals centre on the latest a present can make a refresh, where
	// the live 120 Hz session's 6.9 ms frame-time jitter put them.
	base := d.origin + 3*refresh120 - d.latch
	for i := range frames {
		due := base + int64(i)*refresh120 + rng.Int64N(2*int64(jitter)) - int64(jitter)
		for monoNow() < due {
			time.Sleep(time.Duration(min(due-monoNow(), int64(200*time.Microsecond))))
		}
		arrived[i] = monoNow()
		if _, err := p.Submit(nil, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	p.Close()
	d.mu.Lock()
	defer d.mu.Unlock()
	var r pacingRun
	r.superseded = p.Stats().Superseded
	// Judge the second half, after the clock locked.
	var refreshes []int64
	var sum time.Duration
	for pts, at := range d.landed {
		if pts >= frames/2 {
			refreshes = append(refreshes, at)
			sum += time.Duration(at - arrived[pts])
			r.shown++
		}
	}
	slices.Sort(refreshes)
	unique := slices.Compact(refreshes)
	// Two frames on one refresh: the first was never seen.
	r.hidden = r.shown - len(unique)
	for i := 1; i < len(unique); i++ {
		r.repeats += int((unique[i]-unique[i-1])/refresh120) - 1
	}
	r.meanLatency = sum / time.Duration(max(r.shown, 1))
	return r
}

// TestSmoothPacingRemovesJudder: frames at the display's rate whose
// arrival jitters across the refresh deadline are each shown on a refresh
// of their own with PaceSmooth, where PaceLowestLatency drops some and
// repeats others, for at most a refresh more latency.
func TestSmoothPacingRemovesJudder(t *testing.T) {
	low := runPacing(t, PaceLowestLatency, 3*time.Millisecond)
	smooth := runPacing(t, PaceSmooth, 3*time.Millisecond)
	for _, r := range []struct {
		name string
		pacingRun
	}{{"lowest latency", low}, {"smooth", smooth}} {
		t.Logf("%-14s %d superseded, %d hidden, %d repeated refreshes of %d frames; mean arrival to glass %v", r.name+":", r.superseded, r.hidden, r.repeats, r.shown, r.meanLatency)
	}
	judderLow := low.superseded + low.hidden + low.repeats
	judderSmooth := smooth.superseded + smooth.hidden + smooth.repeats
	if judderLow < 20 {
		t.Fatalf("lowest latency judders only %d times: the jitter did not reach the deadline", judderLow)
	}
	if judderSmooth*4 > judderLow {
		t.Fatalf("smooth judders %d times, lowest latency %d: want a quarter or less", judderSmooth, judderLow)
	}
	if smooth.meanLatency > low.meanLatency+time.Duration(refresh120) {
		t.Fatalf("smooth mean latency %v, more than a refresh over %v", smooth.meanLatency, low.meanLatency)
	}
}
