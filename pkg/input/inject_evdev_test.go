package input

import (
	"errors"
	"slices"
	"testing"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

func TestKeyEvents(t *testing.T) {
	evs, err := keyEvents(KeyEvent{Usage: KeyA, Pressed: true})
	if err != nil || len(evs) != 1 || evs[0] != (evdev.Event{Type: evdev.EvKey, Code: 30, Value: 1}) {
		t.Fatalf("A down = %+v, %v", evs, err)
	}
	evs, _ = keyEvents(KeyEvent{Usage: KeyLeftGUI})
	if evs[0] != (evdev.Event{Type: evdev.EvKey, Code: 125, Value: 0}) {
		t.Errorf("Left GUI up = %+v", evs[0])
	}
	if _, err := keyEvents(KeyEvent{Usage: KeyNonUSHash, Pressed: true}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unmapped usage: %v", err)
	}
}

func TestRelAndButtonEvents(t *testing.T) {
	if got := relEvents(MouseMoveRel{DX: -3, DY: 0}); len(got) != 1 || got[0] != (evdev.Event{Type: evdev.EvRel, Code: evdev.RelX, Value: -3}) {
		t.Errorf("rel = %+v", got)
	}
	if got := relEvents(MouseMoveRel{}); len(got) != 0 {
		t.Errorf("zero move emitted %+v", got)
	}
	for b, want := range map[Button]uint16{ButtonLeft: 0x110, ButtonRight: 0x111, ButtonMiddle: 0x112, ButtonX1: 0x113, ButtonX2: 0x114} {
		evs, err := buttonEvents(MouseButton{Button: b, Pressed: true})
		if err != nil || evs[0].Code != want || evs[0].Value != 1 {
			t.Errorf("button %d = %+v, %v; want code 0x%x", b, evs, err, want)
		}
	}
	if _, err := buttonEvents(MouseButton{Button: 9}); err == nil {
		t.Error("button 9 accepted")
	}
}

func TestWheelEvents(t *testing.T) {
	var ax, ay wheelAccum
	got := wheelEvents(MouseWheel{DY: 60}, &ax, &ay)
	if len(got) != 1 || got[0] != (evdev.Event{Type: evdev.EvRel, Code: evdev.RelWheelHiRes, Value: 60}) {
		t.Fatalf("half detent = %+v", got)
	}
	got = wheelEvents(MouseWheel{DY: 60, DX: -WheelNotch}, &ax, &ay)
	want := []evdev.Event{
		{Type: evdev.EvRel, Code: evdev.RelWheelHiRes, Value: 60},
		{Type: evdev.EvRel, Code: evdev.RelWheel, Value: 1},
		{Type: evdev.EvRel, Code: evdev.RelHWheelHiRes, Value: -WheelNotch},
		{Type: evdev.EvRel, Code: evdev.RelHWheel, Value: -1},
	}
	if !slices.Equal(got, want) {
		t.Errorf("second half detent = %+v, want %+v", got, want)
	}
}

func TestAbsTarget(t *testing.T) {
	if x, y := absTarget(MouseMoveAbs{X: 100, Y: 200}, Rect{}, Rect{}); x != 100 || y != 200 {
		t.Errorf("no target = %d,%d", x, y)
	}
	desktop := Rect{X: 0, Y: 0, W: 3840, H: 1080}
	right := Rect{X: 1920, Y: 0, W: 1920, H: 1080}
	if x, _ := absTarget(MouseMoveAbs{X: 0}, right, desktop); x < 32767 || x > 32800 {
		t.Errorf("left edge of the right display = %d, want just past the middle", x)
	}
	if x, y := absTarget(MouseMoveAbs{X: 65535, Y: 65535}, right, desktop); x < 65500 || y < 65500 {
		t.Errorf("bottom-right = %d,%d, want the last pixel's centre", x, y)
	}
}

func TestDeviceConfigs(t *testing.T) {
	kbd := keyboardConfig()
	if len(kbd.Keys) != len(evdevKeys) || !slices.IsSorted(kbd.Keys) || !slices.Contains(kbd.Keys, 30) {
		t.Errorf("keyboard keys: %d", len(kbd.Keys))
	}
	m := mouseConfig()
	for _, r := range []uint16{evdev.RelX, evdev.RelY, evdev.RelWheel, evdev.RelWheelHiRes, evdev.RelHWheel, evdev.RelHWheelHiRes} {
		if !slices.Contains(m.Rels, r) {
			t.Errorf("mouse lacks REL 0x%x", r)
		}
	}
	p := pointerConfig()
	if len(p.Abs) != 2 || p.Abs[0].Max != absRange || len(p.Rels) != 0 {
		t.Errorf("pointer = %+v", p)
	}
	if !slices.Contains(p.Keys, evdev.BtnLeft) {
		t.Error("the pointer needs BTN_LEFT so udev tags it as a mouse, not a tablet")
	}
}

func TestBackendOrder(t *testing.T) {
	cases := []struct {
		name string
		env  injectEnv
		want []string
	}{
		{"x11", injectEnv{display: ":0", sessionType: "x11"}, []string{"uinput", "xtest"}},
		{"x11 without session type", injectEnv{display: ":0"}, []string{"uinput", "xtest"}},
		{"wayland with xwayland", injectEnv{display: ":0", wayland: "wayland-0"}, []string{"uinput", "portal"}},
		{"wayland by session type", injectEnv{sessionType: "wayland"}, []string{"uinput", "portal"}},
		{"console", injectEnv{}, []string{"uinput"}},
		{"forced", injectEnv{forced: "xtest", wayland: "wayland-0"}, []string{"xtest"}},
	}
	for _, c := range cases {
		if got := backendOrder(c.env); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPickBackend(t *testing.T) {
	boom := errors.New("permission denied")
	open := map[string]func() (string, error){
		"uinput": func() (string, error) { return "", boom },
		"xtest":  func() (string, error) { return "xt", nil },
		"portal": func() (string, error) { t.Error("portal opened after xtest"); return "", nil },
	}
	be, name, err := pickBackend([]string{"uinput", "xtest", "portal"}, open)
	if err != nil || be != "xt" || name != "xtest" {
		t.Errorf("picked %q %q %v", be, name, err)
	}
	_, _, err = pickBackend([]string{"uinput", "nope"}, open)
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, boom) {
		t.Errorf("all failed: %v", err)
	}
}

func TestPortalPaths(t *testing.T) {
	if got := portalRequestPath(":1.42", "tok"); got != "/org/freedesktop/portal/desktop/request/1_42/tok" {
		t.Errorf("request path %q", got)
	}
	var acc wheelAccum
	if got := portalSteps(WheelNotch, &acc); got != -1 {
		t.Errorf("one notch up = %d steps, want -1", got)
	}
}
