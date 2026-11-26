// Package uinput creates virtual input devices through /dev/uinput.
package uinput

import (
	"encoding/binary"

	"github.com/planckdotgg/planck/internal/linux/evdev"
)

const (
	uiType = 'U'

	setupSize    = 92
	absSetupSize = 28
	nameMax      = 80
)

var (
	ioctlDevCreate  = evdev.IO(uiType, 1)
	ioctlDevDestroy = evdev.IO(uiType, 2)
	ioctlDevSetup   = evdev.IOW(uiType, 3, setupSize)
	ioctlAbsSetup   = evdev.IOW(uiType, 4, absSetupSize)
	ioctlSetEvBit   = evdev.IOW(uiType, 100, 4)
	ioctlSetKeyBit  = evdev.IOW(uiType, 101, 4)
	ioctlSetRelBit  = evdev.IOW(uiType, 102, 4)
	ioctlSetAbsBit  = evdev.IOW(uiType, 103, 4)
	ioctlSetFFBit   = evdev.IOW(uiType, 107, 4)
	ioctlSetPropBit = evdev.IOW(uiType, 110, 4)
)

// Event codes uinput uses to ask for force-feedback uploads and erases.
const (
	uiFFUpload = 1
	uiFFErase  = 2
)

// Input properties.
const PropDirect = 0x01

// AbsAxis is one absolute axis of a device.
type AbsAxis struct {
	Code       uint16
	Min, Max   int32
	Fuzz, Flat int32
	Resolution int32
}

// Config describes a device to create.
type Config struct {
	Name                 string
	Bus, Vendor, Product uint16
	Version              uint16
	Keys                 []uint16
	Rels                 []uint16
	Abs                  []AbsAxis
	Props                []uint16
	// Rumble gives the device force feedback of type FF_RUMBLE.
	Rumble bool
}

// FFEffectsMax is how many force-feedback effects a device holds at once.
const FFEffectsMax = 16

// setupBytes is struct uinput_setup: input_id, name[80], ff_effects_max.
func (c *Config) setupBytes() []byte {
	b := make([]byte, setupSize)
	binary.NativeEndian.PutUint16(b[0:], c.Bus)
	binary.NativeEndian.PutUint16(b[2:], c.Vendor)
	binary.NativeEndian.PutUint16(b[4:], c.Product)
	binary.NativeEndian.PutUint16(b[6:], c.Version)
	copy(b[8:8+nameMax-1], c.Name)
	if c.Rumble {
		binary.NativeEndian.PutUint32(b[8+nameMax:], FFEffectsMax)
	}
	return b
}

// absSetupBytes is struct uinput_abs_setup: code, then input_absinfo.
func absSetupBytes(a AbsAxis) []byte {
	b := make([]byte, absSetupSize)
	binary.NativeEndian.PutUint16(b[0:], a.Code)
	binary.NativeEndian.PutUint32(b[4:], 0)
	binary.NativeEndian.PutUint32(b[8:], uint32(a.Min))
	binary.NativeEndian.PutUint32(b[12:], uint32(a.Max))
	binary.NativeEndian.PutUint32(b[16:], uint32(a.Fuzz))
	binary.NativeEndian.PutUint32(b[20:], uint32(a.Flat))
	binary.NativeEndian.PutUint32(b[24:], uint32(a.Resolution))
	return b
}
