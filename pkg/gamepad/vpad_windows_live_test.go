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

	// Other software can add XInput devices of its own as a pad appears
	// (Steam Input mirrors the controllers it sees), so look for the new
	// slot that reads what was sent.
	slot, others := -1, map[int]State{}
	for deadline := time.Now().Add(5 * time.Second); slot < 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, s := range slotsNow() {
			if slices.Contains(before, s) {
				continue
			}
			var raw RawState
			if capture.Read(s, &raw) != nil {
				continue
			}
			m := capture.DefaultMapping(s)
			if got := m.Apply(&raw); got == want {
				slot = s
			} else {
				others[s] = got
			}
		}
	}
	if slot < 0 {
		t.Fatalf("no new XInput controller reads %+v (before %v, other new slots %v)", want, before, others)
	}
	delete(others, slot)
	t.Logf("virtual Xbox 360 pad is XInput slot %d; other new slots %v", slot, others)

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

	// Faster than the pad is polled, as a client mashing a button: the
	// last state, a release, must still land.
	for i := range 200 {
		if err := pads.Submit(3, State{Buttons: ButtonA * uint32(i%2), LX: int16(i)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Microsecond)
	}
	final, m := State{LX: -777}, capture.DefaultMapping(slot)
	pads.Submit(3, final)
	var last State
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		var raw RawState
		capture.Read(slot, &raw)
		if last = m.Apply(&raw); last == final {
			break
		}
	}
	if last != final {
		t.Errorf("after a burst slot %d reads %+v, want %+v", slot, last, final)
	}

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
	ds4 := State{Buttons: ButtonB | ButtonDPadLeft, LX: -32768}
	if err := pads.Submit(0, ds4); err != nil {
		t.Fatalf("DualShock 4 submit: %v", err)
	}
	if s := pads.Slots(); s[0] != 0 {
		t.Errorf("DS4 slots = %v", s)
	}
	if !pads.slots[0].target.(*vigemTarget).waitDelivered(time.Second) {
		t.Fatal("DualShock 4 never took its first report")
	}
}

// Plugs two ViGEmBus pads in with different states, swaps their slots and
// expects the two XInput controllers to swap states with no further input.
// Runs only with PLANCK_LIVE_GAMEPAD_TEST=1.
func TestLiveVirtualPadReorder(t *testing.T) {
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
	var before []int
	for _, c := range capture.Controllers() {
		before = append(before, c.Slot)
	}
	pads, err := OpenVirtualPads(PadXbox360)
	if err != nil {
		t.Fatal(err)
	}
	defer pads.Close()
	first, second := State{Buttons: ButtonA, LX: 11111}, State{Buttons: ButtonB, LX: -22222}
	if err := pads.Submit(1, first); err != nil {
		t.Fatal(err)
	}
	if err := pads.Submit(2, second); err != nil {
		t.Fatal(err)
	}
	find := func(want State) int {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			for _, c := range capture.Controllers() {
				if slices.Contains(before, c.Slot) {
					continue
				}
				var raw RawState
				if capture.Read(c.Slot, &raw) == nil {
					if m := capture.DefaultMapping(c.Slot); m.Apply(&raw) == want {
						return c.Slot
					}
				}
			}
		}
		t.Fatalf("no new XInput controller reads %+v", want)
		return -1
	}
	a, b := find(first), find(second)
	if err := pads.ReorderSlot(0, 1); err != nil {
		t.Fatal(err)
	}
	if got := find(second); got != a {
		t.Errorf("after reorder %+v is on XInput slot %d, want %d", second, got, a)
	}
	if got := find(first); got != b {
		t.Errorf("after reorder %+v is on XInput slot %d, want %d", first, got, b)
	}
}
