package gamepad

import (
	"errors"
	"slices"
	"testing"
)

type fakeBus struct {
	plugged  []*fakeTarget
	rumbles  []func(large, small uint8)
	closed   bool
	plugErr  error
	lastType PadType
}

type fakeTarget struct {
	states  []State
	unplugs int
}

func (t *fakeTarget) submit(s State) error { t.states = append(t.states, s); return nil }
func (t *fakeTarget) unplug() error        { t.unplugs++; return nil }

func (b *fakeBus) plug(t PadType, onRumble func(large, small uint8)) (padTarget, error) {
	if b.plugErr != nil {
		return nil, b.plugErr
	}
	tg := &fakeTarget{}
	b.plugged, b.rumbles, b.lastType = append(b.plugged, tg), append(b.rumbles, onRumble), t
	return tg, nil
}

func (b *fakeBus) close() error { b.closed = true; return nil }

func TestVirtualPadsSlots(t *testing.T) {
	bus := &fakeBus{}
	v := newVirtualPads(bus, PadDS4)
	for _, pad := range []uint8{7, 2, 7, 0, 9} {
		if err := v.Submit(pad, State{Buttons: uint32(pad)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := v.Slots(), [MaxVirtualPads]int{7, 2, 0, 9}; got != want {
		t.Errorf("slots = %v, want %v", got, want)
	}
	if len(bus.plugged) != 4 || bus.lastType != PadDS4 || len(bus.plugged[0].states) != 2 {
		t.Errorf("plugged %d of type %v; first got %d states", len(bus.plugged), bus.lastType, len(bus.plugged[0].states))
	}
	if err := v.Submit(3, State{}); !errors.Is(err, ErrNoFreeSlot) {
		t.Errorf("fifth pad = %v, want ErrNoFreeSlot", err)
	}

	var got []int
	v.SetRumbleHandler(func(pad, large, small uint8) { got = append(got, int(pad), int(large), int(small)) })
	bus.rumbles[1](10, 20)
	if err := v.ReorderSlot(0, 1); err != nil {
		t.Fatal(err)
	}
	if s := v.Slots(); s[0] != 2 || s[1] != 7 {
		t.Errorf("after reorder slots = %v", s)
	}
	v.Submit(2, State{Buttons: 99})
	if st := bus.plugged[0].states; st[len(st)-1].Buttons != 99 {
		t.Error("pad 2 does not feed slot 0's virtual pad after the reorder")
	}
	bus.rumbles[1](30, 40) // slot 1's pad now carries client pad 7
	if want := []int{2, 10, 20, 7, 30, 40}; !slices.Equal(got, want) {
		t.Errorf("rumble = %v, want %v", got, want)
	}
	if err := v.ReorderSlot(0, 4); err == nil {
		t.Error("ReorderSlot out of range succeeded")
	}

	v.SetType(PadXbox360)
	if err := v.ResetSlots(); err != nil {
		t.Fatal(err)
	}
	for i, tg := range bus.plugged {
		if tg.unplugs != 1 {
			t.Errorf("target %d unplugged %d times", i, tg.unplugs)
		}
	}
	bus.rumbles[0](1, 1)
	if len(got) != 6 {
		t.Error("rumble from an unplugged pad was forwarded")
	}
	v.Submit(9, State{})
	if s := v.Slots(); s[0] != 9 || bus.lastType != PadXbox360 {
		t.Errorf("after reset slots = %v, type %v", s, bus.lastType)
	}
	if err := v.Close(); err != nil || !bus.closed {
		t.Errorf("Close = %v, bus closed %v", err, bus.closed)
	}
	if err := v.Submit(9, State{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Submit after Close = %v", err)
	}
}

func TestReorderIntoFreeSlot(t *testing.T) {
	bus := &fakeBus{}
	v := newVirtualPads(bus, PadXbox360)
	v.Submit(5, State{})
	v.ReorderSlot(0, 2)
	if s := v.Slots(); s != [MaxVirtualPads]int{-1, -1, 5, -1} {
		t.Errorf("slots = %v", s)
	}
	v.Submit(5, State{Buttons: 1})
	if len(bus.plugged) != 1 || len(bus.plugged[0].states) != 2 {
		t.Error("pad 5 lost its virtual pad when moved to a free slot")
	}
}

func TestReorderSlotResubmitsStates(t *testing.T) {
	bus := &fakeBus{}
	v := newVirtualPads(bus, PadXbox360)
	first, second := State{Buttons: ButtonA, LX: 100}, State{Buttons: ButtonB, RT: 200}
	v.Submit(4, first)
	v.Submit(6, second)
	if err := v.ReorderSlot(0, 1); err != nil {
		t.Fatal(err)
	}
	last := func(i int) State { st := bus.plugged[i].states; return st[len(st)-1] }
	if last(0) != second || last(1) != first {
		t.Errorf("after reorder the virtual pads read %+v and %+v, want %+v and %+v", last(0), last(1), second, first)
	}
}

func TestPlugFailureLeavesSlotFree(t *testing.T) {
	bus := &fakeBus{plugErr: errors.New("busy")}
	v := newVirtualPads(bus, PadXbox360)
	if err := v.Submit(0, State{}); err == nil {
		t.Fatal("Submit succeeded")
	}
	if s := v.Slots(); s[0] != -1 {
		t.Errorf("slots = %v", s)
	}
}

func TestToDS4(t *testing.T) {
	r := toDS4(State{Buttons: ButtonA | ButtonY | ButtonStart | ButtonGuide | ButtonDPadUp | ButtonDPadRight,
		LT: 200, LX: -32768, LY: 32767, RX: 32767, RY: -32768})
	wantButtons := uint16(ds4Cross | ds4Triangle | ds4Options | ds4TriggerLeft | 1)
	if r.Buttons != wantButtons || r.Special != ds4SpecialPS || r.TriggerL != 200 || r.TriggerR != 0 {
		t.Errorf("buttons %#x special %d triggers %d/%d", r.Buttons, r.Special, r.TriggerL, r.TriggerR)
	}
	// DS4 Y grows downward: stick up is 0.
	if r.ThumbLX != 0 || r.ThumbLY != 0 || r.ThumbRX != 255 || r.ThumbRY != 255 {
		t.Errorf("thumbs = %d %d %d %d", r.ThumbLX, r.ThumbLY, r.ThumbRX, r.ThumbRY)
	}
	if c := toDS4(State{}); c.Buttons != ds4HatNone || c.ThumbLX != 128 || c.ThumbLY != 127 {
		t.Errorf("neutral = %+v", c)
	}
	for _, tc := range []struct {
		buttons uint32
		hat     uint16
	}{
		{ButtonDPadUp, 0}, {ButtonDPadDown, 4}, {ButtonDPadLeft, 6}, {ButtonDPadDown | ButtonDPadLeft, 5},
		{ButtonDPadUp | ButtonDPadLeft, 7}, {ButtonDPadLeft | ButtonDPadRight, ds4HatNone},
	} {
		if h := ds4Hat(tc.buttons); h != tc.hat {
			t.Errorf("ds4Hat(%#x) = %d, want %d", tc.buttons, h, tc.hat)
		}
	}
}

func TestPadTypes(t *testing.T) {
	for _, pt := range []PadType{PadXbox360, PadDS4, PadDualSense} {
		if got, err := ParsePadType(pt.String()); err != nil || got != pt {
			t.Errorf("ParsePadType(%s) = %v, %v", pt, got, err)
		}
	}
	if _, err := ParsePadType("wiimote"); err == nil {
		t.Error("ParsePadType accepted wiimote")
	}
	vigem := PadDriverStatus{Installed: true, SupportedTypes: []PadType{PadXbox360, PadDS4}}
	if vigem.Effective(PadDualSense) != PadDS4 || vigem.Effective(PadDS4) != PadDS4 {
		t.Error("DualSense should fall back to DS4 on ViGEmBus")
	}
	if (PadDriverStatus{SupportedTypes: []PadType{PadXbox360}}).Effective(PadDualSense) != PadXbox360 {
		t.Error("DualSense should fall back to Xbox 360 without DS4")
	}
	if st := VirtualPadStatus(); !st.Installed && st.InstallHint == "" {
		t.Errorf("status %+v has no install hint", st)
	}
}
