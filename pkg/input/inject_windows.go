//go:build windows

package input

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/gamepad"
)

var procSendInput = syscall.NewLazyDLL("user32.dll").NewProc("SendInput")

const (
	inputMouse    = 0
	inputKeyboard = 1

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfScanCode    = 0x0008

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfXDown       = 0x0080
	mouseeventfXUp         = 0x0100
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfVirtualDesk = 0x4000
	mouseeventfAbsolute    = 0x8000

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
// Absolute moves cover the display SetTarget names, the primary one until
// then. Gamepads plug into ViGEmBus virtual controllers the first time
// each sends a state; without ViGEmBus they return ErrUnsupported.
type SendInputInjector struct {
	in inputRecord
	// post replaces SendInput in tests, which must never move the cursor.
	post func(*inputRecord) error

	mu               sync.Mutex
	display, desktop Rect

	padMu   sync.Mutex
	padType gamepad.PadType
	pads    *gamepad.VirtualPads
	padErr  error
}

// SetGamepadType sets what virtual controllers appear as; pads already
// plugged in keep their type until Gamepads().ResetSlots.
func (s *SendInputInjector) SetGamepadType(t gamepad.PadType) {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	s.padType = t
	if s.pads != nil {
		s.pads.SetType(t)
	}
}

// Gamepads opens the virtual controllers on first use, for slot
// management and rumble. A failed open is kept: ViGEmBus installed later
// takes a new injector.
func (s *SendInputInjector) Gamepads() (*gamepad.VirtualPads, error) {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	if s.pads == nil && s.padErr == nil {
		s.pads, s.padErr = gamepad.OpenVirtualPads(s.padType)
		if s.padErr != nil {
			s.padErr = fmt.Errorf("%w: %w", ErrUnsupported, s.padErr)
		}
	}
	return s.pads, s.padErr
}

func (s *SendInputInjector) gamepad(g GamepadState) error {
	pads, err := s.Gamepads()
	if err != nil {
		return err
	}
	return pads.Submit(g.Index, gamepad.State{Buttons: g.Buttons, LT: g.LeftTrigger, RT: g.RightTrigger,
		LX: g.LeftX, LY: g.LeftY, RX: g.RightX, RY: g.RightY})
}

// SetTarget makes absolute moves cover display, within the desktop of
// every display: SendInput then normalizes them across the virtual
// desktop (MOUSEEVENTF_VIRTUALDESK). A zero display goes back to the
// primary one. Safe from any goroutine.
func (s *SendInputInjector) SetTarget(display, desktop Rect) {
	s.mu.Lock()
	s.display, s.desktop = display, desktop
	s.mu.Unlock()
}

// absMove is the MOUSEINPUT of an absolute move to m.
func (s *SendInputInjector) absMove(m MouseMoveAbs) (dx, dy int32, flags uint32) {
	s.mu.Lock()
	d, v := s.display, s.desktop
	s.mu.Unlock()
	if d.W <= 0 || d.H <= 0 || v.W <= 0 || v.H <= 0 {
		return int32(m.X), int32(m.Y), mouseeventfMove | mouseeventfAbsolute
	}
	return virtualDeskAbs(m.X, d.X, d.W, v.X, v.W), virtualDeskAbs(m.Y, d.Y, d.H, v.Y, v.H),
		mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk
}

func NewInjector() (*SendInputInjector, error) {
	if err := procSendInput.Find(); err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	return &SendInputInjector{}, nil
}

func (s *SendInputInjector) send() error {
	if s.post != nil {
		return s.post(&s.in)
	}
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
	ki.extraInfo = InjectMarker
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
		dx, dy, flags := s.absMove(ev.MouseAbs)
		return s.mouse(dx, dy, 0, flags)
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
	case TypeGamepad:
		return s.gamepad(ev.Gamepad)
	case TypeRumble:
		return ErrUnsupported
	}
	return fmt.Errorf("%w: type %d", ErrInvalid, ev.Type)
}

// Close unplugs the virtual controllers.
func (s *SendInputInjector) Close() error {
	s.padMu.Lock()
	defer s.padMu.Unlock()
	if s.pads == nil {
		return nil
	}
	err := s.pads.Close()
	s.pads, s.padErr = nil, ErrUnsupported
	return err
}

var (
	_ Injector = (*SendInputInjector)(nil)
	_ Targeter = (*SendInputInjector)(nil)
)
