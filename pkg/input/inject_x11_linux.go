package input

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// xtestBackend posts input to an X server with the XTest extension, through
// libX11 and libXtst loaded at run time. The X keycode of a key is its evdev
// code plus 8, as on every Xorg and XWayland server.
type xtestBackend struct {
	mu         sync.Mutex
	dpy        uintptr
	screen     int32
	accX, accY wheelAccum

	closeDisplay func(dpy uintptr) int32
	flush        func(dpy uintptr) int32
	fakeKey      func(dpy uintptr, keycode uint32, press int32, delay uintptr) int32
	fakeButton   func(dpy uintptr, button uint32, press int32, delay uintptr) int32
	fakeMotion   func(dpy uintptr, screen, x, y int32, delay uintptr) int32
	fakeRelative func(dpy uintptr, dx, dy int32, delay uintptr) int32
	width        func(dpy uintptr, screen int32) int32
	height       func(dpy uintptr, screen int32) int32
}

func openXTestBackend() (*xtestBackend, error) {
	x11, err := purego.Dlopen("libX11.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("libX11: %w", err)
	}
	xtst, err := purego.Dlopen("libXtst.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("libXtst: %w", err)
	}
	b := &xtestBackend{}
	var openDisplay func(name *byte) uintptr
	var defaultScreen func(dpy uintptr) int32
	var queryExt func(dpy uintptr, event, errBase, major, minor *int32) int32
	for fn, name := range map[any]string{
		&openDisplay: "XOpenDisplay", &b.closeDisplay: "XCloseDisplay", &b.flush: "XFlush",
		&defaultScreen: "XDefaultScreen", &b.width: "XDisplayWidth", &b.height: "XDisplayHeight",
	} {
		if err := registerSym(fn, x11, name); err != nil {
			return nil, err
		}
	}
	for fn, name := range map[any]string{
		&queryExt: "XTestQueryExtension", &b.fakeKey: "XTestFakeKeyEvent", &b.fakeButton: "XTestFakeButtonEvent",
		&b.fakeMotion: "XTestFakeMotionEvent", &b.fakeRelative: "XTestFakeRelativeMotionEvent",
	} {
		if err := registerSym(fn, xtst, name); err != nil {
			return nil, err
		}
	}
	b.dpy = openDisplay(nil)
	if b.dpy == 0 {
		return nil, errors.New("XOpenDisplay failed: no X server at $DISPLAY")
	}
	var ev, er, major, minor int32
	if queryExt(b.dpy, &ev, &er, &major, &minor) == 0 {
		b.closeDisplay(b.dpy)
		return nil, errors.New("the X server has no XTEST extension")
	}
	b.screen = defaultScreen(b.dpy)
	return b, nil
}

func registerSym(fn any, lib uintptr, name string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("symbol %s: %v", name, r)
		}
	}()
	purego.RegisterLibFunc(fn, lib, name)
	return nil
}

const xKeycodeOffset = 8

func (b *xtestBackend) key(code uint16, down bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fakeKey(b.dpy, uint32(code)+xKeycodeOffset, int32(boolByte(down)), 0)
	b.flush(b.dpy)
	return nil
}

func (b *xtestBackend) moveRel(dx, dy int32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fakeRelative(b.dpy, dx, dy, 0)
	b.flush(b.dpy)
	return nil
}

func (b *xtestBackend) moveAbs(m MouseMoveAbs, display, desktop Rect) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if display == (Rect{}) {
		display = Rect{W: int(b.width(b.dpy, b.screen)), H: int(b.height(b.dpy, b.screen))}
	}
	x := int32(scaleAbs(m.X, float64(display.X), float64(display.W)) + 0.5)
	y := int32(scaleAbs(m.Y, float64(display.Y), float64(display.H)) + 0.5)
	b.fakeMotion(b.dpy, -1, x, y, 0)
	b.flush(b.dpy)
	return nil
}

// xButton is the core-protocol button number of an evdev button code.
func xButton(code uint16) uint32 {
	switch code {
	case 0x110:
		return 1
	case 0x112:
		return 2
	case 0x111:
		return 3
	case 0x113:
		return 8
	case 0x114:
		return 9
	}
	return 0
}

func (b *xtestBackend) button(code uint16, down bool) error {
	n := xButton(code)
	if n == 0 {
		return fmt.Errorf("%w: button code 0x%x", ErrUnsupported, code)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fakeButton(b.dpy, n, int32(boolByte(down)), 0)
	b.flush(b.dpy)
	return nil
}

// wheel clicks the scroll buttons 4 to 7, once per whole detent.
func (b *xtestBackend) wheel(w MouseWheel) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	click := func(up, down uint32, lines int32) {
		btn := up
		if lines < 0 {
			btn, lines = down, -lines
		}
		for ; lines > 0; lines-- {
			b.fakeButton(b.dpy, btn, 1, 0)
			b.fakeButton(b.dpy, btn, 0, 0)
		}
	}
	click(4, 5, b.accY.lines(w.DY))
	click(7, 6, b.accX.lines(w.DX))
	b.flush(b.dpy)
	return nil
}

func (b *xtestBackend) close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dpy != 0 {
		b.closeDisplay(b.dpy)
		b.dpy = 0
	}
	return nil
}
