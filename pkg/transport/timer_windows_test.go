//go:build windows

package transport

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	k32                  = syscall.NewLazyDLL("kernel32.dll")
	procCreateTimerEx    = k32.NewProc("CreateWaitableTimerExW")
	procSetTimer         = k32.NewProc("SetWaitableTimer")
	procWaitSingle       = k32.NewProc("WaitForSingleObject")
	procGetThreadTimes   = k32.NewProc("GetThreadTimes")
	procGetCurrentThread = k32.NewProc("GetCurrentThread")
	createTimerHighRes   = uintptr(0x2)
	timerAllAccess       = uintptr(0x1F0003)
	waitInfinite         = ^uintptr(0)
)

// hiResTimer is a CREATE_WAITABLE_TIMER_HIGH_RESOLUTION timer (Windows 10
// 1803+): about 0.5 ms granularity without raising the global timer period.
type hiResTimer struct{ h uintptr }

func newHiResTimer(t testing.TB) *hiResTimer {
	h, _, err := procCreateTimerEx.Call(0, 0, createTimerHighRes, timerAllAccess)
	if h == 0 {
		t.Skipf("high resolution waitable timer: %v", err)
	}
	return &hiResTimer{h: h}
}

func (w *hiResTimer) sleep(ns int64) {
	due := new(int64)
	*due = -(ns / 100) // relative, 100 ns units
	procSetTimer.Call(w.h, uintptr(unsafe.Pointer(due)), 0, 0, 0, 0)
	procWaitSingle.Call(w.h, waitInfinite)
}

// threadCPU is the calling thread's kernel+user time in ns.
func threadCPU() int64 {
	th, _, _ := procGetCurrentThread.Call()
	var c, e, k, u syscall.Filetime
	procGetThreadTimes.Call(th, uintptr(unsafe.Pointer(&c)), uintptr(unsafe.Pointer(&e)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	return (int64(k.HighDateTime)<<32 | int64(k.LowDateTime) + int64(u.HighDateTime)<<32 | int64(u.LowDateTime)) * 100
}

// TestTimerJitterReport compares ways to wait for a deadline: overshoot
// percentiles and the CPU the waiting thread burns. Run with
// PLANCK_TIMER_REPORT=1 go test -run TimerJitterReport -v ./pkg/transport
func TestTimerJitterReport(t *testing.T) {
	if os.Getenv("PLANCK_TIMER_REPORT") == "" {
		t.Skip("set PLANCK_TIMER_REPORT=1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr := newHiResTimer(t)
	hybrid := func(spin time.Duration) func(int64) {
		return func(deadline int64) {
			for {
				d := deadline - Mono()
				if d <= 0 {
					return
				}
				if d > int64(spin) {
					hr.sleep(d - int64(spin))
					continue
				}
				runtime.Gosched()
			}
		}
	}
	methods := []struct {
		name string
		wait func(deadline int64)
	}{
		{"time.Sleep", func(dl int64) { time.Sleep(time.Duration(dl - Mono())) }},
		{"SleepUntilMono (2 ms spin)", SleepUntilMono},
		{"hi-res timer only", func(dl int64) {
			if d := dl - Mono(); d > 0 {
				hr.sleep(d)
			}
		}},
		{"hi-res timer + 300 us spin", hybrid(300 * time.Microsecond)},
		{"hi-res timer + 100 us spin", hybrid(100 * time.Microsecond)},
	}
	for _, target := range []time.Duration{100 * time.Microsecond, 500 * time.Microsecond, 1 * time.Millisecond, 4166 * time.Microsecond} {
		for _, m := range methods {
			const n = 400
			over := make([]float64, 0, n)
			cpu0, wall0 := threadCPU(), Mono()
			for i := 0; i < n; i++ {
				dl := Mono() + int64(target)
				m.wait(dl)
				over = append(over, float64(Mono()-dl)/1e3)
			}
			cpu, wall := threadCPU()-cpu0, Mono()-wall0
			slices.Sort(over)
			at := func(p float64) float64 { return over[min(n-1, int(p*n))] }
			fmt.Printf("target %7v  %-28s overshoot us p50 %7.1f p95 %7.1f p99 %7.1f max %7.1f  cpu %3.0f%%\n",
				target, m.name, at(.5), at(.95), at(.99), over[n-1], 100*float64(cpu)/float64(wall))
		}
	}
}

func BenchmarkMono(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Mono()
	}
}
