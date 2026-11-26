package player

import (
	"sync"
	"time"
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
	// targets are the refreshes the last presents aimed at, by tag.
	targets [8]struct{ tag, at int64 }
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
	if c.period == 0 {
		if d >= minRefresh && d <= maxRefresh {
			c.period = d
		}
		return
	}
	k := (d + c.period/2) / c.period
	if k < 1 {
		// Faster than the period: it was a multiple of the refresh.
		if d >= minRefresh {
			c.period = d
		}
		c.fits = 0
		return
	}
	resid := d - k*c.period
	if resid > fitTolerance || resid < -fitTolerance {
		c.fits = 0
		if k == 1 && d >= minRefresh && d <= maxRefresh {
			c.period = d
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

// aim records that the picture tagged tag is presented for refresh at.
func (c *vsyncClock) aim(tag, at int64) {
	c.mu.Lock()
	c.targets[uint64(tag)%uint64(len(c.targets))] = struct{ tag, at int64 }{tag, at}
	c.mu.Unlock()
}
