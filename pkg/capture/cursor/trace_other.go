//go:build !windows

package cursor

// Foreground is the window in front and its process, for input traces;
// zero where the platform does not say.
func Foreground() (hwnd uintptr, pid uint32) { return 0, 0 }

// DPIAt is the effective DPI of the display at a desktop point, 0 where
// the platform does not say.
func DPIAt(x, y int) int { return 0 }
