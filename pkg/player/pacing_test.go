package player

import (
	"math/rand/v2"
	"testing"
	"time"
)

const refresh120 = int64(time.Second / 120)

func TestVsyncClockLocksOnRefreshTimes(t *testing.T) {
	var c vsyncClock
	at := int64(5_000_000)
	// The first time anchors the clock and the second gives the period.
	for i := 0; i < lockFits+2; i++ {
		if _, _, ok := c.deadline(at); ok {
			t.Fatalf("locked after %d refresh times", i)
		}
		c.observe(int64(i), at)
		// Every third present skips a refresh.
		if i%3 == 2 {
			at += 2 * refresh120
		} else {
			at += refresh120
		}
	}
	start, r, ok := c.deadline(at + 1)
	if !ok {
		t.Fatal("not locked on a steady 120 Hz")
	}
	if d := c.period - refresh120; d > 1000 || d < -1000 {
		t.Fatalf("period %d ns, want %d", c.period, refresh120)
	}
	if off := (r - 5_000_000) % refresh120; off > 1000 && off < refresh120-1000 {
		t.Fatalf("deadline's refresh %d is %d ns off the grid", r, off)
	}
	if r-start != c.lead || start < at+1 {
		t.Fatalf("start %d for refresh %d with lead %d after %d", start, r, c.lead, at+1)
	}
}

func TestVsyncClockStaysUnlockedOnVariableRefresh(t *testing.T) {
	var c vsyncClock
	rng := rand.New(rand.NewPCG(1, 2))
	at := int64(0)
	for i := range 200 {
		at += int64(6*time.Millisecond) + rng.Int64N(int64(8*time.Millisecond))
		c.observe(int64(i), at)
	}
	if _, _, ok := c.deadline(at); ok {
		t.Fatal("locked on irregular screen times")
	}
}

func TestVsyncClockLeadFollowsMisses(t *testing.T) {
	var c vsyncClock
	for i := range lockFits + 2 {
		c.observe(int64(i), int64(i+1)*refresh120)
	}
	lead := c.lead
	_, r, _ := c.deadline(20 * refresh120)
	c.aim(100, r)
	c.observe(100, r+refresh120)
	if c.lead != lead+leadStep {
		t.Fatalf("missed refresh: lead %d, want %d", c.lead, lead+leadStep)
	}
	_, r, _ = c.deadline(r + 2*refresh120)
	c.aim(101, r)
	c.observe(101, r)
	if c.lead != lead+leadStep-leadDrift {
		t.Fatalf("made refresh: lead %d, want %d", c.lead, lead+leadStep-leadDrift)
	}
}
