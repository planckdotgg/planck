package input

import (
	"errors"
	"sync"

	"github.com/planckdotgg/planck/internal/linux/evdev"
	"github.com/planckdotgg/planck/internal/linux/uinput"
)

// uinputBackend writes events to three virtual devices: a keyboard, a
// relative mouse and an absolute pointer. Separate devices keep libinput
// from classifying a combined one as a tablet or touchscreen.
type uinputBackend struct {
	kbd, mouse, pointer *uinput.Device

	mu         sync.Mutex
	accX, accY wheelAccum
}

func openUinputBackend() (*uinputBackend, error) {
	if err := uinput.Available(); err != nil {
		return nil, err
	}
	b := &uinputBackend{}
	for _, d := range []struct {
		dst *(*uinput.Device)
		cfg uinput.Config
	}{
		{&b.kbd, keyboardConfig()}, {&b.mouse, mouseConfig()}, {&b.pointer, pointerConfig()},
	} {
		dev, err := uinput.Create(d.cfg, nil)
		if err != nil {
			b.close()
			return nil, err
		}
		*d.dst = dev
	}
	return b, nil
}

func (b *uinputBackend) key(code uint16, down bool) error {
	return b.kbd.Emit(evdev.Event{Type: evdev.EvKey, Code: code, Value: int32(boolByte(down))})
}

func (b *uinputBackend) moveRel(dx, dy int32) error {
	evs := relEvents(MouseMoveRel{DX: int16(dx), DY: int16(dy)})
	if len(evs) == 0 {
		return nil
	}
	return b.mouse.Emit(evs...)
}

func (b *uinputBackend) moveAbs(m MouseMoveAbs, display, desktop Rect) error {
	x, y := absTarget(m, display, desktop)
	return b.pointer.Emit(absEvents(x, y)...)
}

func (b *uinputBackend) button(code uint16, down bool) error {
	return b.mouse.Emit(evdev.Event{Type: evdev.EvKey, Code: code, Value: int32(boolByte(down))})
}

func (b *uinputBackend) wheel(w MouseWheel) error {
	b.mu.Lock()
	evs := wheelEvents(w, &b.accX, &b.accY)
	b.mu.Unlock()
	if len(evs) == 0 {
		return nil
	}
	return b.mouse.Emit(evs...)
}

func (b *uinputBackend) close() error {
	var errs []error
	for _, d := range []*uinput.Device{b.kbd, b.mouse, b.pointer} {
		if d != nil {
			errs = append(errs, d.Close())
		}
	}
	return errors.Join(errs...)
}
