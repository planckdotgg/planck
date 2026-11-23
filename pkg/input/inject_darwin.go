//go:build darwin

package input

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ebitengine/purego"

	"github.com/watzon/planck/internal/apple"
)

type cgPoint struct{ X, Y float64 }

type cgRect struct {
	Origin cgPoint
	Size   struct{ W, H float64 }
}

var (
	cgOnce sync.Once
	cgErr  error

	axIsProcessTrusted             func() bool
	cgEventSourceCreate            func(state int32) uintptr
	cgEventCreate                  func(source uintptr) uintptr
	cgEventGetLocation             func(ev uintptr) cgPoint
	cgEventCreateKeyboardEvent     func(source uintptr, key uint16, down bool) uintptr
	cgEventCreateMouseEvent        func(source uintptr, typ uint32, at cgPoint, button uint32) uintptr
	cgEventCreateScrollWheelEvent2 func(source uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr
	cgEventSetFlags                func(ev uintptr, flags uint64)
	cgEventSetIntegerValueField    func(ev uintptr, field uint32, v int64)
	cgEventPost                    func(tap uint32, ev uintptr)
	cgMainDisplayID                func() uint32
	cgDisplayBounds                func(display uint32) cgRect
)

func loadCG() {
	as, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		cgErr = fmt.Errorf("input: %w", err)
		return
	}
	apple.Register(&axIsProcessTrusted, as, "AXIsProcessTrusted")
	cg := apple.CoreGraphics
	apple.Register(&cgEventSourceCreate, cg, "CGEventSourceCreate")
	apple.Register(&cgEventCreate, cg, "CGEventCreate")
	apple.Register(&cgEventGetLocation, cg, "CGEventGetLocation")
	apple.Register(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
	apple.Register(&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
	apple.Register(&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2")
	apple.Register(&cgEventSetFlags, cg, "CGEventSetFlags")
	apple.Register(&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField")
	apple.Register(&cgEventPost, cg, "CGEventPost")
	apple.Register(&cgMainDisplayID, cg, "CGMainDisplayID")
	apple.Register(&cgDisplayBounds, cg, "CGDisplayBounds")
}

const (
	hidSystemState = 1
	hidEventTap    = 0

	evLeftDown     = 1
	evLeftUp       = 2
	evRightDown    = 3
	evRightUp      = 4
	evMoved        = 5
	evLeftDragged  = 6
	evRightDragged = 7
	evOtherDown    = 25
	evOtherUp      = 26
	evOtherDragged = 27

	fieldClickState = 1
	fieldButton     = 3
	fieldDeltaX     = 4
	fieldDeltaY     = 5

	scrollUnitLine = 1
)

// ErrNotTrusted means the process lacks the Accessibility permission, without
// which macOS silently drops posted events.
var ErrNotTrusted = errors.New("input: Accessibility permission not granted")

// macFlags converts HID modifier bits to CGEventFlags, with the device-side
// bits that tell left from right.
func macFlags(mods uint8) uint64 {
	var f uint64
	side := []struct {
		mod          uint8
		generic, dev uint64
	}{
		{ModLeftControl, 0x40000, 0x0001}, {ModRightControl, 0x40000, 0x2000},
		{ModLeftShift, 0x20000, 0x0002}, {ModRightShift, 0x20000, 0x0004},
		{ModLeftGUI, 0x100000, 0x0008}, {ModRightGUI, 0x100000, 0x0010},
		{ModLeftAlt, 0x80000, 0x0020}, {ModRightAlt, 0x80000, 0x0040},
	}
	for _, s := range side {
		if mods&s.mod != 0 {
			f |= s.generic | s.dev
		}
	}
	return f
}

// CGEventInjector posts keyboard and mouse events with CoreGraphics.
// Absolute moves and pointer clamping cover the main display. Gamepads
// return ErrUnsupported.
type CGEventInjector struct {
	source uintptr
	held   [ButtonX2 + 1]bool
	clicks clickTracker
	wheelX wheelAccum
	wheelY wheelAccum
}

func NewInjector() (*CGEventInjector, error) {
	cgOnce.Do(loadCG)
	if cgErr != nil {
		return nil, cgErr
	}
	if !axIsProcessTrusted() {
		return nil, ErrNotTrusted
	}
	src := cgEventSourceCreate(hidSystemState)
	if src == 0 {
		return nil, errors.New("input: CGEventSourceCreate failed")
	}
	return &CGEventInjector{source: src}, nil
}

func mainDisplayBounds() cgRect { return cgDisplayBounds(cgMainDisplayID()) }

func cursorLocation() cgPoint {
	ev := cgEventCreate(0)
	if ev == 0 {
		return cgPoint{}
	}
	defer apple.CFRelease(ev)
	return cgEventGetLocation(ev)
}

func (c *CGEventInjector) post(ev uintptr) error {
	if ev == 0 {
		return errors.New("input: CGEvent creation failed")
	}
	cgEventPost(hidEventTap, ev)
	apple.CFRelease(ev)
	return nil
}

// moveType is the event a pointer move must carry while a button is held, so
// drags register as drags.
func (c *CGEventInjector) moveType() (uint32, uint32) {
	switch {
	case c.held[ButtonLeft]:
		return evLeftDragged, 0
	case c.held[ButtonRight]:
		return evRightDragged, 1
	case c.held[ButtonMiddle]:
		return evOtherDragged, 2
	case c.held[ButtonX1]:
		return evOtherDragged, 3
	case c.held[ButtonX2]:
		return evOtherDragged, 4
	}
	return evMoved, 0
}

func (c *CGEventInjector) move(at cgPoint, dx, dy int64) error {
	typ, button := c.moveType()
	ev := cgEventCreateMouseEvent(c.source, typ, at, button)
	if ev != 0 {
		cgEventSetIntegerValueField(ev, fieldDeltaX, dx)
		cgEventSetIntegerValueField(ev, fieldDeltaY, dy)
	}
	return c.post(ev)
}

func (c *CGEventInjector) button(b MouseButton) error {
	at := cursorLocation()
	var typ, button uint32
	switch b.Button {
	case ButtonLeft:
		typ, button = evLeftUp, 0
		if b.Pressed {
			typ = evLeftDown
		}
	case ButtonRight:
		typ, button = evRightUp, 1
		if b.Pressed {
			typ = evRightDown
		}
	default:
		typ, button = evOtherUp, uint32(b.Button)-1
		if b.Pressed {
			typ = evOtherDown
		}
	}
	count := c.clicks.count
	if b.Pressed {
		count = c.clicks.press(b.Button, time.Now(), at.X, at.Y)
	}
	c.held[b.Button] = b.Pressed
	ev := cgEventCreateMouseEvent(c.source, typ, at, button)
	if ev != 0 {
		cgEventSetIntegerValueField(ev, fieldButton, int64(button))
		cgEventSetIntegerValueField(ev, fieldClickState, count)
	}
	return c.post(ev)
}

func (c *CGEventInjector) Inject(ev Event) error {
	if err := ev.validate(); err != nil {
		return err
	}
	switch ev.Type {
	case TypeKey:
		code, ok := MacFromHID(ev.Key.Usage)
		if !ok {
			return fmt.Errorf("%w: HID usage 0x%02x", ErrUnsupported, ev.Key.Usage)
		}
		e := cgEventCreateKeyboardEvent(c.source, code, ev.Key.Pressed)
		if e != 0 {
			cgEventSetFlags(e, macFlags(ev.Key.Modifiers))
		}
		return c.post(e)
	case TypeMouseMoveAbs:
		b := mainDisplayBounds()
		at := cgPoint{scaleAbs(ev.MouseAbs.X, b.Origin.X, b.Size.W), scaleAbs(ev.MouseAbs.Y, b.Origin.Y, b.Size.H)}
		return c.move(at, 0, 0)
	case TypeMouseMoveRel:
		b := mainDisplayBounds()
		at := cursorLocation()
		at.X = clamp(at.X+float64(ev.MouseRel.DX), b.Origin.X, b.Origin.X+b.Size.W-1)
		at.Y = clamp(at.Y+float64(ev.MouseRel.DY), b.Origin.Y, b.Origin.Y+b.Size.H-1)
		return c.move(at, int64(ev.MouseRel.DX), int64(ev.MouseRel.DY))
	case TypeMouseButton:
		return c.button(ev.Button)
	case TypeMouseWheel:
		dy, dx := c.wheelY.lines(ev.Wheel.DY), c.wheelX.lines(ev.Wheel.DX)
		if dx == 0 && dy == 0 {
			return nil
		}
		// Quartz wheel 2 counts positive to the left; MouseWheel.DX to the right.
		return c.post(cgEventCreateScrollWheelEvent2(c.source, scrollUnitLine, 2, dy, -dx, 0))
	case TypeGamepad, TypeRumble:
		return ErrUnsupported
	}
	return fmt.Errorf("%w: type %d", ErrInvalid, ev.Type)
}

func (c *CGEventInjector) Close() error {
	if c.source != 0 {
		apple.CFRelease(c.source)
		c.source = 0
	}
	return nil
}

var _ Injector = (*CGEventInjector)(nil)
