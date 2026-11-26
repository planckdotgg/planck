package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo/gpusurface"
	"github.com/egoist/mygo/gpusurface/wl"

	"github.com/planckdotgg/planck/pkg/input"
)

// seat is the window's input as Linux reports it beside GTK, which the
// pointer, the side buttons and immersive mode's key tap read: on Wayland
// a wl_seat of its own on a private queue of GTK's connection, with the
// pointer constraints, relative pointer and shortcuts inhibit protocols;
// on X11 XInput 2 raw events on a connection of its own, and another for
// requests, each used by one thread.
type seat struct {
	native gpusurface.Native

	// Wayland.
	q                                     *wl.Queue
	compositor, wlSeat, pointer, keyboard wl.Proxy
	constraints, relManager, inhibitMgr   wl.Proxy
	relative                              wl.Proxy
	// held is the constraint while one holds the pointer; inhibitor the
	// shortcuts inhibitor while the tap runs.
	held, inhibitor wl.Proxy
	lockedPointer   bool
	// mods is the keyboard's depressed modifiers, as xkb's standard
	// indices number them.
	mods atomic.Uint32
	// keyFocus tells that the window has the keyboard.
	keyFocus atomic.Bool

	// X11.
	x *x11

	// What reads the input; set before it comes, read on the event
	// goroutine.
	motion  atomic.Pointer[func(dx, dy float64)]
	relOn   atomic.Bool
	buttons atomic.Uint32 // 1 << input.Button held
	side    atomic.Pointer[func(input.Button, bool)]
	keys    atomic.Pointer[func(evdev uint16, down bool)]
}

var seats struct {
	mu  sync.Mutex
	cur *seat
	win uintptr
}

// seatFor is the seat of win, a GtkWindow*, made the first time and
// anew when the window changes. Main thread.
func seatFor(win uintptr) (*seat, error) {
	seats.mu.Lock()
	defer seats.mu.Unlock()
	if seats.cur != nil && seats.win == win {
		return seats.cur, nil
	}
	n, err := gpusurface.NativeOf(win)
	if err != nil {
		return nil, err
	}
	s := &seat{native: n}
	if n.Wayland {
		err = s.openWayland()
	} else {
		err = s.openX11()
	}
	if err != nil {
		return nil, err
	}
	if seats.cur != nil {
		old := seats.cur
		// Its listeners move to the new seat.
		s.motion.Store(old.motion.Load())
		s.side.Store(old.side.Load())
		s.keys.Store(old.keys.Load())
		old.close()
	}
	seats.cur, seats.win = s, win
	return s, nil
}

// Linux input event codes (input-event-codes.h) of the mouse buttons.
const (
	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112
	btnSide   = 0x113
	btnExtra  = 0x114
)

func buttonOfEvdev(code uint32) (input.Button, bool) {
	switch code {
	case btnLeft:
		return input.ButtonLeft, true
	case btnRight:
		return input.ButtonRight, true
	case btnMiddle:
		return input.ButtonMiddle, true
	case btnSide:
		return input.ButtonX1, true
	case btnExtra:
		return input.ButtonX2, true
	}
	return 0, false
}

// button records b's state and passes side buttons on.
func (s *seat) button(b input.Button, down bool) {
	bit := uint32(1) << b
	if down {
		s.buttons.Or(bit)
	} else {
		s.buttons.And(^bit)
	}
	if b == input.ButtonX1 || b == input.ButtonX2 {
		if f := s.side.Load(); f != nil {
			(*f)(b, down)
		}
	}
}

func (s *seat) openWayland() error {
	q, err := wl.NewQueue(s.native.WLDisplay)
	if err != nil {
		return err
	}
	s.q = q
	s.compositor, _ = q.Bind(wl.Core("wl_compositor"), 4)
	s.constraints, _ = q.Bind(wl.ZwpPointerConstraintsV1, 1)
	s.relManager, _ = q.Bind(wl.ZwpRelativePointerManagerV1, 1)
	s.inhibitMgr, _ = q.Bind(wl.ZwpKeyboardShortcutsInhibitManagerV1, 1)
	var caps atomic.Uint32
	s.wlSeat, _ = q.Bind(wl.Core("wl_seat"), 5)
	if s.wlSeat == 0 {
		s.close()
		return errors.New("no wl_seat")
	}
	s.wlSeat.Listen(func(op uint32, e wl.Event) {
		if op == 0 { // capabilities
			caps.Store(e.Uint(0))
		}
	})
	if err := q.Roundtrip(); err != nil {
		s.close()
		return err
	}
	// A request for a device the seat lacks is a protocol error, which
	// would end GTK's connection too.
	if caps.Load()&1 != 0 {
		s.pointer = s.wlSeat.Marshal(wl.SeatGetPointer, wl.Core("wl_pointer"), s.wlSeat.Version(), 0)
		s.pointer.Listen(s.pointerEvent)
		if s.relManager != 0 {
			s.relative = s.relManager.Marshal(wl.RelativeGetPointer, wl.ZwpRelativePointerV1, 1, 0, uintptr(s.pointer))
			s.relative.Listen(func(op uint32, e wl.Event) {
				if op != 0 || !s.relOn.Load() {
					return
				}
				// relative_motion: utime_hi, utime_lo, dx, dy, dx_unaccel, dy_unaccel.
				if f := s.motion.Load(); f != nil {
					(*f)(e.Fixed(4), e.Fixed(5))
				}
			})
		}
	}
	if caps.Load()&2 != 0 {
		s.keyboard = s.wlSeat.Marshal(wl.SeatGetKeyboard, wl.Core("wl_keyboard"), s.wlSeat.Version(), 0)
		s.keyboard.Listen(s.keyboardEvent)
	}
	go q.Run()
	return nil
}

func (s *seat) pointerEvent(op uint32, e wl.Event) {
	if op == 3 { // button: serial, time, button, state
		if b, ok := buttonOfEvdev(e.Uint(2)); ok {
			s.button(b, e.Uint(3) == 1)
		}
	}
	if op == 1 { // leave: buttons held go up wherever they are let go
		s.buttons.Store(0)
	}
}

func (s *seat) keyboardEvent(op uint32, e wl.Event) {
	switch op {
	case 0: // keymap: format, fd, size
		syscall.Close(e.FD(1))
	case 1: // enter
		s.keyFocus.Store(true)
	case 2: // leave
		s.keyFocus.Store(false)
	case 3: // key: serial, time, key, state
		if f := s.keys.Load(); f != nil && s.keyFocus.Load() {
			(*f)(uint16(e.Uint(2)), e.Uint(3) == 1)
		}
	case 4: // modifiers: serial, depressed, latched, locked, group
		s.mods.Store(e.Uint(1))
	}
}

// Standard xkb modifier indices of the depressed mask.
const (
	xkbShift   = 1 << 0
	xkbControl = 1 << 2
	xkbMod1    = 1 << 3
)

// surfaceRect is r, in the content's DIPs, in the toplevel surface's
// coordinates.
func (s *seat) surfaceRect(r rect) (x, y, w, h int) {
	return s.native.AreaX + int(r.X+0.5), s.native.AreaY + int(r.Y+0.5), max(int(r.W+0.5), 1), max(int(r.H+0.5), 1)
}

// waylandHold locks or confines the pointer to the toplevel surface.
func (s *seat) waylandHold(m pointerMode, area rect) {
	s.waylandFree()
	if s.constraints == 0 || s.pointer == 0 {
		return
	}
	surf := s.native.WLSurface
	x, y, w, h := s.surfaceRect(area)
	if m == pointerRelative {
		s.held = s.constraints.Marshal(wl.ConstraintsLockPointer, wl.ZwpLockedPointerV1, 1, 0, surf, uintptr(s.pointer), 0, wl.LifetimePersistent)
		s.held.Marshal(wl.LockedSetCursorHint, nil, 0, wl.Fixed(float64(x+w/2)), wl.Fixed(float64(y+h/2)))
		s.lockedPointer = true
	} else {
		region := s.compositor.Marshal(wl.CompositorCreateRegion, wl.Core("wl_region"), s.compositor.Version(), 0)
		region.Marshal(wl.RegionAdd, nil, 0, wl.Int(x), wl.Int(y), wl.Int(w), wl.Int(h))
		s.held = s.constraints.Marshal(wl.ConstraintsConfinePointer, wl.ZwpConfinedPointerV1, 1, 0, surf, uintptr(s.pointer), uintptr(region), wl.LifetimePersistent)
		region.Destroy(0)
		s.lockedPointer = false
	}
	s.q.Flush()
}

func (s *seat) waylandFree() {
	if s.held != 0 {
		s.held.Destroy(0)
		s.held = 0
		s.q.Flush()
	}
}

// waylandWarp moves the pointer, which Wayland lets a client do only as
// where a locked pointer shows when it is let go.
func (s *seat) waylandWarp(x, y float32) {
	if s.held != 0 && s.lockedPointer {
		px, py, _, _ := s.surfaceRect(rect{X: x, Y: y, W: 1, H: 1})
		s.held.Marshal(wl.LockedSetCursorHint, nil, 0, wl.Fixed(float64(px)), wl.Fixed(float64(py)))
		s.q.Flush()
	}
}

// inhibit has the compositor pass the system's shortcuts to the window.
func (s *seat) inhibit(on bool) error {
	if !on {
		if s.inhibitor != 0 {
			s.inhibitor.Destroy(0)
			s.inhibitor = 0
			s.q.Flush()
		}
		return nil
	}
	if s.inhibitMgr == 0 || s.keyboard == 0 {
		return errTapDenied{hint: "This compositor does not let apps capture system shortcuts (keyboard-shortcuts-inhibit)."}
	}
	if s.inhibitor == 0 {
		s.inhibitor = s.inhibitMgr.Marshal(wl.InhibitShortcuts, wl.ZwpKeyboardShortcutsInhibitorV1, 1, 0, s.native.WLSurface, uintptr(s.wlSeat))
		s.q.Flush()
	}
	return nil
}

func (s *seat) close() {
	if s.x != nil {
		s.x.close()
		return
	}
	if s.q == nil {
		return
	}
	s.inhibit(false)
	s.waylandFree()
	s.relative.Destroy(0)
	if s.pointer != 0 {
		s.pointer.Destroy(wl.PointerRelease)
	}
	if s.keyboard != 0 {
		s.keyboard.Destroy(wl.KeyboardRelease)
	}
	s.constraints.Destroy(0)
	s.relManager.Destroy(0)
	s.inhibitMgr.Destroy(0)
	if s.wlSeat != 0 {
		s.wlSeat.Destroy(wl.SeatRelease)
	}
	s.compositor.Free()
	s.q.Close()
}

// x11 holds the XInput 2 and XFixes connections.
type x11 struct {
	events, req uintptr // Display*
	root        uintptr
	xi          int32 // the XInputExtension's major opcode
	barriers    []uintptr
	stop        [2]int
	done        chan struct{}
	s           *seat
}

var xlib struct {
	once sync.Once
	err  error

	openDisplay, closeDisplay, defaultRoot, connectionNumber, pending, nextEvent     uintptr
	getEventData, freeEventData, queryExtension, flush, warpPointer, translateCoords uintptr
	queryPointer, xiQueryVersion, xiSelectEvents                                     uintptr
	createBarrier, destroyBarrier                                                    uintptr
}

func loadXlib() error {
	xlib.once.Do(func() {
		var libs [3]uintptr
		for i, n := range []string{"libX11.so.6", "libXi.so.6", "libXfixes.so.3"} {
			h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				xlib.err = err
				return
			}
			libs[i] = h
		}
		for _, f := range []struct {
			lib  int
			p    *uintptr
			name string
		}{
			{0, &xlib.openDisplay, "XOpenDisplay"}, {0, &xlib.closeDisplay, "XCloseDisplay"},
			{0, &xlib.defaultRoot, "XDefaultRootWindow"}, {0, &xlib.connectionNumber, "XConnectionNumber"},
			{0, &xlib.pending, "XPending"}, {0, &xlib.nextEvent, "XNextEvent"},
			{0, &xlib.getEventData, "XGetEventData"}, {0, &xlib.freeEventData, "XFreeEventData"},
			{0, &xlib.queryExtension, "XQueryExtension"}, {0, &xlib.flush, "XFlush"},
			{0, &xlib.warpPointer, "XWarpPointer"}, {0, &xlib.translateCoords, "XTranslateCoordinates"},
			{0, &xlib.queryPointer, "XQueryPointer"},
			{1, &xlib.xiQueryVersion, "XIQueryVersion"}, {1, &xlib.xiSelectEvents, "XISelectEvents"},
			{2, &xlib.createBarrier, "XFixesCreatePointerBarrier"}, {2, &xlib.destroyBarrier, "XFixesDestroyPointerBarrier"},
		} {
			sym, err := purego.Dlsym(libs[f.lib], f.name)
			if err != nil {
				xlib.err = err
				return
			}
			*f.p = sym
		}
	})
	return xlib.err
}

// XInput 2 raw events.
const (
	xiRawKeyPress      = 13
	xiRawKeyRelease    = 14
	xiRawButtonPress   = 15
	xiRawButtonRelease = 16
	xiRawMotion        = 17
	genericEvent       = 35
)

func (s *seat) openX11() error {
	if err := loadXlib(); err != nil {
		return err
	}
	x := &x11{s: s, stop: [2]int{-1, -1}}
	x.events = ccall(xlib.openDisplay, 0)
	x.req = ccall(xlib.openDisplay, 0)
	if x.events == 0 || x.req == 0 {
		x.close()
		return errors.New("XOpenDisplay failed")
	}
	x.root = ccall(xlib.defaultRoot, x.events)
	name := cstr("XInputExtension")
	var op, ev, er int32
	if ccall(xlib.queryExtension, x.events, uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&op)), uintptr(unsafe.Pointer(&ev)), uintptr(unsafe.Pointer(&er)))&1 == 0 {
		x.close()
		return errors.New("no XInput extension")
	}
	x.xi = op
	major, minor := int32(2), int32(2)
	if int32(ccall(xlib.xiQueryVersion, x.events, uintptr(unsafe.Pointer(&major)), uintptr(unsafe.Pointer(&minor)))) != 0 {
		x.close()
		return errors.New("XInput 2.2 missing")
	}
	mask := [4]byte{}
	for _, e := range []int{xiRawKeyPress, xiRawKeyRelease, xiRawButtonPress, xiRawButtonRelease, xiRawMotion} {
		mask[e/8] |= 1 << (e % 8)
	}
	em := struct {
		device, maskLen int32
		mask            *byte
	}{1 /* XIAllMasterDevices */, int32(len(mask)), &mask[0]}
	ccall(xlib.xiSelectEvents, x.events, x.root, uintptr(unsafe.Pointer(&em)), 1)
	ccall(xlib.flush, x.events)
	if err := syscall.Pipe2(x.stop[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		x.close()
		return err
	}
	x.done = make(chan struct{})
	s.x = x
	go x.run()
	return nil
}

// run reads raw events until close.
func (x *x11) run() {
	defer close(x.done)
	fd := int32(ccall(xlib.connectionNumber, x.events))
	var ev [192]byte // XEvent
	for {
		for int32(ccall(xlib.pending, x.events)) > 0 {
			ccall(xlib.nextEvent, x.events, uintptr(unsafe.Pointer(&ev[0])))
			x.event(&ev)
		}
		fds := [2]struct {
			fd              int32
			events, revents int16
		}{{fd: fd, events: 1}, {fd: int32(x.stop[0]), events: 1}}
		syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), 2, 0, 0, 0, 0)
		if fds[1].revents != 0 {
			return
		}
	}
}

// event handles one XGenericEventCookie (type 0, extension 32, evtype 36,
// data 48) of an XIRawEvent (detail 56, valuators' mask_len 64, mask 72,
// raw_values 88).
func (x *x11) event(ev *[192]byte) {
	if *(*int32)(unsafe.Pointer(&ev[0])) != genericEvent || *(*int32)(unsafe.Pointer(&ev[32])) != x.xi {
		return
	}
	if ccall(xlib.getEventData, x.events, uintptr(unsafe.Pointer(&ev[0])))&1 == 0 {
		return
	}
	defer ccall(xlib.freeEventData, x.events, uintptr(unsafe.Pointer(&ev[0])))
	data := *(*uintptr)(unsafe.Pointer(&ev[48]))
	detail := *(*int32)(xptr(data + 56))
	s := x.s
	switch *(*int32)(unsafe.Pointer(&ev[36])) {
	case xiRawMotion:
		if !s.relOn.Load() {
			return
		}
		maskLen := int(*(*int32)(xptr(data + 64)))
		mask := *(*uintptr)(xptr(data + 72))
		raw := *(*uintptr)(xptr(data + 88))
		var d [2]float64
		k := 0
		for axis := 0; axis < maskLen*8; axis++ {
			if *(*byte)(xptr(mask + uintptr(axis/8)))&(1<<(axis%8)) == 0 {
				continue
			}
			if axis < 2 {
				d[axis] = *(*float64)(xptr(raw + uintptr(k)*8))
			}
			k++
		}
		if f := s.motion.Load(); f != nil && (d[0] != 0 || d[1] != 0) {
			(*f)(d[0], d[1])
		}
	case xiRawButtonPress, xiRawButtonRelease:
		down := *(*int32)(unsafe.Pointer(&ev[36])) == xiRawButtonPress
		if b, ok := xButtons[detail]; ok {
			s.button(b, down)
		}
	case xiRawKeyPress, xiRawKeyRelease:
		if f := s.keys.Load(); f != nil && detail >= 8 {
			(*f)(uint16(detail-8), *(*int32)(unsafe.Pointer(&ev[36])) == xiRawKeyPress)
		}
	}
}

// xButtons maps X11 button numbers to the host's buttons.
var xButtons = map[int32]input.Button{1: input.ButtonLeft, 2: input.ButtonMiddle, 3: input.ButtonRight, 8: input.ButtonX1, 9: input.ButtonX2}

func xptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// rootRect is r, in the content's DIPs, in the root window's pixels.
func (x *x11) rootRect(r rect) (x0, y0, x1, y1 int) {
	n := x.s.native
	var rx, ry int32
	var child uintptr
	ccall(xlib.translateCoords, x.req, n.XID, x.root, 0, 0, uintptr(unsafe.Pointer(&rx)), uintptr(unsafe.Pointer(&ry)), uintptr(unsafe.Pointer(&child)))
	sc := float32(n.Scale)
	px := func(v float32) int { return int(v*sc + 0.5) }
	x0, y0 = int(rx)+n.AreaX*n.Scale+px(r.X), int(ry)+n.AreaY*n.Scale+px(r.Y)
	return x0, y0, x0 + max(px(r.W), 1), y0 + max(px(r.H), 1)
}

// hold keeps the pointer in area with four XFixes barriers, which leave
// GTK every event (a pointer grab of this connection would take them);
// relative, in a box of two pixels at its centre, the pointer warped in
// first, while raw motion brings the mouse's moves.
func (x *x11) hold(m pointerMode, area rect) {
	x.free()
	x0, y0, x1, y1 := x.rootRect(area)
	if m == pointerRelative {
		cx, cy := (x0+x1)/2, (y0+y1)/2
		ccall(xlib.warpPointer, x.req, 0, x.root, 0, 0, 0, 0, uintptr(cx), uintptr(cy))
		x0, y0, x1, y1 = cx-1, cy-1, cx+1, cy+1
	}
	const posX, posY, negX, negY = 1, 2, 4, 8
	for _, b := range [...][5]int{
		{x0, y0, x0, y1, posX}, {x1, y0, x1, y1, negX},
		{x0, y0, x1, y0, posY}, {x0, y1, x1, y1, negY},
	} {
		id := ccall(xlib.createBarrier, x.req, x.root, uintptr(b[0]), uintptr(b[1]), uintptr(b[2]), uintptr(b[3]), uintptr(b[4]), 0, 0)
		if id != 0 {
			x.barriers = append(x.barriers, id)
		}
	}
	ccall(xlib.flush, x.req)
}

func (x *x11) free() {
	for _, b := range x.barriers {
		ccall(xlib.destroyBarrier, x.req, b)
	}
	x.barriers = x.barriers[:0]
	ccall(xlib.flush, x.req)
}

func (x *x11) warp(r rect) {
	x0, y0, _, _ := x.rootRect(r)
	ccall(xlib.warpPointer, x.req, 0, x.root, 0, 0, 0, 0, uintptr(x0), uintptr(y0))
	ccall(xlib.flush, x.req)
}

// mods reads Shift, Control and Mod1 (Alt) held now, on the event
// goroutine, which owns the events connection.
func (x *x11) mods() uint32 {
	var root, child uintptr
	var rx, ry, wx, wy int32
	var mask uint32
	ccall(xlib.queryPointer, x.events, x.root, uintptr(unsafe.Pointer(&root)), uintptr(unsafe.Pointer(&child)),
		uintptr(unsafe.Pointer(&rx)), uintptr(unsafe.Pointer(&ry)), uintptr(unsafe.Pointer(&wx)), uintptr(unsafe.Pointer(&wy)), uintptr(unsafe.Pointer(&mask)))
	var m uint32
	if mask&1 != 0 {
		m |= xkbShift
	}
	if mask&4 != 0 {
		m |= xkbControl
	}
	if mask&8 != 0 {
		m |= xkbMod1
	}
	return m
}

func (x *x11) close() {
	if x.done != nil {
		syscall.Write(x.stop[1], []byte{0})
		<-x.done
		syscall.Close(x.stop[0])
		syscall.Close(x.stop[1])
	}
	if x.req != 0 {
		x.free()
		ccall(xlib.closeDisplay, x.req)
	}
	if x.events != 0 {
		ccall(xlib.closeDisplay, x.events)
	}
}
