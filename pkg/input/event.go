// Package input carries keyboard, mouse, and gamepad input from the client
// to the host: a fixed binary wire format, key code tables, and host
// injectors.
package input

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/planckdotgg/planck/pkg/gamepad"
)

type Type uint8

const (
	TypeKey Type = iota + 1
	TypeMouseMoveAbs
	TypeMouseMoveRel
	TypeMouseButton
	TypeMouseWheel
	TypeGamepad
	TypeRumble
)

// KeyEvent is one key going down or up.
type KeyEvent struct {
	Usage     uint16 // HID usage, page 0x07
	Pressed   bool
	Modifiers uint8 // Mod* bits held once this event has taken effect
	// SystemKeyCapture is set when the client captured a key combination
	// its own OS would otherwise act on (Cmd-Tab, Alt-Tab, the Windows key).
	SystemKeyCapture bool
}

// MouseMoveAbs is a pointer position normalized over the streamed display:
// 0 is the left or top edge and 65535 the right or bottom edge.
type MouseMoveAbs struct{ X, Y uint16 }

type MouseMoveRel struct{ DX, DY int16 }

type Button uint8

const (
	ButtonLeft Button = iota + 1
	ButtonRight
	ButtonMiddle
	ButtonX1
	ButtonX2
)

type MouseButton struct {
	Button  Button
	Pressed bool
}

// WheelNotch is one detent of a standard mouse wheel; finer values come from
// high-resolution wheels and trackpads.
const WheelNotch = 120

// MouseWheel scrolls in WheelNotch units; positive DY scrolls up (away from
// the user) and positive DX scrolls right.
type MouseWheel struct{ DX, DY int16 }

// Gamepad buttons use the XInput bit layout, plus Guide.
const (
	PadDPadUp        = gamepad.ButtonDPadUp
	PadDPadDown      = gamepad.ButtonDPadDown
	PadDPadLeft      = gamepad.ButtonDPadLeft
	PadDPadRight     = gamepad.ButtonDPadRight
	PadStart         = gamepad.ButtonStart
	PadBack          = gamepad.ButtonBack
	PadLeftThumb     = gamepad.ButtonLeftThumb
	PadRightThumb    = gamepad.ButtonRightThumb
	PadLeftShoulder  = gamepad.ButtonLeftShoulder
	PadRightShoulder = gamepad.ButtonRightShoulder
	PadGuide         = gamepad.ButtonGuide
	PadA             = gamepad.ButtonA
	PadB             = gamepad.ButtonB
	PadX             = gamepad.ButtonX
	PadY             = gamepad.ButtonY
)

const MaxGamepads = 16

// GamepadState is the full state of one controller. Stick axes are signed
// with positive X right and positive Y up, as in XInput.
type GamepadState struct {
	Index                     uint8
	Buttons                   uint32
	LeftTrigger, RightTrigger uint8
	LeftX, LeftY              int16
	RightX, RightY            int16
}

// RumbleEvent travels host to client: motor strengths for one controller.
type RumbleEvent struct {
	Index             uint8
	LowFreq, HighFreq uint16
	DurationMs        uint16
}

// Event is one input event of any Type. Only the field matching Type is
// meaningful; a tagged struct rather than an interface keeps encoding and
// decoding free of allocations.
type Event struct {
	Type     Type
	Key      KeyEvent
	MouseAbs MouseMoveAbs
	MouseRel MouseMoveRel
	Button   MouseButton
	Wheel    MouseWheel
	Gamepad  GamepadState
	Rumble   RumbleEvent
}

// MaxEventSize is the largest encoded event in bytes.
const MaxEventSize = 16

var (
	ErrShortBuffer = errors.New("input: short buffer")
	ErrInvalid     = errors.New("input: invalid event")
)

// payloadSize is the encoded size after the type byte.
func payloadSize(t Type) int {
	switch t {
	case TypeKey, TypeMouseMoveAbs, TypeMouseMoveRel, TypeMouseWheel:
		return 4
	case TypeMouseButton:
		return 2
	case TypeGamepad:
		return 15
	case TypeRumble:
		return 7
	}
	return -1
}

const (
	keyFlagPressed    = 1 << 0
	keyFlagSystemKeys = 1 << 1
)

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (e *Event) validate() error {
	switch e.Type {
	case TypeMouseButton:
		if e.Button.Button < ButtonLeft || e.Button.Button > ButtonX2 {
			return fmt.Errorf("%w: mouse button %d", ErrInvalid, e.Button.Button)
		}
	case TypeGamepad:
		if e.Gamepad.Index >= MaxGamepads {
			return fmt.Errorf("%w: gamepad index %d", ErrInvalid, e.Gamepad.Index)
		}
	case TypeRumble:
		if e.Rumble.Index >= MaxGamepads {
			return fmt.Errorf("%w: gamepad index %d", ErrInvalid, e.Rumble.Index)
		}
	}
	return nil
}

// Encode writes e to dst in little-endian wire form and returns the bytes
// written: one type byte, then the payload.
func (e *Event) Encode(dst []byte) (int, error) {
	size := payloadSize(e.Type)
	if size < 0 {
		return 0, fmt.Errorf("%w: type %d", ErrInvalid, e.Type)
	}
	if err := e.validate(); err != nil {
		return 0, err
	}
	if len(dst) < 1+size {
		return 0, ErrShortBuffer
	}
	dst[0] = byte(e.Type)
	p := dst[1 : 1+size]
	le := binary.LittleEndian
	switch e.Type {
	case TypeKey:
		le.PutUint16(p, e.Key.Usage)
		p[2] = boolByte(e.Key.Pressed)*keyFlagPressed | boolByte(e.Key.SystemKeyCapture)*keyFlagSystemKeys
		p[3] = e.Key.Modifiers
	case TypeMouseMoveAbs:
		le.PutUint16(p, e.MouseAbs.X)
		le.PutUint16(p[2:], e.MouseAbs.Y)
	case TypeMouseMoveRel:
		le.PutUint16(p, uint16(e.MouseRel.DX))
		le.PutUint16(p[2:], uint16(e.MouseRel.DY))
	case TypeMouseButton:
		p[0] = byte(e.Button.Button)
		p[1] = boolByte(e.Button.Pressed)
	case TypeMouseWheel:
		le.PutUint16(p, uint16(e.Wheel.DX))
		le.PutUint16(p[2:], uint16(e.Wheel.DY))
	case TypeGamepad:
		g := &e.Gamepad
		p[0] = g.Index
		le.PutUint32(p[1:], g.Buttons)
		p[5], p[6] = g.LeftTrigger, g.RightTrigger
		le.PutUint16(p[7:], uint16(g.LeftX))
		le.PutUint16(p[9:], uint16(g.LeftY))
		le.PutUint16(p[11:], uint16(g.RightX))
		le.PutUint16(p[13:], uint16(g.RightY))
	case TypeRumble:
		r := &e.Rumble
		p[0] = r.Index
		le.PutUint16(p[1:], r.LowFreq)
		le.PutUint16(p[3:], r.HighFreq)
		le.PutUint16(p[5:], r.DurationMs)
	}
	return 1 + size, nil
}

// Decode reads one event from the start of src into e and returns the bytes
// consumed, so several events can share one datagram. It rejects unknown
// types, unknown flag bits, and out-of-range values.
func (e *Event) Decode(src []byte) (int, error) {
	if len(src) < 1 {
		return 0, ErrShortBuffer
	}
	t := Type(src[0])
	size := payloadSize(t)
	if size < 0 {
		return 0, fmt.Errorf("%w: type %d", ErrInvalid, src[0])
	}
	if len(src) < 1+size {
		return 0, ErrShortBuffer
	}
	p := src[1 : 1+size]
	le := binary.LittleEndian
	*e = Event{Type: t}
	switch t {
	case TypeKey:
		if p[2]&^(keyFlagPressed|keyFlagSystemKeys) != 0 {
			return 0, fmt.Errorf("%w: key flags 0x%02x", ErrInvalid, p[2])
		}
		e.Key = KeyEvent{Usage: le.Uint16(p), Pressed: p[2]&keyFlagPressed != 0,
			SystemKeyCapture: p[2]&keyFlagSystemKeys != 0, Modifiers: p[3]}
	case TypeMouseMoveAbs:
		e.MouseAbs = MouseMoveAbs{X: le.Uint16(p), Y: le.Uint16(p[2:])}
	case TypeMouseMoveRel:
		e.MouseRel = MouseMoveRel{DX: int16(le.Uint16(p)), DY: int16(le.Uint16(p[2:]))}
	case TypeMouseButton:
		if p[1] > 1 {
			return 0, fmt.Errorf("%w: button state %d", ErrInvalid, p[1])
		}
		e.Button = MouseButton{Button: Button(p[0]), Pressed: p[1] == 1}
	case TypeMouseWheel:
		e.Wheel = MouseWheel{DX: int16(le.Uint16(p)), DY: int16(le.Uint16(p[2:]))}
	case TypeGamepad:
		e.Gamepad = GamepadState{Index: p[0], Buttons: le.Uint32(p[1:]), LeftTrigger: p[5], RightTrigger: p[6],
			LeftX: int16(le.Uint16(p[7:])), LeftY: int16(le.Uint16(p[9:])),
			RightX: int16(le.Uint16(p[11:])), RightY: int16(le.Uint16(p[13:]))}
	case TypeRumble:
		e.Rumble = RumbleEvent{Index: p[0], LowFreq: le.Uint16(p[1:]), HighFreq: le.Uint16(p[3:]), DurationMs: le.Uint16(p[5:])}
	}
	if err := e.validate(); err != nil {
		return 0, err
	}
	return 1 + size, nil
}
