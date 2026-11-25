package gamepad

import (
	"cmp"
	"slices"
)

// HID usage pages and usages a controller's inputs use.
const (
	pageGenericDesktop = 0x01
	pageSimulation     = 0x02
	pageButton         = 0x09
	pageConsumer       = 0x0C

	usageGamepad  = 0x05
	usageJoystick = 0x04
	usageX        = 0x30
	usageY        = 0x31
	usageZ        = 0x32
	usageRx       = 0x33
	usageRy       = 0x34
	usageRz       = 0x35
	usageWheel    = 0x38
	usageHat      = 0x39
	usageStart    = 0x3D
	usageSelect   = 0x3E
	usageMainMenu = 0x85
	usageDPadUp   = 0x90
	usageDPadLeft = 0x93

	usageRudder      = 0xBA
	usageThrottle    = 0xBB
	usageAccelerator = 0xC4
	usageBrake       = 0xC5

	usageACHome = 0x223
	usageACBack = 0x224
)

type elementKind uint8

const (
	elementNone elementKind = iota
	elementButton
	elementAxis
	elementHat
)

// hidElement is one input element of a HID controller.
type hidElement struct {
	page, usage uint32
	cookie      uint32
	min, max    int32
	ref         uintptr
}

// classify sorts an element into what SDL reads it as.
func classify(page, usage uint32) elementKind {
	switch page {
	case pageGenericDesktop:
		switch {
		case usage >= usageX && usage <= usageWheel:
			return elementAxis
		case usage == usageHat:
			return elementHat
		case usage >= usageDPadUp && usage <= usageDPadLeft, usage == usageStart, usage == usageSelect, usage == usageMainMenu:
			return elementButton
		}
	case pageSimulation:
		switch usage {
		case usageRudder, usageThrottle, usageAccelerator, usageBrake:
			return elementAxis
		}
	case pageButton:
		return elementButton
	case pageConsumer:
		if usage == usageACHome || usage == usageACBack {
			return elementButton
		}
	}
	return elementNone
}

// hidLayout is a controller's elements in raw order: each kind sorted by
// usage page, usage and cookie, with duplicate cookies dropped.
type hidLayout struct{ buttons, axes, hats []hidElement }

func newHIDLayout(elems []hidElement) hidLayout {
	var l hidLayout
	seen := map[uint32]bool{}
	for _, e := range elems {
		if seen[e.cookie] {
			continue
		}
		switch classify(e.page, e.usage) {
		case elementButton:
			l.buttons = append(l.buttons, e)
		case elementAxis:
			l.axes = append(l.axes, e)
		case elementHat:
			l.hats = append(l.hats, e)
		default:
			continue
		}
		seen[e.cookie] = true
	}
	order := func(a, b hidElement) int {
		return cmp.Or(cmp.Compare(a.page, b.page), cmp.Compare(a.usage, b.usage), cmp.Compare(a.cookie, b.cookie))
	}
	slices.SortFunc(l.buttons, order)
	slices.SortFunc(l.axes, order)
	slices.SortFunc(l.hats, order)
	return l
}

var hatDirections = [8]uint8{HatUp, HatUp | HatRight, HatRight, HatRight | HatDown, HatDown, HatDown | HatLeft, HatLeft, HatLeft | HatUp}

// hatBits turns a hat switch value into SDL hat bits. Eight-way hats count
// 0 (up) clockwise to 7; four-way ones 0 to 3. Anything outside the logical
// range is centred.
func hatBits(v, min, max int32) uint8 {
	pos := v - min
	switch max - min + 1 {
	case 4:
		pos *= 2
	case 8:
	default:
		return 0
	}
	if pos < 0 || pos >= 8 {
		return 0
	}
	return hatDirections[pos]
}

// hidDefaultBindings guesses a HID controller's sticks and triggers from
// its axis usages: X and Y are the left stick; the right stick is Z and Rz
// (with Rx and Ry, or Brake and Accelerator, as triggers) or else Rx and Ry
// (with Z and Rz as triggers). Buttons and the hat keep the XInput order.
func hidDefaultBindings(axes []hidElement) [NumInputs]Binding {
	b := defaultBindings
	for _, in := range []Input{InputLeftX, InputLeftY, InputRightX, InputRightY, InputLeftTrigger, InputRightTrigger} {
		b[in] = Binding{}
	}
	find := func(page, usage uint32) (Binding, bool) {
		for i, a := range axes {
			if a.page == page && a.usage == usage {
				return Binding{Kind: SourceAxis, Index: i}, true
			}
		}
		return Binding{}, false
	}
	set := func(in Input, page, usage uint32) bool {
		bind, ok := find(page, usage)
		if ok {
			b[in] = bind
		}
		return ok
	}
	set(InputLeftX, pageGenericDesktop, usageX)
	set(InputLeftY, pageGenericDesktop, usageY)
	_, hasZ := find(pageGenericDesktop, usageZ)
	_, hasRz := find(pageGenericDesktop, usageRz)
	if hasZ && hasRz {
		set(InputRightX, pageGenericDesktop, usageZ)
		set(InputRightY, pageGenericDesktop, usageRz)
		if !set(InputLeftTrigger, pageGenericDesktop, usageRx) {
			set(InputLeftTrigger, pageSimulation, usageBrake)
		}
		if !set(InputRightTrigger, pageGenericDesktop, usageRy) {
			set(InputRightTrigger, pageSimulation, usageAccelerator)
		}
		return b
	}
	set(InputRightX, pageGenericDesktop, usageRx)
	set(InputRightY, pageGenericDesktop, usageRy)
	set(InputLeftTrigger, pageGenericDesktop, usageZ)
	set(InputRightTrigger, pageGenericDesktop, usageRz)
	return b
}

// rumbleReport is the HID output report that sets a wired Sony pad's
// motors, report ID first, laid out as Linux's hid-sony and hid-playstation
// drivers send it; nil for other pads.
func rumbleReport(info ControllerInfo, large, small uint8) []byte {
	if info.VendorID != 0x054C || info.Connection != ConnectionUSB {
		return nil
	}
	switch info.ProductID {
	case 0x05C4, 0x09CC: // DualShock 4, both revisions
		r := make([]byte, 32)
		r[0], r[1], r[4], r[5] = 0x05, 0x01, small, large // flag 0x01: motors only, lightbar untouched
		return r
	case 0x0CE6, 0x0DF2: // DualSense, DualSense Edge
		r := make([]byte, 63)
		r[0], r[1], r[3], r[4] = 0x02, 0x03, small, large // compatible vibration + haptics select
		return r
	}
	return nil
}
