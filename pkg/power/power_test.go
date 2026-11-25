package power

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestKeepAwakeLive takes and lets go of the assertion; it never locks
// the machine.
func TestKeepAwakeLive(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("no backend")
	}
	const reason = "Planck power test"
	release, err := KeepAwake(reason)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
		if err == nil && !strings.Contains(string(out), reason) {
			t.Errorf("pmset does not list %q:\n%s", reason, out)
		}
	}
	release()
	release()
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
		if err == nil && strings.Contains(string(out), reason) {
			t.Errorf("pmset still lists %q after release", reason)
		}
	}
}
