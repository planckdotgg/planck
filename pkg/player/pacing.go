package player

import (
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/phaselock"
)

// Pacing is when the present thread shows each decoded picture.
type Pacing uint8

const (
	// PaceLowestLatency shows each picture as soon as it is decoded; one
	// that comes within a refresh of the last replaces it unshown.
	PaceLowestLatency Pacing = iota
	// PaceSmooth shows one picture a display refresh, in step with it: a
	// picture that comes within a refresh of the last waits for the next
	// refresh instead of replacing it. While pictures come in pairs that
	// costs up to a refresh of latency, and a backlog that lasts
	// catchUpTicks drops a picture to win it back. Where the refresh times
	// are not known (Windows) or not regular (a variable refresh rate) it
	// is PaceLowestLatency.
	PaceSmooth
)

const (
	// smoothQueue is how many pictures wait for their refresh.
	smoothQueue = 2
	// catchUpTicks of steady backlog drop the oldest waiting picture.
	catchUpTicks = 60
	// lockFits refresh times in a row on the period lock the clock.
	lockFits     = 8
	fitTolerance = int64(500 * time.Microsecond)
	minRefresh   = int64(time.Second / 500)
	maxRefresh   = int64(time.Second / 23)
	// The lead is how long before a refresh a present must start to make
	// it; it moves with the presents that made or missed theirs.
	startLead = int64(3 * time.Millisecond)
	minLead   = int64(time.Millisecond)
	leadStep  = int64(500 * time.Microsecond)
	leadDrift = int64(20 * time.Microsecond)
)

// vsyncClock follows the display's refresh from the times pictures reached
// the screen, which are refresh times. Safe for concurrent use.
type vsyncClock struct {
	mu     sync.Mutex
	period int64
	anchor int64
	fits   int
	lead   int64
	// nominal is the display's refresh period as the system states it, 0
	// where it does not: no refresh comes sooner.
	nominal int64
	// targets are the refreshes the last presents aimed at, by tag.
	targets [8]struct{ tag, at int64 }
}

// setNominal gives the display's refresh period as the system states it.
func (c *vsyncClock) setNominal(ns int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nominal = ns
	if p := c.fold(c.period); c.period > 0 && p != c.period {
		c.period, c.fits = 0, 0
	}
}

// fold is a refresh period measured as d, brought to the display's stated
// one (see phaselock.Harmonic): a 60 fps stream on a 120 Hz display reaches
// it every other refresh, and taking those 16.7 ms for the refresh would
// lock the clock at 60 Hz. 0 for a period shorter than the stated one.
func (c *vsyncClock) fold(d int64) int64 {
	p, ok := phaselock.Harmonic(d, c.nominal, fitTolerance)
	if !ok {
		return 0
	}
	return p
}

// shortest is the shortest time between refreshes. Two pictures a third of
// a refresh apart, as a compositor shows them off its grid, would else make
// the period a third of the refresh, and every later refresh time a whole
// number of those apart would lock the clock on it: a 360 Hz grid on a 120
// Hz display.
func (c *vsyncClock) shortest() int64 {
	return max(minRefresh, c.nominal-fitTolerance)
}

// observe takes the time a picture tagged tag reached the screen.
func (c *vsyncClock) observe(tag, t int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lead == 0 {
		c.lead = startLead
	}
	if tg := c.targets[uint64(tag)%uint64(len(c.targets))]; tg.at != 0 && tg.tag == tag && c.period > 0 {
		switch late := t - tg.at; {
		case late > c.period/2:
			c.lead = min(c.lead+leadStep, c.period-minLead)
		case late < c.period/4:
			c.lead = max(c.lead-leadDrift, minLead)
		}
	}
	if t <= c.anchor {
		return
	}
	if c.anchor == 0 {
		c.anchor = t
		return
	}
	d := t - c.anchor
	c.anchor = t
	lo := c.shortest()
	if c.period == 0 {
		if d >= lo && d <= maxRefresh {
			c.period = c.fold(d)
		}
		return
	}
	k := (d + c.period/2) / c.period
	if k < 1 {
		// Faster than the period: it was a multiple of the refresh.
		if d >= lo {
			c.period = c.fold(d)
		}
		c.fits = 0
		return
	}
	resid := d - k*c.period
	if resid > fitTolerance || resid < -fitTolerance {
		c.fits = 0
		if k == 1 && d >= lo && d <= maxRefresh {
			c.period = c.fold(d)
		}
		return
	}
	c.period += resid / k / 8
	c.fits++
}

// deadline is when to start presenting a picture to make the first
// refresh it can still make after now, and that refresh. ok is false
// until the clock is locked.
func (c *vsyncClock) deadline(now int64) (start, refresh int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fits < lockFits {
		return 0, 0, false
	}
	lead := c.lead
	at := now + lead
	n := (at - c.anchor + c.period - 1) / c.period
	refresh = c.anchor + n*c.period
	return refresh - lead, refresh, true
}

// grid is the refresh period and a refresh time, once the clock is
// locked.
func (c *vsyncClock) grid() (period, anchor int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.period, c.anchor, c.fits >= lockFits
}

// aim records that the picture tagged tag is presented for refresh at.
func (c *vsyncClock) aim(tag, at int64) {
	c.mu.Lock()
	c.targets[uint64(tag)%uint64(len(c.targets))] = struct{ tag, at int64 }{tag, at}
	c.mu.Unlock()
}
