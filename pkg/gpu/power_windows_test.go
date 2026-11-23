//go:build windows

package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func nvidia(t *testing.T) Adapter {
	if err := loadNVAPI(); err != nil {
		t.Skip(err)
	}
	return Adapter{Vendor: VendorNVIDIA, Name: "NVIDIA"}
}

func TestOtherVendorsNeedNothing(t *testing.T) {
	g := AcquireHighPerf(Adapter{Vendor: VendorAMD})
	if g.Mode != PowerNotNeeded || g.Close() != nil {
		t.Fatalf("AMD: %+v", g)
	}
}

func TestCloseRestores(t *testing.T) {
	g := AcquireHighPerf(nvidia(t))
	if g.Mode != PowerMaxPerformance {
		t.Fatalf("not applied: %v", g.Reason)
	}
	if v, set, err := ReadPState(g.Executable); err != nil || !set || v != preferredPStatePrefMax {
		t.Fatalf("while held: set=%v value=%d err=%v", set, v, err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, set, err := ReadPState(g.Executable); err != nil || set {
		t.Fatalf("after Close: set=%v err=%v", set, err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// A run that crashed left its executable in the Planck profile; the next
// run must still clean it up on Close.
func TestRecoversFromCrashedRun(t *testing.T) {
	a := nvidia(t)
	exe, _ := os.Executable()
	if _, err := applyPolicy(filepath.Base(exe)); err != nil {
		t.Fatal(err)
	}
	g := AcquireHighPerf(a)
	if g.Mode != PowerMaxPerformance {
		t.Fatalf("not applied: %v", g.Reason)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, set, err := ReadPState(g.Executable); err != nil || set {
		t.Fatalf("the crashed run's policy survived Close: set=%v err=%v", set, err)
	}
}
