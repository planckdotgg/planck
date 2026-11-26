// Package evdev holds the Linux input event ABI that Planck's host injector,
// virtual gamepads and gamepad reader share: event types and codes, the
// input_event record, and ioctl request numbers.
package evdev

import (
	"encoding/binary"
	"math/bits"
)

const (
	EvSyn     = 0x00
	EvKey     = 0x01
	EvRel     = 0x02
	EvAbs     = 0x03
	EvMsc     = 0x04
	EvFF      = 0x15
	EvUinput  = 0x0101
	SynReport = 0
	SynDrop   = 3
)

const (
	RelX           = 0x00
	RelY           = 0x01
	RelHWheel      = 0x06
	RelWheel       = 0x08
	RelWheelHiRes  = 0x0B
	RelHWheelHiRes = 0x0C
)

const (
	AbsX     = 0x00
	AbsY     = 0x01
	AbsZ     = 0x02
	AbsRX    = 0x03
	AbsRY    = 0x04
	AbsRZ    = 0x05
	AbsHat0X = 0x10
	AbsHat0Y = 0x11
	AbsHat3Y = 0x17
	AbsMax   = 0x3F
)

const (
	BtnJoystick   = 0x120
	BtnLeft       = 0x110
	BtnRight      = 0x111
	BtnMiddle     = 0x112
	BtnSide       = 0x113
	BtnExtra      = 0x114
	BtnSouth      = 0x130
	BtnEast       = 0x131
	BtnNorth      = 0x133
	BtnWest       = 0x134
	BtnTL         = 0x136
	BtnTR         = 0x137
	BtnTL2        = 0x138
	BtnTR2        = 0x139
	BtnSelect     = 0x13A
	BtnStart      = 0x13B
	BtnMode       = 0x13C
	BtnThumbL     = 0x13D
	BtnThumbR     = 0x13E
	BtnDPadUp     = 0x220
	BtnDPadDown   = 0x221
	BtnDPadLeft   = 0x222
	BtnDPadRight  = 0x223
	BtnTouch      = 0x14A
	BtnToolFinger = 0x145
	KeyMax        = 0x2FF
)

const (
	FFRumble = 0x50
	FFGain   = 0x60
)

const (
	BusUSB       = 0x03
	BusBluetooth = 0x05
	BusVirtual   = 0x06
)

// Event is one input_event.
type Event struct {
	Type, Code uint16
	Value      int32
}

// EventSize is sizeof(struct input_event): a timeval, then type, code and
// value.
const EventSize = 2*(bits.UintSize/8) + 8

const timevalSize = 2 * (bits.UintSize / 8)

// Put writes e into b, which must hold EventSize bytes, with a zero time (the
// kernel stamps events written to uinput).
func (e Event) Put(b []byte) {
	clear(b[:timevalSize])
	binary.NativeEndian.PutUint16(b[timevalSize:], e.Type)
	binary.NativeEndian.PutUint16(b[timevalSize+2:], e.Code)
	binary.NativeEndian.PutUint32(b[timevalSize+4:], uint32(e.Value))
}

// ParseEvent reads the event at the start of b, which must hold EventSize
// bytes.
func ParseEvent(b []byte) Event {
	return Event{
		Type:  binary.NativeEndian.Uint16(b[timevalSize:]),
		Code:  binary.NativeEndian.Uint16(b[timevalSize+2:]),
		Value: int32(binary.NativeEndian.Uint32(b[timevalSize+4:])),
	}
}

// Encode appends the wire form of evs to dst.
func Encode(dst []byte, evs ...Event) []byte {
	for _, e := range evs {
		dst = append(dst, make([]byte, EventSize)...)
		e.Put(dst[len(dst)-EventSize:])
	}
	return dst
}

// ioctl request layout, shared by every architecture Planck supports.
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2

	iocNRShift   = 0
	iocTypeShift = 8
	iocSizeShift = 16
	iocDirShift  = 30
)

func ioc(dir, typ, nr, size uint32) uintptr {
	return uintptr(dir<<iocDirShift | size<<iocSizeShift | typ<<iocTypeShift | nr<<iocNRShift)
}

func IO(typ, nr uint32) uintptr         { return ioc(iocNone, typ, nr, 0) }
func IOR(typ, nr, size uint32) uintptr  { return ioc(iocRead, typ, nr, size) }
func IOW(typ, nr, size uint32) uintptr  { return ioc(iocWrite, typ, nr, size) }
func IOWR(typ, nr, size uint32) uintptr { return ioc(iocRead|iocWrite, typ, nr, size) }

const (
	InputIDSize   = 8
	AbsInfoSize   = 24
	evdevIoctlTyp = 'E'
)

// EVIOCGID reads struct input_id.
var EVIOCGID = IOR(evdevIoctlTyp, 0x02, InputIDSize)

// EVIOCGNAME reads the device name into a buffer of n bytes.
func EVIOCGNAME(n int) uintptr { return IOR(evdevIoctlTyp, 0x06, uint32(n)) }

// EVIOCGBIT reads the bitmap of codes of event type ev (0 for the types
// themselves) into a buffer of n bytes.
func EVIOCGBIT(ev, n int) uintptr { return IOR(evdevIoctlTyp, 0x20+uint32(ev), uint32(n)) }

// EVIOCGKEY reads the pressed keys into a buffer of n bytes.
func EVIOCGKEY(n int) uintptr { return IOR(evdevIoctlTyp, 0x18, uint32(n)) }

// EVIOCGABS reads struct input_absinfo of axis abs.
func EVIOCGABS(abs int) uintptr { return IOR(evdevIoctlTyp, 0x40+uint32(abs), AbsInfoSize) }

// EVIOCRMFF erases a force-feedback effect by id.
var EVIOCRMFF = IOW(evdevIoctlTyp, 0x81, 4)

// EVIOCSFF uploads a force-feedback effect.
var EVIOCSFF = IOW(evdevIoctlTyp, 0x80, FFEffectSize)

// FFEffectSize is sizeof(struct ff_effect): fourteen bytes of header padded
// to the union, whose largest member (ff_periodic_effect) ends in a pointer.
const FFEffectSize = ffUnionOffset + ffUnionSize

const (
	ffUnionOffset = 16
	ffUnionSize   = 24 + bits.UintSize/8
)

// FFRumbleOffset is where struct ff_effect keeps its rumble magnitudes
// (strong, then weak, 16 bits each).
const FFRumbleOffset = ffUnionOffset

// TestBit reports whether bit n is set in a bitmap the kernel returned.
func TestBit(bm []byte, n int) bool {
	return n/8 < len(bm) && bm[n/8]&(1<<(n%8)) != 0
}
