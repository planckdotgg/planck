//go:build windows

package power

import (
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	user32                      = windows.NewLazySystemDLL("user32.dll")
	procLockWorkStation         = user32.NewProc("LockWorkStation")
)

const (
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
	esContinuous      = 0x80000000
)

// KeepAwake keeps the machine and its display from sleeping on idle until
// release. SetThreadExecutionState holds for the thread that set it, so a
// thread of its own keeps it.
func KeepAwake(string) (release func(), err error) {
	if err := procSetThreadExecutionState.Find(); err != nil {
		return nil, err
	}
	set := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if r, _, e := procSetThreadExecutionState.Call(esContinuous | esSystemRequired | esDisplayRequired); r == 0 {
			set <- fmt.Errorf("power: SetThreadExecutionState: %w", e)
			return
		}
		set <- nil
		<-stop
		procSetThreadExecutionState.Call(esContinuous)
	}()
	if err := <-set; err != nil {
		<-done
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}, nil
}

// LockScreen locks the workstation, as Win+L does.
func LockScreen() error {
	if r, _, e := procLockWorkStation.Call(); r == 0 {
		return fmt.Errorf("power: LockWorkStation: %w", e)
	}
	return nil
}
