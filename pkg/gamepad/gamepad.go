// Package gamepad reads the controllers attached to a client, maps their raw
// buttons and axes onto the XInput layout Planck sends (with SDL
// gamecontrollerdb mapping strings for import and export), applies the
// user's calibration, and drives virtual controllers on a host.
package gamepad

import (
	"errors"
	"math"
)

// Buttons use the XInput bit layout, plus Guide, as input.GamepadState does.
const (
	ButtonDPadUp        uint32 = 0x0001
	ButtonDPadDown      uint32 = 0x0002
	ButtonDPadLeft      uint32 = 0x0004
	ButtonDPadRight     uint32 = 0x0008
	ButtonStart         uint32 = 0x0010
	ButtonBack          uint32 = 0x0020
	ButtonLeftThumb     uint32 = 0x0040
	ButtonRightThumb    uint32 = 0x0080
	ButtonLeftShoulder  uint32 = 0x0100
	ButtonRightShoulder uint32 = 0x0200
	ButtonGuide         uint32 = 0x0400
	ButtonA             uint32 = 0x1000
	ButtonB             uint32 = 0x2000
	ButtonX             uint32 = 0x4000
	ButtonY             uint32 = 0x8000
)

// State is one controller in the wire layout: stick axes signed with
// positive X right and positive Y up, as in XInput.
type State struct {
	Buttons        uint32
	LT, RT         uint8
	LX, LY, RX, RY int16
}

type Connection uint8

const (
	ConnectionUnknown Connection = iota
	ConnectionUSB
	ConnectionBluetooth
	// ConnectionWireless is a radio that is not Bluetooth, such as the Xbox
	// Wireless Adapter, or a wireless pad XInput cannot tell apart.
	ConnectionWireless
)

func (c Connection) String() string {
	switch c {
	case ConnectionUSB:
		return "USB"
	case ConnectionBluetooth:
		return "Bluetooth"
	case ConnectionWireless:
		return "Wireless"
	}
	return "Unknown"
}

// ControllerInfo describes one attached controller. ID stays the same while
// the controller stays attached; Slot is its place among the attached
// controllers, the pad index a stream sends it as.
type ControllerInfo struct {
	ID         int
	Slot       int
	Name       string
	VendorID   uint16
	ProductID  uint16
	GUID       string // SDL joystick GUID, 32 hex digits
	Connection Connection
	BatteryPct int // -1 when unknown or wired
	Connected  bool
}

// Axis is one raw axis value and its logical range as the device reports it.
type Axis struct{ Value, Min, Max int32 }

// Norm maps the value onto -1..1.
func (a Axis) Norm() float64 {
	if a.Max <= a.Min {
		return 0
	}
	v := 2*float64(a.Value-a.Min)/float64(a.Max-a.Min) - 1
	return math.Max(-1, math.Min(1, v))
}

// Hat directions are SDL's hat bits, as mapping strings use them.
const (
	HatUp    uint8 = 1
	HatRight uint8 = 2
	HatDown  uint8 = 4
	HatLeft  uint8 = 8
)

// RawState is a controller's inputs before mapping, indexed as SDL indexes
// them: bN, aN and hN in a mapping string. Axes follow SDL's direction, so a
// stick's Y grows downward.
type RawState struct {
	Buttons []bool
	Axes    []Axis
	Hats    []uint8
}

var (
	ErrUnsupported  = errors.New("gamepad: unsupported on this platform")
	ErrNotConnected = errors.New("gamepad: controller not connected")
)
