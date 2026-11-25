//go:build darwin || windows

package input

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/planckdotgg/planck/pkg/gamepad"
)

// Posts real OS input: the cursor moves one pixel right and back. Runs only
// with PLANCK_LIVE_INPUT_TEST=1, never on a machine someone is using.
func TestLiveInjectorNudgesCursor(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_INPUT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_INPUT_TEST=1 to post real input events")
	}
	inj, err := NewInjector()
	if err != nil {
		t.Fatal(err)
	}
	defer inj.Close()
	for _, dx := range []int16{1, -1} {
		if err := inj.Inject(Event{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: dx}}); err != nil {
			t.Fatal(err)
		}
	}
	err = inj.Inject(Event{Type: TypeGamepad})
	if runtime.GOOS == "windows" && gamepad.VirtualPadStatus().Installed {
		if err != nil {
			t.Fatalf("gamepad inject with ViGEmBus = %v", err)
		}
	} else if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gamepad inject = %v, want ErrUnsupported", err)
	}
}
