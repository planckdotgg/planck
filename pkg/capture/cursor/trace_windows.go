//go:build windows

package cursor

import "unsafe"

var (
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

// Foreground is the window in front and its process, for input traces.
func Foreground() (hwnd uintptr, pid uint32) {
	hwnd, _, _ = procGetForegroundWindow.Call()
	if hwnd != 0 {
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	}
	return hwnd, pid
}

// DPIAt is the effective DPI of the display at a desktop point; 96 is
// 100%.
func DPIAt(x, y int) int { return dpiAt(x, y) }
