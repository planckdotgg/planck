//go:build windows

package vdd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Live tests add real monitors to this machine's desktop. Run them with
// PLANCK_LIVE_VDD_TEST=1 [PLANCK_LIVE_VDD_GPU=4080]; Apollo must not run.

func liveDriver(t *testing.T) (*Driver, gpu.Adapter) {
	t.Helper()
	if os.Getenv("PLANCK_LIVE_VDD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_VDD_TEST=1 to add a real SudoVDA display")
	}
	name := os.Getenv("PLANCK_LIVE_VDD_GPU")
	if name == "" {
		name = "4080"
	}
	a, err := dxgi.FindAdapter(name)
	if err != nil {
		t.Skip(err)
	}
	drv, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { drv.Close() })
	return drv, a
}

func onDesktop(t *testing.T, idd gpu.LUID, target uint32) bool {
	t.Helper()
	ms, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.IddAdapter == idd && m.TargetID == target {
			return true
		}
	}
	return false
}

func waitGone(t *testing.T, idd gpu.LUID, target uint32, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for onDesktop(t, idd, target) {
		if time.Since(start) > limit {
			t.Fatalf("target %d still on the desktop after %v", target, limit)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Since(start)
}

func TestLiveCreate(t *testing.T) {
	drv, adapter := liveDriver(t)
	spec := DisplaySpec{Width: 1920, Height: 1080, RefreshMilliHz: 120000,
		ID: MonitorID([]byte("planck-live-create")), RenderAdapter: adapter.LUID}
	start := time.Now()
	v, err := drv.CreateDisplay(spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s target %d on %s in %v", v.GDIName, v.TargetID, adapter.Name, time.Since(start))
	if v.RenderAdapter != adapter.LUID {
		t.Fatalf("render adapter 0x%x, want %s 0x%x", uint64(v.RenderAdapter), adapter.Name, uint64(adapter.LUID))
	}
	if !onDesktop(t, v.IddAdapter, v.TargetID) {
		t.Fatal("Monitors does not list the new display")
	}

	frames := captureAnimated(t, v)
	if frames < 20 {
		t.Fatalf("only %d frames from %s", frames, v.GDIName)
	}
	t.Logf("%d frames from %s", frames, v.GDIName)

	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	t.Logf("gone %v after Close", waitGone(t, v.IddAdapter, v.TargetID, 4*time.Second))
	outs, err := dxgi.Displays()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if o.ID == v.GDIName {
			t.Fatalf("DXGI still lists %s", v.GDIName)
		}
	}
}

// captureAnimated duplicates v while a small window changes color on it.
func captureAnimated(t *testing.T, v *VirtualDisplay) int {
	t.Helper()
	dm, err := currentMode(v.GDIName)
	if err != nil {
		t.Fatal(err)
	}
	x, y := *(*int32)(unsafe.Pointer(&dm[76])), *(*int32)(unsafe.Pointer(&dm[80])) // dmPosition
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); win.Animate(x+100, y+100, 96, stop) }()
	defer func() { close(stop); <-done }()

	src, err := dxgi.OpenDisplay(v.GDIName, dxgi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.Adapter().LUID != v.RenderAdapter {
		t.Fatalf("duplication on 0x%x, display renders on 0x%x", uint64(src.Adapter().LUID), uint64(v.RenderAdapter))
	}
	frames := 0
	for range 60 {
		_, err := src.Next(100 * time.Millisecond)
		switch {
		case errors.Is(err, capture.ErrTimeout):
		case errors.Is(err, capture.ErrAccessLost):
			if err := src.Recover(2*time.Second, 50*time.Millisecond); err != nil {
				t.Fatal(err)
			}
		case err != nil:
			t.Fatal(err)
		default:
			frames++
			src.Release()
		}
	}
	return frames
}

func TestLiveIdempotentCreate(t *testing.T) {
	drv, adapter := liveDriver(t)
	spec := DisplaySpec{Width: 1280, Height: 720, RefreshMilliHz: 60000,
		ID: MonitorID([]byte("planck-live-idempotent")), RenderAdapter: adapter.LUID}
	a, err := drv.CreateDisplay(spec)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	b, err := drv.CreateDisplay(spec)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	if a.TargetID != b.TargetID || a.IddAdapter != b.IddAdapter || a.GDIName != b.GDIName {
		t.Fatalf("second create: %+v, first %+v", b, a)
	}
	if len(after) != len(before) {
		t.Fatalf("%d SudoVDA monitors after the second create, %d before", len(after), len(before))
	}
	if err := drv.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, a.IddAdapter, a.TargetID, 4*time.Second)
}

// TestLiveCrash runs a child that adds a display and exits without Close;
// the driver's watchdog must remove it within 4 s.
func TestLiveCrash(t *testing.T) {
	if os.Getenv("PLANCK_VDD_CRASH_CHILD") == "1" {
		drv, err := Open()
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		v, err := drv.CreateDisplay(DisplaySpec{Width: 1280, Height: 720, RefreshMilliHz: 60000,
			ID: MonitorID([]byte("planck-live-crash"))})
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(1)
		}
		fmt.Printf("READY %d %d\n", uint64(v.IddAdapter), v.TargetID)
		os.Exit(3)
	}
	liveDriver(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestLiveCrash$")
	cmd.Env = append(os.Environ(), "PLANCK_VDD_CRASH_CHILD=1")
	out, err := cmd.Output()
	exited := time.Now()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var idd uint64
	var target uint32
	if _, err := fmt.Sscanf(string(out), "READY %d %d", &idd, &target); err != nil {
		t.Fatalf("child output %q: %v", out, err)
	}
	waitGone(t, gpu.LUID(idd), target, 4*time.Second)
	t.Logf("watchdog removed target %d %v after the child exited", target, time.Since(exited))
}

// TestLiveResize resizes a display to a mode it offers, to sizes it does
// not, and through a burst: each time Windows shows the new mode on one
// monitor, and after Close no SudoVDA monitor is left.
func TestLiveResize(t *testing.T) {
	drv, adapter := liveDriver(t)
	before, err := Monitors()
	if err != nil {
		t.Fatal(err)
	}
	v, err := drv.CreateDisplay(DisplaySpec{Width: 1280, Height: 720, RefreshMilliHz: 60000,
		ID: MonitorID([]byte("planck-live-resize")), RenderAdapter: adapter.LUID})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range [][2]int{{1600, 900}, {1734, 968}, {2532, 1418}, {1920, 1080}, {2048, 1152}, {1280, 802}, {2560, 1600}} {
		start := time.Now()
		if err := v.Resize(s[0], s[1]); err != nil {
			t.Fatalf("resize to %dx%d: %v", s[0], s[1], err)
		}
		dm, err := currentMode(v.GDIName)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := *dm.u32(dmPelsWidthOff), *dm.u32(dmPelsHeightOff); int(w) != s[0] || int(h) != s[1] || v.RenderAdapter != adapter.LUID {
			t.Fatalf("%s is %dx%d on 0x%x after resize to %dx%d", v.GDIName, w, h, uint64(v.RenderAdapter), s[0], s[1])
		}
		ms, err := Monitors()
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != len(before)+1 {
			t.Fatalf("%d SudoVDA monitors after resize to %dx%d, %d before the test", len(ms), s[0], s[1], len(before))
		}
		t.Logf("%dx%d on %s in %v", s[0], s[1], v.GDIName, time.Since(start))
	}
	if n := captureAnimated(t, v); n < 20 {
		t.Fatalf("only %d frames after the resizes", n)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, v.IddAdapter, v.TargetID, 4*time.Second)
	if after, err := Monitors(); err != nil || len(after) != len(before) {
		t.Fatalf("%d SudoVDA monitors after Close (%v), %d before", len(after), err, len(before))
	}
}
