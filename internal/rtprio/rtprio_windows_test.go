package rtprio

import (
	"syscall"
	"testing"
	"unsafe"
)

// TestBoostUnthrottlesProcess: a media thread's Boost takes the process
// out of EcoQoS execution-speed throttling.
func TestBoostUnthrottlesProcess(t *testing.T) {
	errc := make(chan error, 1)
	go func() { errc <- Boost() }()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	k := syscall.NewLazyDLL("kernel32.dll")
	proc, _, _ := k.NewProc("GetCurrentProcess").Call()
	st := [3]uint32{1, 0, 0} // PROCESS_POWER_THROTTLING_STATE
	if ok, _, err := k.NewProc("GetProcessInformation").Call(proc, 4, uintptr(unsafe.Pointer(&st)), 12); ok == 0 {
		t.Fatal(err)
	}
	if st[1]&1 == 0 || st[2]&1 != 0 {
		t.Fatalf("throttling control %#x state %#x: execution speed still throttled", st[1], st[2])
	}
}
