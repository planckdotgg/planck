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

// InjectMarker tags the keys a host injects (dwExtraInfo on Windows, the
// event source's user data on macOS), so that a client capturing the
// system's keys on the same machine leaves them alone rather than sending
// them back to the host.
const InjectMarker = 0x504C4E4B // "PLNK"

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

// Rect is a display's place on the host's desktop, in physical pixels.
type Rect struct{ X, Y, W, H int }

// Targeter is an injector whose absolute moves can cover one display of a
// desktop of several: display within desktop, the bounds of every display
// together. A zero display covers the primary display.
type Targeter interface {
	SetTarget(display, desktop Rect)
}

// virtualDeskAbs maps v, normalized 0..65535 across a display of size
// pixels at origin, to the normalized coordinate across a desktop of
// dsize pixels at dorigin that lands on the same pixel. It aims at the
// pixel's centre, so that Windows lands there whether it truncates or
// rounds the coordinate back to a pixel.
func virtualDeskAbs(v uint16, origin, size, dorigin, dsize int) int32 {
	if size <= 0 || dsize <= 0 {
		return int32(v)
	}
	px := math.Round(scaleAbs(v, float64(origin), float64(size)))
	n := (px - float64(dorigin) + 0.5) * 65536 / float64(dsize)
	return int32(clamp(math.Floor(n), 0, 65535))
}

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
