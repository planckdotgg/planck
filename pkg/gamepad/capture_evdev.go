package gamepad

import (
	"encoding/binary"
	"slices"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

// evdevAxis is one absolute axis of an evdev device.
type evdevAxis struct {
	Code     uint16
	Min, Max int32
}

// evdevCaps is what an evdev device reports about itself.
type evdevCaps struct {
	Name                          string
	Bus, Vendor, Product, Version uint16
	Keys                          []uint16 // codes present, ascending
	Abs                           []evdevAxis
	Rumble                        bool
}

func (c *evdevCaps) hasKey(code uint16) bool { _, ok := slices.BinarySearch(c.Keys, code); return ok }

func (c *evdevCaps) hasAbs(code uint16) bool {
	return slices.ContainsFunc(c.Abs, func(a evdevAxis) bool { return a.Code == code })
}

// isGamepad tells a game controller from the mice, touchpads, tablets and
// keyboards that also report absolute axes or buttons: it has a stick or
// D-pad axis pair, a joystick or gamepad button, and no touch or pointer
// buttons.
func (c *evdevCaps) isGamepad() bool {
	hasPair := c.hasAbs(evdev.AbsX) && c.hasAbs(evdev.AbsY) || c.hasAbs(evdev.AbsHat0X) && c.hasAbs(evdev.AbsHat0Y)
	hasButton := slices.ContainsFunc(c.Keys, func(k uint16) bool { return k >= evdev.BtnJoystick && k < btnDigi })
	pointer := c.hasKey(evdev.BtnTouch) || c.hasKey(evdev.BtnToolFinger) || c.hasKey(evdev.BtnLeft)
	return hasPair && hasButton && !pointer
}

// evdevLayout indexes a device's inputs the way SDL does, so that SDL
// gamecontrollerdb mappings for Linux apply: buttons in code order from
// BTN_JOYSTICK up, then the codes below it; axes in code order, with each
// pair of HAT axes folded into one hat.
type evdevLayout struct {
	buttons []uint16
	axes    []evdevAxis
	hats    []uint16 // the X axis code of each hat; Y follows it
}

func isHatAxis(code uint16) bool { return code >= evdev.AbsHat0X && code <= evdev.AbsHat3Y }

func (c *evdevCaps) layout() evdevLayout {
	var l evdevLayout
	for _, k := range c.Keys {
		if k >= evdev.BtnJoystick && k <= evdev.KeyMax {
			l.buttons = append(l.buttons, k)
		}
	}
	for _, k := range c.Keys {
		if k < evdev.BtnJoystick {
			l.buttons = append(l.buttons, k)
		}
	}
	for _, a := range c.Abs {
		if !isHatAxis(a.Code) {
			l.axes = append(l.axes, a)
			continue
		}
		if x := a.Code &^ 1; !slices.Contains(l.hats, x) {
			l.hats = append(l.hats, x)
		}
	}
	return l
}

func (l *evdevLayout) button(code uint16) (Binding, bool) {
	if i := slices.Index(l.buttons, code); i >= 0 {
		return Binding{Kind: SourceButton, Index: i}, true
	}
	return Binding{}, false
}

func (l *evdevLayout) axis(code uint16) (Binding, bool) {
	if i := slices.IndexFunc(l.axes, func(a evdevAxis) bool { return a.Code == code }); i >= 0 {
		return Binding{Kind: SourceAxis, Index: i}, true
	}
	return Binding{}, false
}

const (
	vendorSony = 0x054C
	btnDigi    = 0x140
)

// bindings is the standard-controller mapping of a device that follows the
// kernel's gamepad conventions (xpad, hid-playstation, hid-nintendo and
// the generic HID pads): face buttons BTN_SOUTH/EAST/NORTH/WEST, BTN_TL/TR
// bumpers, analog triggers on ABS_Z/ABS_RZ or buttons BTN_TL2/TR2, a hat or
// BTN_DPAD_* for the D-pad. Xbox pads send X as BTN_NORTH and Y as BTN_WEST;
// Sony pads send them by position, so the two swap there.
func (l *evdevLayout) bindings(vendor uint16) [NumInputs]Binding {
	var b [NumInputs]Binding
	set := func(in Input, bind Binding, ok bool) {
		if ok {
			b[in] = bind
		}
	}
	btn := func(in Input, code uint16) { bind, ok := l.button(code); set(in, bind, ok) }
	axis := func(in Input, code uint16) { bind, ok := l.axis(code); set(in, bind, ok) }
	x, y := uint16(evdev.BtnNorth), uint16(evdev.BtnWest)
	if vendor == vendorSony {
		x, y = y, x
	}
	btn(InputA, evdev.BtnSouth)
	btn(InputB, evdev.BtnEast)
	btn(InputX, x)
	btn(InputY, y)
	btn(InputBack, evdev.BtnSelect)
	btn(InputStart, evdev.BtnStart)
	btn(InputGuide, evdev.BtnMode)
	btn(InputLeftStick, evdev.BtnThumbL)
	btn(InputRightStick, evdev.BtnThumbR)
	btn(InputLeftShoulder, evdev.BtnTL)
	btn(InputRightShoulder, evdev.BtnTR)
	axis(InputLeftX, evdev.AbsX)
	axis(InputLeftY, evdev.AbsY)
	axis(InputRightX, evdev.AbsRX)
	axis(InputRightY, evdev.AbsRY)
	if _, ok := l.axis(evdev.AbsZ); ok {
		axis(InputLeftTrigger, evdev.AbsZ)
		axis(InputRightTrigger, evdev.AbsRZ)
	} else {
		btn(InputLeftTrigger, evdev.BtnTL2)
		btn(InputRightTrigger, evdev.BtnTR2)
	}
	if i := slices.Index(l.hats, evdev.AbsHat0X); i >= 0 {
		b[InputDPadUp] = Binding{Kind: SourceHat, Index: i, HatMask: HatUp}
		b[InputDPadRight] = Binding{Kind: SourceHat, Index: i, HatMask: HatRight}
		b[InputDPadDown] = Binding{Kind: SourceHat, Index: i, HatMask: HatDown}
		b[InputDPadLeft] = Binding{Kind: SourceHat, Index: i, HatMask: HatLeft}
	} else {
		btn(InputDPadUp, evdev.BtnDPadUp)
		btn(InputDPadDown, evdev.BtnDPadDown)
		btn(InputDPadLeft, evdev.BtnDPadLeft)
		btn(InputDPadRight, evdev.BtnDPadRight)
	}
	return b
}

// fill writes the device's inputs into raw: down says whether a button is
// held and value gives an axis's current value.
func (l *evdevLayout) fill(raw *RawState, down func(code uint16) bool, value func(code uint16) int32) {
	raw.Buttons, raw.Axes, raw.Hats = raw.Buttons[:0], raw.Axes[:0], raw.Hats[:0]
	for _, k := range l.buttons {
		raw.Buttons = append(raw.Buttons, down(k))
	}
	for _, a := range l.axes {
		raw.Axes = append(raw.Axes, Axis{Value: value(a.Code), Min: a.Min, Max: a.Max})
	}
	for _, x := range l.hats {
		var h uint8
		switch dx := value(x); {
		case dx < 0:
			h |= HatLeft
		case dx > 0:
			h |= HatRight
		}
		switch dy := value(x + 1); {
		case dy < 0:
			h |= HatUp
		case dy > 0:
			h |= HatDown
		}
		raw.Hats = append(raw.Hats, h)
	}
}

func (c *evdevCaps) connection() Connection {
	switch c.Bus {
	case evdev.BusUSB:
		return ConnectionUSB
	case evdev.BusBluetooth:
		return ConnectionBluetooth
	}
	return ConnectionUnknown
}

func (c *evdevCaps) info(id int) ControllerInfo {
	conn := c.connection()
	return ControllerInfo{
		ID: id, Name: c.Name, VendorID: c.Vendor, ProductID: c.Product, Connection: conn,
		GUID: HIDGUID(conn, c.Vendor, c.Product, c.Version), BatteryPct: -1, Connected: true,
	}
}

// ffEffectBytes is a struct ff_effect for an endless rumble of the given
// strengths; id is -1 to have the kernel assign one.
func ffEffectBytes(id int16, strong, weak uint16) []byte {
	b := make([]byte, evdev.FFEffectSize)
	binary.NativeEndian.PutUint16(b[0:], evdev.FFRumble)
	binary.NativeEndian.PutUint16(b[2:], uint16(id))
	binary.NativeEndian.PutUint16(b[evdev.FFRumbleOffset:], strong)
	binary.NativeEndian.PutUint16(b[evdev.FFRumbleOffset+2:], weak)
	return b
}

func ffEffectID(b []byte) int16 { return int16(binary.NativeEndian.Uint16(b[2:])) }

// motor widens a 0..255 motor strength to the 16 bits evdev takes.
func motor(v uint8) uint16 { return uint16(v) * 257 }
