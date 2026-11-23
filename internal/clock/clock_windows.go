//go:build windows

// Package clock reads the platform's high resolution monotonic counter, the
// same clock DXGI and ScreenCaptureKit stamp frames with.
package clock

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")
	freq     int64
)

var qpc uintptr

func init() {
	procQPF.Call(uintptr(unsafe.Pointer(&freq)))
	qpc = procQPC.Addr()
}

// Ticks is the raw QueryPerformanceCounter value.
func Ticks() int64 {
	var t int64
	syscall.SyscallN(qpc, uintptr(unsafe.Pointer(&t)))
	return t
}

func ToDuration(ticks int64) time.Duration {
	return time.Duration(ticks/freq*int64(time.Second) + ticks%freq*int64(time.Second)/freq)
}

func Now() time.Duration { return ToDuration(Ticks()) }

// Ms is the time in milliseconds between two Now values.
func Ms(from, to time.Duration) float64 { return float64(to-from) / 1e6 }
