package rtprio

import (
	"fmt"
	"unsafe"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// boost is best effort: a session that may neither set a scheduling class
// nor reach RealtimeKit is the normal state of an unprivileged desktop, and
// the thread simply stays at its own class.
func boost() error {
	_, _ = boostThread(unix.Gettid(), boostOps{fifo: setFIFO, nice: setNice, rtkitNice: rtkitHighPriority})
	return nil
}

func setFIFO(tid, prio int) error {
	attr := unix.SchedAttr{Size: uint32(unsafe.Sizeof(unix.SchedAttr{})), Policy: unix.SCHED_FIFO, Priority: uint32(prio)}
	return unix.SchedSetAttr(tid, &attr, 0)
}

func setNice(tid, nice int) error { return unix.Setpriority(unix.PRIO_PROCESS, tid, nice) }

// rtkitHighPriority asks RealtimeKit, on the system bus, to lower the
// thread's nice value; unlike its real-time call it sets no CPU-time limit
// that would kill a busy encoder thread.
func rtkitHighPriority(tid, nice int) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	obj := conn.Object("org.freedesktop.RealtimeKit1", "/org/freedesktop/RealtimeKit1")
	call := obj.Call("org.freedesktop.RealtimeKit1.MakeThreadHighPriority", 0, uint64(tid), int32(nice))
	if call.Err != nil {
		return fmt.Errorf("MakeThreadHighPriority: %w", call.Err)
	}
	return nil
}
