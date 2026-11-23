//go:build windows

package input

import (
	"fmt"
	"syscall"
	"unsafe"
)

var procSendInput = syscall.NewLazyDLL("user32.dll").NewProc("SendInput")

const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfScanCode    = 0x0008

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfXDown      = 0x0080
	mouseeventfXUp        = 0x0100
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x1000
	mouseeventfAbsolute   = 0x8000

	xbutton1 = 1
	xbutton2 = 2
)

type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type keybdInput struct {
	vk, scan  uint16
	flags     uint32
	time      uint32
	extraInfo uintptr
}

// inputRecord is the Win32 INPUT struct: a type tag, then a union whose
// largest member is MOUSEINPUT (40 bytes in all on amd64).
type inputRecord struct {
	typ uint32
	_   uint32
	mi  mouseInput
}

// SendInputInjector injects keyboard and mouse input with SendInput.
// Absolute moves cover the primary monitor. Gamepads need a virtual
// controller driver (ViGEmBus) that is not wired in yet, so they return
// ErrUnsupported.
type SendInputInjector struct {
	in inputRecord
}

func NewInjector() (*SendInputInjector, error) {
	if err := procSendInput.Find(); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	return &SendInputInjector{}, nil
}

func (s *SendInputInjector) send() error {
	r, _, errno := syscall.SyscallN(procSendInput.Addr(), 1, uintptr(unsafe.Pointer(&s.in)), unsafe.Sizeof(s.in))
	if r != 1 {
		return fmt.Errorf("input: SendInput: %w", errno)
	}
	return nil
}

func (s *SendInputInjector) mouse(dx, dy int32, data, flags uint32) error {
	s.in = inputRecord{typ: inputMouse, mi: mouseInput{dx: dx, dy: dy, mouseData: data, flags: flags}}
	return s.send()
}

func (s *SendInputInjector) key(k KeyEvent) error {
	scan, ext, vk, ok := windowsScan(k.Usage)
	if !ok {
		return fmt.Errorf("%w: HID usage 0x%02x", ErrUnsupported, k.Usage)
	}
	s.in = inputRecord{typ: inputKeyboard}
	ki := (*keybdInput)(unsafe.Pointer(&s.in.mi))
	if scan != 0 {
		ki.scan, ki.flags = uint16(scan), keyeventfScanCode
	} else {
		ki.vk = uint16(vk)
	}
	if ext {
		ki.flags |= keyeventfExtendedKey
	}
	if !k.Pressed {
		ki.flags |= keyeventfKeyUp
	}
	return s.send()
}

func buttonFlags(b MouseButton) (flags, data uint32) {
	down := b.Pressed
	pick := func(d, u uint32) uint32 {
		if down {
			return d
		}
		return u
	}
	switch b.Button {
	case ButtonLeft:
		return pick(mouseeventfLeftDown, mouseeventfLeftUp), 0
	case ButtonRight:
		return pick(mouseeventfRightDown, mouseeventfRightUp), 0
	case ButtonMiddle:
		return pick(mouseeventfMiddleDown, mouseeventfMiddleUp), 0
	case ButtonX1:
		return pick(mouseeventfXDown, mouseeventfXUp), xbutton1
	default:
		return pick(mouseeventfXDown, mouseeventfXUp), xbutton2
	}
}

func (s *SendInputInjector) Inject(ev Event) error {
	if err := ev.validate(); err != nil {
		return err
	}
	switch ev.Type {
	case TypeKey:
		return s.key(ev.Key)
	case TypeMouseMoveAbs:
		return s.mouse(int32(ev.MouseAbs.X), int32(ev.MouseAbs.Y), 0, mouseeventfMove|mouseeventfAbsolute)
	case TypeMouseMoveRel:
		return s.mouse(int32(ev.MouseRel.DX), int32(ev.MouseRel.DY), 0, mouseeventfMove)
	case TypeMouseButton:
		flags, data := buttonFlags(ev.Button)
		return s.mouse(0, 0, data, flags)
	case TypeMouseWheel:
		if ev.Wheel.DY != 0 {
			if err := s.mouse(0, 0, uint32(int32(ev.Wheel.DY)), mouseeventfWheel); err != nil {
				return err
			}
		}
		if ev.Wheel.DX != 0 {
			return s.mouse(0, 0, uint32(int32(ev.Wheel.DX)), mouseeventfHWheel)
		}
		return nil
	case TypeGamepad, TypeRumble:
		return ErrUnsupported
	}
	return fmt.Errorf("%w: type %d", ErrInvalid, ev.Type)
}

func (s *SendInputInjector) Close() error { return nil }

var _ Injector = (*SendInputInjector)(nil)
