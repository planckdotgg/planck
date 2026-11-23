package transport

import (
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// Windows time.Sleep has a floor of about 0.5 ms and overshoots by up to
// another millisecond, so spin for the final 2 ms.
const spinThreshold = 2 * time.Millisecond

var (
	procQPC  = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	qpcFreq  int64
	qpcEpoch int64
)

func init() {
	freq := syscall.NewLazyDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")
	syscall.SyscallN(freq.Addr(), uintptr(unsafe.Pointer(&qpcFreq)))
	qpcEpoch = qpc()
}

func qpc() int64 {
	var c int64
	syscall.SyscallN(procQPC.Addr(), uintptr(unsafe.Pointer(&c)))
	return c
}

func mono() int64 {
	d := qpc() - qpcEpoch
	return d/qpcFreq*1e9 + d%qpcFreq*1e9/qpcFreq
}

func sleep(ns int64) { time.Sleep(time.Duration(ns)) }
func gosched()       { runtime.Gosched() }
