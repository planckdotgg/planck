//go:build linux

package gpu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AcquireHighPerf keeps an AMD GPU at its highest clocks while the
// process runs, through amdgpu's power_dpm_force_performance_level, where
// this user may write it (root, or a udev rule granting it). Elsewhere it
// changes nothing: NVIDIA's Linux driver has no per-application power
// policy (locking clocks with nvidia-smi needs root and holds for every
// process), and Intel's has none to set.
func AcquireHighPerf(adapter Adapter) *PowerGuard {
	g := &PowerGuard{Adapter: adapter, Executable: filepath.Base(os.Args[0])}
	if adapter.Vendor != VendorAMD {
		g.Reason = fmt.Errorf("gpu: no power policy for %s on Linux", adapter.Vendor)
		return g
	}
	path, err := dpmLevelPath("/sys/class/drm", adapter.LUID)
	if err != nil {
		g.Reason = err
		return g
	}
	restore, err := forceLevel(path, "high")
	if err != nil {
		g.Reason = err
		return g
	}
	g.Mode, g.restore = PowerMaxPerformance, restore
	return g
}

// dpmLevelPath is the amdgpu power level file of the render node with
// this device number.
func dpmLevelPath(sysDir string, luid LUID) (string, error) {
	nodes, err := renderNodes("/dev/dri", sysDir)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if n.Adapter.LUID == luid {
			return filepath.Join(sysDir, filepath.Base(n.Path), "device", "power_dpm_force_performance_level"), nil
		}
	}
	return "", errors.New("gpu: no render node for the adapter")
}

// forceLevel writes level to path and returns what puts the old one back.
func forceLevel(path, level string) (func() error, error) {
	old, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	prev := strings.TrimSpace(string(old))
	if prev == level {
		return func() error { return nil }, nil
	}
	if err := os.WriteFile(path, []byte(level), 0); err != nil {
		return nil, fmt.Errorf("gpu: set %s: %w", path, err)
	}
	return func() error { return os.WriteFile(path, []byte(prev), 0) }, nil
}
