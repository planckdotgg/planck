package input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

func openEventNode(t *testing.T, name string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		names, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, n := range names {
			if b, err := os.ReadFile(n); err == nil && strings.TrimSpace(string(b)) == name {
				fd, err := unix.Open("/dev/input/"+filepath.Base(filepath.Dir(filepath.Dir(n))), unix.O_RDONLY|unix.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { unix.Close(fd) })
				return fd
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no input node for %q", name)
	return -1
}

// readEvents collects events of the given type until n arrive.
func readEvents(t *testing.T, fd int, typ uint16, n int) []evdev.Event {
	t.Helper()
	var got []evdev.Event
	buf := make([]byte, evdev.EventSize*32)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && len(got) < n; time.Sleep(10 * time.Millisecond) {
		r, err := unix.Read(fd, buf)
		if err != nil {
			continue
		}
		for off := 0; off+evdev.EventSize <= r; off += evdev.EventSize {
			if ev := evdev.ParseEvent(buf[off:]); ev.Type == typ {
				got = append(got, ev)
			}
		}
	}
	return got
}

// TestLiveUinputInjector injects each kind of event through /dev/uinput
// and reads it back from the virtual devices' event nodes.
func TestLiveUinputInjector(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_INPUT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_INPUT_TEST=1 to inject through /dev/uinput")
	}
	t.Setenv("PLANCK_INJECT_BACKEND", BackendUinput)
	inj, err := NewInjector()
	if err != nil {
		t.Skip(err)
	}
	defer inj.Close()
	if inj.Backend() != BackendUinput {
		t.Fatalf("backend %s", inj.Backend())
	}
	kbd := openEventNode(t, "Planck Virtual Keyboard")
	mouse := openEventNode(t, "Planck Virtual Mouse")
	ptr := openEventNode(t, "Planck Virtual Pointer")

	for _, down := range []bool{true, false} {
		if err := inj.Inject(Event{Type: TypeKey, Key: KeyEvent{Usage: KeyA, Pressed: down}}); err != nil {
			t.Fatal(err)
		}
	}
	keys := readEvents(t, kbd, evdev.EvKey, 2)
	if len(keys) != 2 || keys[0] != (evdev.Event{Type: evdev.EvKey, Code: 30, Value: 1}) || keys[1].Value != 0 {
		t.Errorf("keys %+v", keys)
	}

	if err := inj.Inject(Event{Type: TypeMouseMoveRel, MouseRel: MouseMoveRel{DX: 7, DY: -3}}); err != nil {
		t.Fatal(err)
	}
	if err := inj.Inject(Event{Type: TypeMouseWheel, Wheel: MouseWheel{DY: WheelNotch}}); err != nil {
		t.Fatal(err)
	}
	if err := inj.Inject(Event{Type: TypeMouseButton, Button: MouseButton{Button: ButtonRight, Pressed: true}}); err != nil {
		t.Fatal(err)
	}
	rel := readEvents(t, mouse, evdev.EvRel, 4)
	seen := map[uint16]int32{}
	for _, e := range rel {
		seen[e.Code] = e.Value
	}
	if seen[evdev.RelX] != 7 || seen[evdev.RelY] != -3 || seen[evdev.RelWheelHiRes] != WheelNotch || seen[evdev.RelWheel] != 1 {
		t.Errorf("rel %+v", rel)
	}

	inj.SetTarget(Rect{X: 1920, W: 1920, H: 1080}, Rect{W: 3840, H: 1080})
	if err := inj.Inject(Event{Type: TypeMouseMoveAbs, MouseAbs: MouseMoveAbs{X: 65535, Y: 0}}); err != nil {
		t.Fatal(err)
	}
	abs := readEvents(t, ptr, evdev.EvAbs, 2)
	if len(abs) != 2 || abs[0].Code != evdev.AbsX || abs[0].Value < 65500 || abs[1].Value > 40 {
		t.Errorf("abs %+v", abs)
	}
}
