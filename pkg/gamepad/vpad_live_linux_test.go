package gamepad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findEventNode waits for the /dev/input node of the device called name.
func findEventNode(t *testing.T, name string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		names, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, n := range names {
			if b, err := os.ReadFile(n); err == nil && strings.TrimSpace(string(b)) == name {
				return "/dev/input/" + filepath.Base(filepath.Dir(filepath.Dir(n)))
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no input node for %q", name)
	return ""
}

// TestLiveVirtualPadLoopback plugs a virtual pad in through /dev/uinput
// and reads it back through the evdev reader, then sends it a rumble the
// way a game would. It needs write access to /dev/uinput.
func TestLiveVirtualPadLoopback(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_INPUT_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_INPUT_TEST=1 to create a virtual gamepad")
	}
	pads, err := OpenVirtualPads(PadXbox360)
	if err != nil {
		t.Skip(err)
	}
	defer pads.Close()
	type rumble struct{ pad, large, small uint8 }
	got := make(chan rumble, 4)
	pads.SetRumbleHandler(func(pad, large, small uint8) { got <- rumble{pad, large, small} })
	want := State{Buttons: ButtonA | ButtonLeftShoulder | ButtonDPadLeft | ButtonDPadUp, LT: 200, RT: 9, LX: 12000, LY: -8000, RX: -32768, RY: 32767}
	if err := pads.Submit(0, want); err != nil {
		t.Fatal(err)
	}
	node := findEventNode(t, "Planck Virtual Xbox 360 Controller")
	p, err := openEvdevPad(node)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if p.caps.Vendor != 0x045e || p.caps.Product != 0x028e || !p.caps.Rumble {
		t.Errorf("caps %+v", p.caps)
	}
	m := Mapping{Bindings: p.bindings}
	var s State
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := p.drain(); err != nil {
			t.Fatal(err)
		}
		var raw RawState
		p.layout.fill(&raw, func(c uint16) bool { return p.keys[c/8]&(1<<(c%8)) != 0 }, func(c uint16) int32 { return p.abs[c] })
		if s = m.Apply(&raw); s == want {
			break
		}
	}
	if s != want {
		t.Fatalf("read back %+v, want %+v", s, want)
	}

	c := &Capture{skipped: map[string]struct{}{}, pads: []*evdevPad{p}}
	p.info = p.caps.info(1)
	if err := c.Rumble(1, 200, 100); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r != (rumble{0, 200, 100}) {
			t.Errorf("rumble %+v, want 0/200/100", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the game's rumble never reached the handler")
	}
	if err := c.Rumble(1, 0, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.large != 0 || r.small != 0 {
			t.Errorf("stop rumble %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stop never reached the handler")
	}
}
