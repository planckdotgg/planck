package gamepad

import (
	"testing"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

func padValue(evs []evdev.Event, typ, code uint16) int32 {
	for _, e := range evs {
		if e.Type == typ && e.Code == code {
			return e.Value
		}
	}
	return -999
}

func TestPadEvents(t *testing.T) {
	evs := padEvents(State{
		Buttons: ButtonA | ButtonX | ButtonGuide | ButtonDPadLeft | ButtonDPadUp,
		LT:      200, RT: 7, LX: -1000, LY: 32767, RX: 5, RY: -32768,
	})
	key := func(code uint16) int32 { return padValue(evs, evdev.EvKey, code) }
	abs := func(code uint16) int32 { return padValue(evs, evdev.EvAbs, code) }
	if key(evdev.BtnSouth) != 1 || key(evdev.BtnNorth) != 1 || key(evdev.BtnMode) != 1 || key(evdev.BtnEast) != 0 {
		t.Errorf("buttons: A %d X(north) %d guide %d B %d", key(evdev.BtnSouth), key(evdev.BtnNorth), key(evdev.BtnMode), key(evdev.BtnEast))
	}
	if abs(evdev.AbsX) != -1000 || abs(evdev.AbsY) != -32767 || abs(evdev.AbsRX) != 5 || abs(evdev.AbsRY) != 32767 {
		t.Errorf("sticks: %d %d %d %d", abs(evdev.AbsX), abs(evdev.AbsY), abs(evdev.AbsRX), abs(evdev.AbsRY))
	}
	if abs(evdev.AbsZ) != 200 || abs(evdev.AbsRZ) != 7 {
		t.Errorf("triggers: %d %d", abs(evdev.AbsZ), abs(evdev.AbsRZ))
	}
	if abs(evdev.AbsHat0X) != -1 || abs(evdev.AbsHat0Y) != -1 {
		t.Errorf("hat: %d %d (left and up are negative)", abs(evdev.AbsHat0X), abs(evdev.AbsHat0Y))
	}
	both := padEvents(State{Buttons: ButtonDPadLeft | ButtonDPadRight})
	if padValue(both, evdev.EvAbs, evdev.AbsHat0X) != 0 {
		t.Error("opposite directions did not cancel")
	}
}

func TestVirtualPadConfig(t *testing.T) {
	c := virtualPadConfig(0)
	if c.Vendor != 0x045e || c.Product != 0x028e || !c.Rumble || len(c.Keys) != len(virtualPadKeyMap) || len(c.Abs) != 8 {
		t.Errorf("config %+v", c)
	}
	if virtualPadConfig(2).Name == c.Name {
		t.Error("slots share a name")
	}
	for _, k := range virtualPadKeyMap {
		found := false
		for _, code := range c.Keys {
			found = found || code == k.to
		}
		if !found {
			t.Errorf("config lacks button 0x%x", k.to)
		}
	}
}

func TestRumbleLevels(t *testing.T) {
	if l, s := rumbleLevels(0xFFFF, 0x0100); l != 255 || s != 1 {
		t.Errorf("levels %d %d", l, s)
	}
	if stickValue(-32768) != 32767 || stickValue(32767) != -32767 || stickValue(0) != 0 {
		t.Error("stickValue")
	}
}
