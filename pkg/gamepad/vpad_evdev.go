package gamepad

import (
	"github.com/planckdotgg/planck/internal/linux/evdev"
	"github.com/planckdotgg/planck/internal/linux/uinput"
)

const (
	vendorMicrosoft  = 0x045E
	productXbox360   = 0x028E
	productVersion   = 0x0114
	virtualPadPrefix = "Planck Virtual"
)

var virtualPadButtons = []uint16{
	evdev.BtnSouth, evdev.BtnEast, evdev.BtnNorth, evdev.BtnWest, evdev.BtnTL, evdev.BtnTR,
	evdev.BtnSelect, evdev.BtnStart, evdev.BtnMode, evdev.BtnThumbL, evdev.BtnThumbR,
}

// virtualPadConfig is an Xbox 360 controller as the xpad driver presents
// it, so SDL and Proton pick the standard mapping for it by vendor and
// product.
func virtualPadConfig(slot int) uinput.Config {
	stick := func(code uint16) uinput.AbsAxis {
		return uinput.AbsAxis{Code: code, Min: -32768, Max: 32767, Fuzz: 16, Flat: 128}
	}
	trigger := func(code uint16) uinput.AbsAxis { return uinput.AbsAxis{Code: code, Min: 0, Max: 255} }
	hat := func(code uint16) uinput.AbsAxis { return uinput.AbsAxis{Code: code, Min: -1, Max: 1} }
	name := virtualPadPrefix + " Xbox 360 Controller"
	if slot > 0 {
		name += " " + string(rune('1'+slot))
	}
	return uinput.Config{
		Name: name, Bus: evdev.BusUSB, Vendor: vendorMicrosoft, Product: productXbox360, Version: productVersion,
		Keys: virtualPadButtons,
		Abs: []uinput.AbsAxis{
			stick(evdev.AbsX), stick(evdev.AbsY), trigger(evdev.AbsZ),
			stick(evdev.AbsRX), stick(evdev.AbsRY), trigger(evdev.AbsRZ),
			hat(evdev.AbsHat0X), hat(evdev.AbsHat0Y),
		},
		Rumble: true,
	}
}

var virtualPadKeyMap = [...]struct {
	from uint32
	to   uint16
}{
	{ButtonA, evdev.BtnSouth}, {ButtonB, evdev.BtnEast}, {ButtonX, evdev.BtnNorth}, {ButtonY, evdev.BtnWest},
	{ButtonLeftShoulder, evdev.BtnTL}, {ButtonRightShoulder, evdev.BtnTR},
	{ButtonBack, evdev.BtnSelect}, {ButtonStart, evdev.BtnStart}, {ButtonGuide, evdev.BtnMode},
	{ButtonLeftThumb, evdev.BtnThumbL}, {ButtonRightThumb, evdev.BtnThumbR},
}

// stickValue flips a stick axis, positive up in State, to evdev's positive
// down.
func stickValue(v int16) int32 {
	if v == -32768 {
		return 32767
	}
	return -int32(v)
}

func hatValue(s uint32, minus, plus uint32) int32 {
	var v int32
	if s&plus != 0 {
		v++
	}
	if s&minus != 0 {
		v--
	}
	return v
}

// padEvents is every input of a virtual pad at state s. The kernel drops
// the ones that did not change.
func padEvents(s State) []evdev.Event {
	evs := make([]evdev.Event, 0, len(virtualPadKeyMap)+8)
	for _, k := range virtualPadKeyMap {
		v := int32(0)
		if s.Buttons&k.from != 0 {
			v = 1
		}
		evs = append(evs, evdev.Event{Type: evdev.EvKey, Code: k.to, Value: v})
	}
	abs := func(code uint16, v int32) { evs = append(evs, evdev.Event{Type: evdev.EvAbs, Code: code, Value: v}) }
	abs(evdev.AbsX, int32(s.LX))
	abs(evdev.AbsY, stickValue(s.LY))
	abs(evdev.AbsRX, int32(s.RX))
	abs(evdev.AbsRY, stickValue(s.RY))
	abs(evdev.AbsZ, int32(s.LT))
	abs(evdev.AbsRZ, int32(s.RT))
	abs(evdev.AbsHat0X, hatValue(s.Buttons, ButtonDPadLeft, ButtonDPadRight))
	abs(evdev.AbsHat0Y, hatValue(s.Buttons, ButtonDPadUp, ButtonDPadDown))
	return evs
}

// rumbleLevels turns a game's 16-bit strengths into the 0..255 a
// RumbleFunc takes.
func rumbleLevels(strong, weak uint16) (large, small uint8) {
	return uint8(strong >> 8), uint8(weak >> 8)
}
