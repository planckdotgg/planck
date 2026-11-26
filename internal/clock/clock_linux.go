//go:build linux

package clock

import (
	"time"

	"golang.org/x/sys/unix"
)

// Ticks is CLOCK_MONOTONIC in nanoseconds.
func Ticks() int64 {
	var ts unix.Timespec
	unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return ts.Nano()
}

func ToDuration(ticks int64) time.Duration { return time.Duration(ticks) }

func Now() time.Duration { return ToDuration(Ticks()) }

// Ms is the time in milliseconds between two Now values.
func Ms(from, to time.Duration) float64 { return float64(to-from) / 1e6 }
