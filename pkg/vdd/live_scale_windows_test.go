//go:build windows

package vdd

import (
	"fmt"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procEnumDisplayMonitors          = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW              = user32.NewProc("GetMonitorInfoW")
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
)

// monitorInfoEx is MONITORINFOEXW, 104 bytes.
type monitorInfoEx struct {
	Size    uint32
	Monitor windows.Rect
	Work    windows.Rect
	Flags   uint32
	Device  [32]uint16
}

// unawareBounds is the size of the display named gdi as a DPI-unaware
// process sees it, as many games in a borderless window do: at 200% scale
// a 5120x1440 display reads 2560x720.
func unawareBounds(t *testing.T, gdi string) (int, int) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const unaware = ^uintptr(0) // DPI_AWARENESS_CONTEXT_UNAWARE (-1)
	old, _, _ := procSetThreadDpiAwarenessContext.Call(unaware)
	if old == 0 {
		t.Fatal("SetThreadDpiAwarenessContext failed")
	}
	defer procSetThreadDpiAwarenessContext.Call(old)
	var w, h int
	cb := syscall.NewCallback(func(mon, _, _, _ uintptr) uintptr {
		mi := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 && windows.UTF16ToString(mi.Device[:]) == gdi {
			w, h = int(mi.Monitor.Right-mi.Monitor.Left), int(mi.Monitor.Bottom-mi.Monitor.Top)
		}
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if w == 0 {
		t.Fatalf("no monitor named %s", gdi)
	}
	return w, h
}

// displayModes lists the modes a display offers, as WxH@Hz.
func displayModes(gdi string) []string {
	n, _ := windows.UTF16PtrFromString(gdi)
	var out []string
	for i := uintptr(0); ; i++ {
		dm := new(devmode)
		*(*uint16)(unsafe.Pointer(&dm[68])) = devmodeSize
		if r, _, _ := syscall.SyscallN(procEnumDisplaySettingsW.Addr(), uintptr(unsafe.Pointer(n)), i, uintptr(unsafe.Pointer(dm))); r == 0 {
			return out
		}
		out = append(out, fmt.Sprintf("%dx%d@%d", *dm.u32(dmPelsWidthOff), *dm.u32(dmPelsHeightOff), *dm.u32(dmFrequencyOff)))
	}
}

func scaleOf(t *testing.T, v *VirtualDisplay) int {
	t.Helper()
	c, err := queryActive()
	if err != nil {
		t.Fatal(err)
	}
	p := c.findPath(v.IddAdapter, v.TargetID)
	if p == nil {
		t.Fatalf("%s is not on the desktop", v.GDIName)
	}
	pct, err := scale(p)
	if err != nil {
		t.Fatal(err)
	}
	return pct
}

// TestLiveScale: a 5120x1440 120 Hz display asked for at 100% is unscaled,
// so a DPI-unaware game sees all of it, and stays so through a resize;
// closing it leaves no SudoVDA monitor behind.
func TestLiveScale(t *testing.T) {
	drv, adapter := liveDriver(t)
	before, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SudoVDA monitors before: %+v", before)
	spec := DisplaySpec{Width: 5120, Height: 1440, RefreshMilliHz: 120000,
		ID: MonitorID([]byte("planck-live-scale")), RenderAdapter: adapter.LUID, ScalePct: 100}
	start := time.Now()
	v, err := drv.CreateDisplay(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s target %d on %s in %v; scale %d%% (err %v)", v.GDIName, v.TargetID, adapter.Name, time.Since(start), v.ScalePct, v.ScaleErr)
	t.Logf("modes of %s: %v", v.GDIName, displayModes(v.GDIName))
	check := func(w, h int) {
		t.Helper()
		if v.ScaleErr != nil || v.ScalePct != 100 {
			t.Fatalf("%dx%d: scale %d%%, err %v; want 100%%", w, h, v.ScalePct, v.ScaleErr)
		}
		if pct := scaleOf(t, v); pct != 100 {
			t.Fatalf("%dx%d: Windows reads %d%% scale, want 100%%", w, h, pct)
		}
		if lw, lh := unawareBounds(t, v.GDIName); lw != w || lh != h {
			t.Fatalf("a DPI-unaware process sees %s as %dx%d, want %dx%d", v.GDIName, lw, lh, w, h)
		}
	}
	check(5120, 1440)
	if err := v.Resize(3440, 1440); err != nil {
		t.Fatal(err)
	}
	t.Logf("resized: %s target %d", v.GDIName, v.TargetID)
	check(3440, 1440)
	idd, target := v.IddAdapter, v.TargetID
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("removed in %v", waitGone(t, idd, target, 5*time.Second))
	after, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("SudoVDA monitors after: %+v, before: %+v", after, before)
	}
}
