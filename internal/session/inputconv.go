package session

import (
	"fmt"

	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/wire"
)

// modSystemKeys carries KeyEvent.SystemKeyCapture above the 8 modifier bits
// in wire.Input.Mods.
const modSystemKeys = 1 << 8

// toWire maps an input event onto the media datagram layout. Rumble travels
// host to client and has no input datagram.
func toWire(ev input.Event) (wire.Input, error) {
	var in wire.Input
	switch ev.Type {
	case input.TypeKey:
		in.Type, in.Code, in.Down, in.Mods = wire.InputKey, ev.Key.Usage, ev.Key.Pressed, uint16(ev.Key.Modifiers)
		if ev.Key.SystemKeyCapture {
			in.Mods |= modSystemKeys
		}
	case input.TypeMouseMoveAbs:
		in.Type, in.X, in.Y = wire.InputMouseAbs, int32(ev.MouseAbs.X), int32(ev.MouseAbs.Y)
	case input.TypeMouseMoveRel:
		in.Type, in.X, in.Y = wire.InputMouseRel, int32(ev.MouseRel.DX), int32(ev.MouseRel.DY)
	case input.TypeMouseButton:
		in.Type, in.Button, in.Down = wire.InputMouseButton, uint8(ev.Button.Button), ev.Button.Pressed
	case input.TypeMouseWheel:
		in.Type, in.X, in.Y = wire.InputScroll, int32(ev.Wheel.DX), int32(ev.Wheel.DY)
	case input.TypeGamepad:
		g := ev.Gamepad
		in = wire.Input{Type: wire.InputGamepad, Pad: g.Index, Buttons: g.Buttons, LT: g.LeftTrigger, RT: g.RightTrigger,
			LX: g.LeftX, LY: g.LeftY, RX: g.RightX, RY: g.RightY}
	default:
		return in, fmt.Errorf("session: input type %d cannot be sent to the host", ev.Type)
	}
	return in, nil
}

// fromWire maps a received datagram back to an event and validates it, so a
// malformed peer value never reaches an injector.
func fromWire(in wire.Input) (input.Event, error) {
	var ev input.Event
	switch in.Type {
	case wire.InputKey:
		if in.Mods&^(0xff|modSystemKeys) != 0 {
			return ev, fmt.Errorf("%w: key mods 0x%04x", input.ErrInvalid, in.Mods)
		}
		ev = input.Event{Type: input.TypeKey, Key: input.KeyEvent{Usage: in.Code, Pressed: in.Down,
			Modifiers: uint8(in.Mods), SystemKeyCapture: in.Mods&modSystemKeys != 0}}
	case wire.InputMouseAbs:
		if in.X < 0 || in.X > 0xffff || in.Y < 0 || in.Y > 0xffff {
			return ev, fmt.Errorf("%w: absolute position %d,%d", input.ErrInvalid, in.X, in.Y)
		}
		ev = input.Event{Type: input.TypeMouseMoveAbs, MouseAbs: input.MouseMoveAbs{X: uint16(in.X), Y: uint16(in.Y)}}
	case wire.InputMouseRel:
		ev = input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: clamp16(in.X), DY: clamp16(in.Y)}}
	case wire.InputMouseButton:
		ev = input.Event{Type: input.TypeMouseButton, Button: input.MouseButton{Button: input.Button(in.Button), Pressed: in.Down}}
	case wire.InputScroll:
		ev = input.Event{Type: input.TypeMouseWheel, Wheel: input.MouseWheel{DX: clamp16(in.X), DY: clamp16(in.Y)}}
	case wire.InputGamepad:
		ev = input.Event{Type: input.TypeGamepad, Gamepad: input.GamepadState{Index: in.Pad, Buttons: in.Buttons,
			LeftTrigger: in.LT, RightTrigger: in.RT, LeftX: in.LX, LeftY: in.LY, RightX: in.RX, RightY: in.RY}}
	default:
		return ev, fmt.Errorf("%w: wire input type %d", input.ErrInvalid, in.Type)
	}
	var buf [input.MaxEventSize]byte
	if _, err := ev.Encode(buf[:]); err != nil {
		return input.Event{}, err
	}
	return ev, nil
}

func clamp16(v int32) int16 { return int16(max(-32768, min(32767, v))) }
