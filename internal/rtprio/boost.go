package rtprio

import (
	"errors"
	"fmt"
)

const (
	// fifoPriority is modest: enough to preempt ordinary threads, far below
	// the kernel's own real-time work.
	fifoPriority = 10
	niceLevel    = -15
)

// boostOps are the ways to raise a thread's class, so that the order they
// are tried in can be tested.
type boostOps struct {
	fifo      func(tid, prio int) error
	nice      func(tid, nice int) error
	rtkitNice func(tid, nice int) error
}

// boostThread raises thread tid by the strongest means that is allowed: a
// SCHED_FIFO class, else a lower nice value, else the same through
// RealtimeKit, which grants it to unprivileged desktop users. It returns
// the means used.
func boostThread(tid int, ops boostOps) (string, error) {
	var errs []error
	try := func(name string, f func() error) bool {
		if f == nil {
			return false
		}
		if err := f(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			return false
		}
		return true
	}
	switch {
	case try("SCHED_FIFO", wrap(ops.fifo, tid, fifoPriority)):
		return "SCHED_FIFO", nil
	case try("setpriority", wrap(ops.nice, tid, niceLevel)):
		return "nice", nil
	case try("RealtimeKit", wrap(ops.rtkitNice, tid, niceLevel)):
		return "RealtimeKit", nil
	}
	return "", errors.Join(errs...)
}

func wrap(f func(tid, v int) error, tid, v int) func() error {
	if f == nil {
		return nil
	}
	return func() error { return f(tid, v) }
}
