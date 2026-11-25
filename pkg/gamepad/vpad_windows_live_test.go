//go:build windows

package gamepad

import (
	"os"
	"slices"
	"testing"
	"time"
)

// Plugs a ViGEmBus Xbox 360 pad in, reads it back through XInput, sends it
// rumble through XInput and expects the rumble back from ViGEmBus; then
// plugs a DualShock 4 in. Runs only with PLANCK_LIVE_GAMEPAD_TEST=1, on a
// host whose games will not mind a pad appearing.
func TestLiveVirtualPadLoopback(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_GAMEPAD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_GAMEPAD_TEST=1 to plug in virtual controllers")
	}
	if st := VirtualPadStatus(); !st.Installed {
		t.Skipf("ViGEmBus not installed: %+v", st)
	}
	capture, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	slotsNow := func() []int {
		var s []int
		for _, c := range capture.Controllers() {
			s = append(s, c.Slot)
		}
		return s
	}
	before := slotsNow()

	pads, err := OpenVirtualPads(PadXbox360)
	if err != nil {
		t.Fatal(err)
	}
	defer pads.Close()
	rumble := make(chan [3]uint8, 8)
	pads.SetRumbleHandler(func(pad, large, small uint8) { rumble <- [3]uint8{pad, large, small} })
	want := State{Buttons: ButtonA | ButtonY | ButtonDPadUp | ButtonLeftShoulder, LT: 200, RT: 17, LX: 12345, LY: -20000, RX: -32768, RY: 32767}
	if err := pads.Submit(3, want); err != nil {
		t.Fatal(err)
	}

	slot := -1
	for deadline := time.Now().Add(5 * time.Second); slot < 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, s := range slotsNow() {
			if !slices.Contains(before, s) {
				slot = s
			}
		}
	}
	if slot < 0 {
		t.Fatalf("no new XInput controller after plugging in (before %v)", before)
	}
	m := capture.DefaultMapping(slot)
	var got State
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		var raw RawState
		if err := capture.Read(slot, &raw); err != nil {
			t.Fatal(err)
		}
		if got = m.Apply(&raw); got == want {
			break
		}
	}
	if got != want {
		t.Fatalf("XInput slot %d reads %+v, want %+v", slot, got, want)
	}
	t.Logf("virtual Xbox 360 pad is XInput slot %d: %+v", slot, got)

	if err := capture.Rumble(slot, 200, 90); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-rumble:
		if r != [3]uint8{3, 200, 90} {
			t.Errorf("rumble = %v, want pad 3 large 200 small 90", r)
		}
	case <-time.After(3 * time.Second):
		t.Error("no rumble back from ViGEmBus")
	}
	capture.Rumble(slot, 0, 0)

	if err := pads.ResetSlots(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); slices.Contains(slotsNow(), slot) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if slices.Contains(slotsNow(), slot) {
		t.Errorf("slot %d still connected after ResetSlots", slot)
	}

	pads.SetType(PadDS4)
	if err := pads.Submit(0, State{Buttons: ButtonB | ButtonDPadLeft, LX: -32768}); err != nil {
		t.Fatalf("DualShock 4 submit: %v", err)
	}
	if s := pads.Slots(); s[0] != 0 {
		t.Errorf("DS4 slots = %v", s)
	}
}
