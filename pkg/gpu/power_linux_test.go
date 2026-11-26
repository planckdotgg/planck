//go:build linux

package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestForceLevelRestores(t *testing.T) {
	p := filepath.Join(t.TempDir(), "power_dpm_force_performance_level")
	if err := os.WriteFile(p, []byte("auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := forceLevel(p, "high")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "high" {
		t.Fatalf("level %q while held", b)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "auto" {
		t.Fatalf("level %q after restore", b)
	}
}

func TestNoPolicyForNVIDIA(t *testing.T) {
	g := AcquireHighPerf(Adapter{Vendor: VendorNVIDIA})
	defer g.Close()
	if g.Mode != PowerUnchanged || g.Reason == nil {
		t.Fatalf("mode %v reason %v", g.Mode, g.Reason)
	}
}
