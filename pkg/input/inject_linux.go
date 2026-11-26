package input

import (
	"fmt"
	"os"
	"sync"

	"github.com/planckdotgg/planck/pkg/gamepad"
)

// injectBackend posts input to one Linux input stack. Buttons are evdev
// BTN_* codes and keys evdev KEY_* codes, so a backend never sees HID
// usages.
type injectBackend interface {
	key(code uint16, down bool) error
	moveRel(dx, dy int32) error
	// moveAbs places the pointer at m over display, within desktop; a zero
	// display covers the primary one.
	moveAbs(m MouseMoveAbs, display, desktop Rect) error
	button(code uint16, down bool) error
	// wheel scrolls in WheelNotch units, positive DY up and DX right.
	wheel(w MouseWheel) error
	close() error
}

// LinuxInjector injects keyboard and mouse input on a Linux host through
// /dev/uinput, X11 XTest or the RemoteDesktop portal, whichever opens
// first (see backendOrder), and plugs gamepads into virtual controllers.
type LinuxInjector struct {
	be   injectBackend
	name string

	mu               sync.Mutex
	display, desktop Rect

	padMu   sync.Mutex
	padType gamepad.PadType
	pads    *gamepad.VirtualPads
	padErr  error
}

// NewInjector opens the first injection backend that works. Set
// PLANCK_INJECT_BACKEND to uinput, xtest or portal to use only that one.
func NewInjector() (*LinuxInjector, error) {
	env := injectEnv{
		forced:      os.Getenv("PLANCK_INJECT_BACKEND"),
		display:     os.Getenv("DISPLAY"),
		wayland:     os.Getenv("WAYLAND_DISPLAY"),
		sessionType: os.Getenv("XDG_SESSION_TYPE"),
	}
	be, name, err := pickBackend(backendOrder(env), map[string]func() (injectBackend, error){
		BackendUinput: func() (injectBackend, error) { return openUinputBackend() },
		BackendXTest:  func() (injectBackend, error) { return openXTestBackend() },
		BackendPortal: func() (injectBackend, error) { return openPortalBackend() },
	})
	if err != nil {
		return nil, err
	}
	return &LinuxInjector{be: be, name: name}, nil
}

// Backend names the stack in use: BackendUinput, BackendXTest or
// BackendPortal.
func (l *LinuxInjector) Backend() string { return l.name }

// SetTarget makes absolute moves cover display, within the desktop of
// every display. A zero display goes back to the primary one.
func (l *LinuxInjector) SetTarget(display, desktop Rect) {
	l.mu.Lock()
	l.display, l.desktop = display, desktop
	l.mu.Unlock()
}

// SetGamepadType sets what virtual controllers appear as.
func (l *LinuxInjector) SetGamepadType(t gamepad.PadType) {
	l.padMu.Lock()
	defer l.padMu.Unlock()
	l.padType = t
	if l.pads != nil {
		l.pads.SetType(t)
	}
}

// Gamepads opens the virtual controllers on first use, for slot
// management and rumble. A failed open is kept.
func (l *LinuxInjector) Gamepads() (*gamepad.VirtualPads, error) {
	l.padMu.Lock()
	defer l.padMu.Unlock()
	if l.pads == nil && l.padErr == nil {
		l.pads, l.padErr = gamepad.OpenVirtualPads(l.padType)
		if l.padErr != nil {
			l.padErr = fmt.Errorf("%w: %w", ErrUnsupported, l.padErr)
		}
	}
	return l.pads, l.padErr
}

func (l *LinuxInjector) gamepad(g GamepadState) error {
	pads, err := l.Gamepads()
	if err != nil {
		return err
	}
	return pads.Submit(g.Index, gamepad.State{Buttons: g.Buttons, LT: g.LeftTrigger, RT: g.RightTrigger,
		LX: g.LeftX, LY: g.LeftY, RX: g.RightX, RY: g.RightY})
}

func (l *LinuxInjector) Inject(ev Event) error {
	if err := ev.validate(); err != nil {
		return err
	}
	switch ev.Type {
	case TypeKey:
		code, ok := EvdevFromHID(ev.Key.Usage)
		if !ok {
			return fmt.Errorf("%w: HID usage 0x%02x has no Linux key", ErrUnsupported, ev.Key.Usage)
		}
		return l.be.key(code, ev.Key.Pressed)
	case TypeMouseMoveAbs:
		l.mu.Lock()
		display, desktop := l.display, l.desktop
		l.mu.Unlock()
		return l.be.moveAbs(ev.MouseAbs, display, desktop)
	case TypeMouseMoveRel:
		return l.be.moveRel(int32(ev.MouseRel.DX), int32(ev.MouseRel.DY))
	case TypeMouseButton:
		code, ok := evdevButton(ev.Button.Button)
		if !ok {
			return fmt.Errorf("%w: mouse button %d", ErrInvalid, ev.Button.Button)
		}
		return l.be.button(code, ev.Button.Pressed)
	case TypeMouseWheel:
		return l.be.wheel(ev.Wheel)
	case TypeGamepad:
		return l.gamepad(ev.Gamepad)
	case TypeRumble:
		return ErrUnsupported
	}
	return fmt.Errorf("%w: type %d", ErrInvalid, ev.Type)
}

// Close destroys the virtual devices and closes the controllers.
func (l *LinuxInjector) Close() error {
	l.padMu.Lock()
	var perr error
	if l.pads != nil {
		perr = l.pads.Close()
		l.pads, l.padErr = nil, ErrUnsupported
	}
	l.padMu.Unlock()
	if err := l.be.close(); err != nil {
		return err
	}
	return perr
}

var (
	_ Injector = (*LinuxInjector)(nil)
	_ Targeter = (*LinuxInjector)(nil)
)
