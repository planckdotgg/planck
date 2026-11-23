package input

import (
	"errors"
	"math"
	"time"
)

// Injector replays client input on the host. Implementations post real OS
// events; tests must not call them unless PLANCK_LIVE_INPUT_TEST=1.
type Injector interface {
	Inject(ev Event) error
	Close() error
}

// ErrUnsupported is returned for events the host cannot inject, such as
// gamepads with no virtual controller driver or host-bound rumble.
var ErrUnsupported = errors.New("input: unsupported on this host")

// scaleAbs maps a normalized 0..65535 coordinate onto [origin, origin+size).
func scaleAbs(v uint16, origin, size float64) float64 {
	if size <= 1 {
		return origin
	}
	return origin + float64(v)*(size-1)/65535
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// wheelAccum turns WheelNotch-unit deltas into whole lines, carrying the
// remainder so high-resolution scrolling is not lost.
type wheelAccum struct{ rem int32 }

func (w *wheelAccum) lines(delta int16) int32 {
	w.rem += int32(delta)
	n := w.rem / WheelNotch
	w.rem -= n * WheelNotch
	return n
}

// clickTracker counts multi-clicks for hosts that need the count spelled out
// on each event (macOS click state).
type clickTracker struct {
	button Button
	at     time.Time
	x, y   float64
	count  int64
}

const (
	multiClickInterval = 500 * time.Millisecond
	multiClickSlop     = 4.0
)

func (c *clickTracker) press(b Button, now time.Time, x, y float64) int64 {
	if b == c.button && now.Sub(c.at) <= multiClickInterval &&
		math.Abs(x-c.x) <= multiClickSlop && math.Abs(y-c.y) <= multiClickSlop {
		c.count++
	} else {
		c.count = 1
	}
	c.button, c.at, c.x, c.y = b, now, x, y
	return c.count
}
