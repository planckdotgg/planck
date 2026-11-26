package input

import (
	"errors"
	"fmt"
	"slices"

	"github.com/planckdotgg/planck/internal/linux/evdev"
	"github.com/planckdotgg/planck/internal/linux/uinput"
)

// evdevButton is the Linux button code of b.
func evdevButton(b Button) (uint16, bool) {
	switch b {
	case ButtonLeft:
		return evdev.BtnLeft, true
	case ButtonRight:
		return evdev.BtnRight, true
	case ButtonMiddle:
		return evdev.BtnMiddle, true
	case ButtonX1:
		return evdev.BtnSide, true
	case ButtonX2:
		return evdev.BtnExtra, true
	}
	return 0, false
}

var evdevButtons = []uint16{evdev.BtnLeft, evdev.BtnRight, evdev.BtnMiddle, evdev.BtnSide, evdev.BtnExtra}

// absRange is the extent of the absolute pointer's axes.
const absRange = 65535

func keyboardConfig() uinput.Config {
	keys := make([]uint16, 0, len(evdevKeys))
	for _, k := range evdevKeys {
		keys = append(keys, k.code)
	}
	slices.Sort(keys)
	return uinput.Config{Name: "Planck Virtual Keyboard", Bus: evdev.BusVirtual, Vendor: 0x504C, Product: 0x0001, Version: 1, Keys: keys}
}

func mouseConfig() uinput.Config {
	return uinput.Config{
		Name: "Planck Virtual Mouse", Bus: evdev.BusVirtual, Vendor: 0x504C, Product: 0x0002, Version: 1,
		Keys: evdevButtons,
		Rels: []uint16{evdev.RelX, evdev.RelY, evdev.RelWheel, evdev.RelHWheel, evdev.RelWheelHiRes, evdev.RelHWheelHiRes},
	}
}

func pointerConfig() uinput.Config {
	return uinput.Config{
		Name: "Planck Virtual Pointer", Bus: evdev.BusVirtual, Vendor: 0x504C, Product: 0x0003, Version: 1,
		Keys: evdevButtons,
		Abs: []uinput.AbsAxis{
			{Code: evdev.AbsX, Min: 0, Max: absRange},
			{Code: evdev.AbsY, Min: 0, Max: absRange},
		},
	}
}

func keyEvents(k KeyEvent) ([]evdev.Event, error) {
	code, ok := EvdevFromHID(k.Usage)
	if !ok {
		return nil, fmt.Errorf("%w: HID usage 0x%02x has no Linux key", ErrUnsupported, k.Usage)
	}
	return []evdev.Event{{Type: evdev.EvKey, Code: code, Value: int32(boolByte(k.Pressed))}}, nil
}

func relEvents(m MouseMoveRel) []evdev.Event {
	evs := make([]evdev.Event, 0, 2)
	if m.DX != 0 {
		evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelX, Value: int32(m.DX)})
	}
	if m.DY != 0 {
		evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelY, Value: int32(m.DY)})
	}
	return evs
}

// absTarget maps a pointer position over the streamed display onto the
// 0..absRange axes the pointer device spans across the whole desktop.
func absTarget(m MouseMoveAbs, display, desktop Rect) (x, y int32) {
	if display == (Rect{}) || desktop == (Rect{}) {
		return int32(m.X), int32(m.Y)
	}
	return virtualDeskAbs(m.X, display.X, display.W, desktop.X, desktop.W),
		virtualDeskAbs(m.Y, display.Y, display.H, desktop.Y, desktop.H)
}

func absEvents(x, y int32) []evdev.Event {
	return []evdev.Event{
		{Type: evdev.EvAbs, Code: evdev.AbsX, Value: x},
		{Type: evdev.EvAbs, Code: evdev.AbsY, Value: y},
	}
}

func buttonEvents(b MouseButton) ([]evdev.Event, error) {
	code, ok := evdevButton(b.Button)
	if !ok {
		return nil, fmt.Errorf("%w: mouse button %d", ErrInvalid, b.Button)
	}
	return []evdev.Event{{Type: evdev.EvKey, Code: code, Value: int32(boolByte(b.Pressed))}}, nil
}

// wheelEvents carries a scroll in the high-resolution axes, which the
// kernel defines as 120 per detent like WheelNotch, and in the legacy
// whole-line axes through the accumulators, as a mouse driver does.
func wheelEvents(w MouseWheel, accX, accY *wheelAccum) []evdev.Event {
	var evs []evdev.Event
	if w.DY != 0 {
		evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelWheelHiRes, Value: int32(w.DY)})
		if n := accY.lines(w.DY); n != 0 {
			evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelWheel, Value: n})
		}
	}
	if w.DX != 0 {
		evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelHWheelHiRes, Value: int32(w.DX)})
		if n := accX.lines(w.DX); n != 0 {
			evs = append(evs, evdev.Event{Type: evdev.EvRel, Code: evdev.RelHWheel, Value: n})
		}
	}
	return evs
}

// Backend names of the Linux injector.
const (
	BackendUinput = "uinput"
	BackendXTest  = "xtest"
	BackendPortal = "portal"
)

// injectEnv is what backend selection reads from the session.
type injectEnv struct {
	forced      string // PLANCK_INJECT_BACKEND
	display     string // DISPLAY
	wayland     string // WAYLAND_DISPLAY
	sessionType string // XDG_SESSION_TYPE
}

// backendOrder is the backends to try, best first. uinput leads because it
// works under X11 and Wayland alike and needs no consent dialog. XTest only
// reaches X clients, so a Wayland session, where DISPLAY is just XWayland,
// falls back to the RemoteDesktop portal instead.
func backendOrder(e injectEnv) []string {
	if e.forced != "" {
		return []string{e.forced}
	}
	order := []string{BackendUinput}
	onWayland := e.wayland != "" || e.sessionType == "wayland"
	if !onWayland && e.display != "" {
		order = append(order, BackendXTest)
	}
	if onWayland {
		order = append(order, BackendPortal)
	}
	return order
}

// pickBackend opens the first of order that opens, and reports why each
// earlier one did not.
func pickBackend[B any](order []string, open map[string]func() (B, error)) (B, string, error) {
	var zero B
	var errs []error
	for _, name := range order {
		f, ok := open[name]
		if !ok {
			errs = append(errs, fmt.Errorf("%s: unknown backend", name))
			continue
		}
		b, err := f()
		if err == nil {
			return b, name, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
	}
	return zero, "", fmt.Errorf("%w: no input injection backend: %w", ErrUnsupported, joinErrs(errs))
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return fmt.Errorf("none to try")
	}
	return errors.Join(errs...)
}
