//go:build linux && !android && (amd64 || arm64)

// Package x11 captures an Xorg (or XWayland) screen with MIT-SHM: each
// frame is one XShmGetImage of a monitor's rectangle of the root window
// into shared memory, which the encoder reads in place. RandR lists the
// monitors and XFixes reads the cursor.
package x11

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/pkg/capture"
)

// ErrNoDisplay means there is no X server to talk to, or no monitor with
// the asked ID on it.
var ErrNoDisplay = errors.New("x11: no X display")

var lib struct {
	once sync.Once
	err  error

	initThreads, openDisplay, closeDisplay, defaultScreen, rootWindow, defaultVisual uintptr
	defaultDepth, displayWidth, displayHeight, xfree, xsync, getAtomName             uintptr
	queryPointer, resourceManagerString, setErrorHandler, queryExtension             uintptr

	shmQueryExtension, shmCreateImage, shmAttach, shmDetach, shmGetImage uintptr

	rrGetMonitors, rrFreeMonitors, rrGetScreenResourcesCurrent, rrFreeScreenResources uintptr
	rrGetOutputInfo, rrFreeOutputInfo, rrGetCrtcInfo, rrFreeCrtcInfo                  uintptr

	fixesGetCursorImage uintptr
}

func load() error {
	lib.once.Do(func() {
		x, err := purego.Dlopen("libX11.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: no libX11 (%v)", ErrNoDisplay, err)
			return
		}
		ext, err := purego.Dlopen("libXext.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("x11: no libXext for MIT-SHM (%v)", err)
			return
		}
		syms := []struct {
			h    uintptr
			p    *uintptr
			name string
		}{
			{x, &lib.initThreads, "XInitThreads"},
			{x, &lib.openDisplay, "XOpenDisplay"},
			{x, &lib.closeDisplay, "XCloseDisplay"},
			{x, &lib.defaultScreen, "XDefaultScreen"},
			{x, &lib.rootWindow, "XRootWindow"},
			{x, &lib.defaultVisual, "XDefaultVisual"},
			{x, &lib.defaultDepth, "XDefaultDepth"},
			{x, &lib.displayWidth, "XDisplayWidth"},
			{x, &lib.displayHeight, "XDisplayHeight"},
			{x, &lib.xfree, "XFree"},
			{x, &lib.xsync, "XSync"},
			{x, &lib.getAtomName, "XGetAtomName"},
			{x, &lib.queryPointer, "XQueryPointer"},
			{x, &lib.resourceManagerString, "XResourceManagerString"},
			{x, &lib.setErrorHandler, "XSetErrorHandler"},
			{x, &lib.queryExtension, "XQueryExtension"},
			{ext, &lib.shmQueryExtension, "XShmQueryExtension"},
			{ext, &lib.shmCreateImage, "XShmCreateImage"},
			{ext, &lib.shmAttach, "XShmAttach"},
			{ext, &lib.shmDetach, "XShmDetach"},
			{ext, &lib.shmGetImage, "XShmGetImage"},
		}
		for _, s := range syms {
			if *s.p, err = purego.Dlsym(s.h, s.name); err != nil {
				lib.err = fmt.Errorf("x11: %v", err)
				return
			}
		}
		// RandR and XFixes are optional: without RandR the whole screen
		// is one monitor, without XFixes the cursor is unknown.
		if rr, err := purego.Dlopen("libXrandr.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			ok := true
			for _, s := range []struct {
				p    *uintptr
				name string
			}{
				{&lib.rrGetMonitors, "XRRGetMonitors"},
				{&lib.rrFreeMonitors, "XRRFreeMonitors"},
				{&lib.rrGetScreenResourcesCurrent, "XRRGetScreenResourcesCurrent"},
				{&lib.rrFreeScreenResources, "XRRFreeScreenResources"},
				{&lib.rrGetOutputInfo, "XRRGetOutputInfo"},
				{&lib.rrFreeOutputInfo, "XRRFreeOutputInfo"},
				{&lib.rrGetCrtcInfo, "XRRGetCrtcInfo"},
				{&lib.rrFreeCrtcInfo, "XRRFreeCrtcInfo"},
			} {
				if *s.p, err = purego.Dlsym(rr, s.name); err != nil {
					ok = false
				}
			}
			if !ok {
				lib.rrGetMonitors = 0
			}
		}
		if fx, err := purego.Dlopen("libXfixes.so.3", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			lib.fixesGetCursorImage, _ = purego.Dlsym(fx, "XFixesGetCursorImage")
		}
		call(lib.initThreads)
		call(lib.setErrorHandler, purego.NewCallback(onError))
	})
	return lib.err
}

// lastError is the code of the X protocol error last reported.
var lastError atomic.Uint32

// onError keeps an X protocol error, which Xlib's own handler answers by
// ending the process: a monitor that shrank under XShmGetImage reports
// BadMatch, and the request returns failure.
func onError(dpy, event uintptr) uintptr {
	lastError.Store(uint32(field[uint8](event, 32)))
	return 0
}

// call calls C; go:uintptrescapes keeps a pointer argument valid when the
// goroutine's stack moves before the call.
//
//go:uintptrescapes
func call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(fn, args...)
	return r
}

func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

func field[T any](base, off uintptr) T { return *(*T)(ptr(base + off)) }

func cString(p uintptr) string {
	if p == 0 {
		return ""
	}
	n := 0
	for *(*byte)(ptr(p + uintptr(n))) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptr(p)), n))
}

// Available reports whether an X server answers on $DISPLAY.
func Available() error {
	c, err := connect()
	if err != nil {
		return err
	}
	c.close()
	return nil
}

// XWayland reports whether the X server is XWayland. A rootless one, as
// a Wayland desktop runs for its X clients, composes no root image:
// capture there goes through the desktop portal.
func XWayland() bool {
	c, err := connect()
	if err != nil {
		return false
	}
	defer c.close()
	name := []byte("XWAYLAND\x00")
	var op, ev, er int32
	return call(lib.queryExtension, c.dpy, uintptr(unsafe.Pointer(&name[0])),
		uintptr(unsafe.Pointer(&op)), uintptr(unsafe.Pointer(&ev)), uintptr(unsafe.Pointer(&er))) != 0
}

// conn is one Xlib connection to the default screen.
type conn struct {
	dpy    uintptr
	screen uintptr
	root   uintptr
}

func connect() (*conn, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, fmt.Errorf("%w: DISPLAY is not set", ErrNoDisplay)
	}
	if err := load(); err != nil {
		return nil, err
	}
	dpy := call(lib.openDisplay, 0)
	if dpy == 0 {
		return nil, fmt.Errorf("%w: cannot open %q", ErrNoDisplay, os.Getenv("DISPLAY"))
	}
	scr := call(lib.defaultScreen, dpy)
	return &conn{dpy: dpy, screen: scr, root: call(lib.rootWindow, dpy, scr)}, nil
}

func (c *conn) close() {
	if c.dpy != 0 {
		call(lib.closeDisplay, c.dpy)
		c.dpy = 0
	}
}

// XRRMonitorInfo, XRRScreenResources, XRRModeInfo, XRROutputInfo and
// XRRCrtcInfo offsets on LP64 (Xrandr.h).
const (
	monSize     = 56
	monName     = 0
	monPrimary  = 8
	monNOutput  = 16
	monX        = 20
	monY        = 24
	monWidth    = 28
	monHeight   = 32
	monOutputs  = 48
	resNMode    = 48
	resModes    = 56
	modeSize    = 80
	modeID      = 0
	modeDot     = 16
	modeHTotal  = 32
	modeVTotal  = 48
	modeFlags   = 72
	outCrtc     = 8
	crtcMode    = 24
	flagDblScan = 0x20
	flagIntl    = 0x10
)

// Displays lists the X screen's monitors from RandR, or the whole screen
// as one display where RandR is missing.
func Displays() ([]capture.Display, error) {
	c, err := connect()
	if err != nil {
		return nil, err
	}
	defer c.close()
	return c.displays(), nil
}

func (c *conn) displays() []capture.Display {
	whole := capture.Display{ID: "screen", Name: "X screen",
		Width: int(call(lib.displayWidth, c.dpy, c.screen)), Height: int(call(lib.displayHeight, c.dpy, c.screen)), Refresh: 60, Primary: true}
	if lib.rrGetMonitors == 0 {
		return []capture.Display{whole}
	}
	var n int32
	mons := call(lib.rrGetMonitors, c.dpy, c.root, 1, uintptr(unsafe.Pointer(&n)))
	if mons == 0 || n <= 0 {
		return []capture.Display{whole}
	}
	defer call(lib.rrFreeMonitors, mons)
	res := call(lib.rrGetScreenResourcesCurrent, c.dpy, c.root)
	if res != 0 {
		defer call(lib.rrFreeScreenResources, res)
	}
	var out []capture.Display
	for i := range uintptr(n) {
		m := mons + i*monSize
		d := capture.Display{
			X: int(field[int32](m, monX)), Y: int(field[int32](m, monY)),
			Width: int(field[int32](m, monWidth)), Height: int(field[int32](m, monHeight)),
			Primary: field[int32](m, monPrimary) != 0, Refresh: 60,
		}
		if a := field[uintptr](m, monName); a != 0 {
			if p := call(lib.getAtomName, c.dpy, a); p != 0 {
				d.ID = cString(p)
				call(lib.xfree, p)
			}
		}
		if d.ID == "" {
			d.ID = strconv.Itoa(int(i))
		}
		d.Name = d.ID
		if res != 0 && field[int32](m, monNOutput) > 0 {
			if hz := c.refresh(res, field[uintptr](field[uintptr](m, monOutputs), 0)); hz > 0 {
				d.Refresh = hz
			}
		}
		out = append(out, d)
	}
	if len(out) > 0 && !hasPrimary(out) {
		out[0].Primary = true
	}
	return out
}

func hasPrimary(ds []capture.Display) bool {
	for _, d := range ds {
		if d.Primary {
			return true
		}
	}
	return false
}

// refresh is the rate of the mode output's CRTC shows, in Hz.
func (c *conn) refresh(res, output uintptr) float64 {
	oi := call(lib.rrGetOutputInfo, c.dpy, res, output)
	if oi == 0 {
		return 0
	}
	defer call(lib.rrFreeOutputInfo, oi)
	crtc := field[uintptr](oi, outCrtc)
	if crtc == 0 {
		return 0
	}
	ci := call(lib.rrGetCrtcInfo, c.dpy, res, crtc)
	if ci == 0 {
		return 0
	}
	defer call(lib.rrFreeCrtcInfo, ci)
	mode := field[uintptr](ci, crtcMode)
	modes := field[uintptr](res, resModes)
	for i := range uintptr(field[int32](res, resNMode)) {
		mi := modes + i*modeSize
		if field[uintptr](mi, modeID) == mode {
			return modeRate(field[uint64](mi, modeDot), field[uint32](mi, modeHTotal), field[uint32](mi, modeVTotal), field[uint64](mi, modeFlags))
		}
	}
	return 0
}

// modeRate is a mode's refresh rate from its pixel clock and totals, as
// xrandr prints it.
func modeRate(dotClock uint64, hTotal, vTotal uint32, flags uint64) float64 {
	v := float64(vTotal)
	if flags&flagDblScan != 0 {
		v *= 2
	}
	if flags&flagIntl != 0 {
		v /= 2
	}
	if hTotal == 0 || v == 0 {
		return 0
	}
	return math.Round(float64(dotClock)/(float64(hTotal)*v)*100) / 100
}

// DPI is the X resource Xft.dpi, which desktops set to their scale times
// 96; 96 where it is not set.
func DPI() int {
	c, err := connect()
	if err != nil {
		return 96
	}
	defer c.close()
	return parseDPI(cString(call(lib.resourceManagerString, c.dpy)))
}

func parseDPI(resources string) int {
	for _, line := range strings.Split(resources, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "Xft.dpi" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
				return int(math.Round(f))
			}
		}
	}
	return 96
}

// XImage and XShmSegmentInfo offsets on LP64 (Xlib.h, XShm.h).
const (
	imgData         = 16
	imgBytesPerLine = 44
	imgBitsPerPixel = 48
	imgRedMask      = 56
	imgObdata       = 80
	imgDestroy      = 96
	zPixmap         = 2
	allPlanes       = ^uintptr(0)
)

type shmSegment struct {
	shmseg   uintptr
	shmid    int32
	_        int32
	shmaddr  uintptr
	readOnly int32
	_        int32
}

// Source captures one monitor of the X screen at a fixed rate.
type Source struct {
	c       *conn
	display capture.Display
	image   uintptr
	seg     *shmSegment
	mem     []byte
	buf     capture.DMABuf
	period  time.Duration
	next    time.Duration
	held    bool
}

// Open captures the monitor with this ID (a RandR monitor name such as
// "DP-1"; empty for the primary one) fps times a second.
func Open(id string, fps int) (*Source, error) {
	c, err := connect()
	if err != nil {
		return nil, err
	}
	var d capture.Display
	for _, x := range c.displays() {
		if x.ID == id || id == "" && x.Primary {
			d = x
		}
	}
	if d.ID == "" {
		c.close()
		return nil, fmt.Errorf("%w: no monitor %q", ErrNoDisplay, id)
	}
	if fps <= 0 {
		fps = max(int(d.Refresh+0.5), 60)
	}
	s := &Source{c: c, display: d, period: time.Second / time.Duration(fps)}
	if err := s.attach(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Display is the monitor the source captures.
func (s *Source) Display() capture.Display { return s.display }

func (s *Source) attach() error {
	c := s.c
	if call(lib.shmQueryExtension, c.dpy) == 0 {
		return errors.New("x11: the X server has no MIT-SHM (a remote display?)")
	}
	depth := call(lib.defaultDepth, c.dpy, c.screen)
	if depth != 24 && depth != 32 {
		return fmt.Errorf("x11: a %d-bit screen; capture takes 24 and 32 bits", depth)
	}
	s.seg = &shmSegment{}
	img := call(lib.shmCreateImage, c.dpy, call(lib.defaultVisual, c.dpy, c.screen), depth, zPixmap, 0,
		uintptr(unsafe.Pointer(s.seg)), uintptr(s.display.Width), uintptr(s.display.Height))
	if img == 0 {
		return errors.New("x11: XShmCreateImage failed")
	}
	s.image = img
	if bpp := field[int32](img, imgBitsPerPixel); bpp != 32 || field[uint64](img, imgRedMask) != 0xff0000 {
		return fmt.Errorf("x11: pixels of %d bits with red mask %#x; capture takes BGRX", bpp, field[uint64](img, imgRedMask))
	}
	pitch := int(field[int32](img, imgBytesPerLine))
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, pitch*s.display.Height, unix.IPC_CREAT|0o600)
	if err != nil {
		return fmt.Errorf("x11: shmget: %w", err)
	}
	s.seg.shmid = int32(id)
	mem, err := unix.SysvShmAttach(id, 0, 0)
	// Marked for removal now, the segment goes when both sides detach,
	// whatever ends this process.
	unix.SysvShmCtl(id, unix.IPC_RMID, nil)
	if err != nil {
		s.seg.shmid = -1
		return fmt.Errorf("x11: shmat: %w", err)
	}
	s.mem = mem
	s.seg.shmaddr = uintptr(unsafe.Pointer(&mem[0]))
	*(*uintptr)(ptr(img + imgData)) = s.seg.shmaddr
	if call(lib.shmAttach, c.dpy, uintptr(unsafe.Pointer(s.seg))) == 0 {
		return errors.New("x11: XShmAttach failed")
	}
	call(lib.xsync, c.dpy, 0)
	s.buf = capture.DMABuf{Fourcc: capture.FourccXRGB8888, Modifier: capture.ModifierLinear, Width: s.display.Width, Height: s.display.Height, Planes: 1}
	s.buf.CPU[0] = unsafe.Pointer(&mem[0])
	s.buf.Pitches[0] = uint32(pitch)
	return nil
}

// Next waits for the next capture tick and reads the monitor. X11 tells
// nothing of changes without XDamage, so every tick is a frame.
func (s *Source) Next(timeout time.Duration) (capture.Frame, error) {
	now := clock.Now()
	if s.next == 0 {
		s.next = now
	}
	if s.next-now > timeout {
		time.Sleep(timeout)
		return capture.Frame{}, capture.ErrTimeout
	}
	if wait := s.next - now; wait > 0 {
		time.Sleep(wait)
	}
	if late := clock.Now() - s.next; late > 4*s.period {
		s.next = clock.Now()
	}
	s.next += s.period
	at := clock.Now()
	if call(lib.shmGetImage, s.c.dpy, s.c.root, s.image, uintptr(s.display.X), uintptr(s.display.Y), allPlanes) == 0 {
		return capture.Frame{}, fmt.Errorf("%w: XShmGetImage failed (X error %d)", capture.ErrAccessLost, lastError.Load())
	}
	s.held = true
	return capture.Frame{Surface: s.buf.Handle(), Format: capture.FormatBGRA8, Width: s.display.Width, Height: s.display.Height,
		Presented: at, Acquired: clock.Now()}, nil
}

func (s *Source) Release() error {
	s.held = false
	return nil
}

func (s *Source) Close() error {
	if s.c == nil {
		return nil
	}
	if s.seg != nil && s.seg.shmaddr != 0 {
		call(lib.shmDetach, s.c.dpy, uintptr(unsafe.Pointer(s.seg)))
		call(lib.xsync, s.c.dpy, 0)
	}
	if s.image != 0 {
		// The data is the shared segment, detached below; destroy frees
		// only the XImage.
		*(*uintptr)(ptr(s.image + imgData)) = 0
		*(*uintptr)(ptr(s.image + imgObdata)) = 0
		call(field[uintptr](s.image, imgDestroy), s.image)
		s.image = 0
	}
	if s.mem != nil {
		unix.SysvShmDetach(s.mem)
		s.mem = nil
	}
	s.c.close()
	s.c = nil
	return nil
}

var _ capture.Source = (*Source)(nil)

// Pointer is where the pointer is on the X screen.
func Pointer() (x, y int, err error) {
	c, err := cursorConn()
	if err != nil {
		return 0, 0, err
	}
	var root, child uintptr
	var rx, ry, wx, wy int32
	var mask uint32
	if call(lib.queryPointer, c.dpy, c.root, uintptr(unsafe.Pointer(&root)), uintptr(unsafe.Pointer(&child)),
		uintptr(unsafe.Pointer(&rx)), uintptr(unsafe.Pointer(&ry)), uintptr(unsafe.Pointer(&wx)), uintptr(unsafe.Pointer(&wy)),
		uintptr(unsafe.Pointer(&mask))) == 0 {
		return 0, 0, errors.New("x11: the pointer is on another screen")
	}
	return int(rx), int(ry), nil
}

// CursorImage is the cursor's image from XFixes.
type CursorImage struct {
	X, Y, W, H, HotX, HotY int
	// Serial changes as the cursor changes shape.
	Serial uint64
	// ARGB holds W*H pixels, premultiplied as XFixes gives them.
	ARGB []uint32
}

// XFixesCursorImage offsets on LP64 (Xfixes.h).
const (
	curX      = 0
	curY      = 2
	curWidth  = 4
	curHeight = 6
	curXHot   = 8
	curYHot   = 10
	curSerial = 16
	curPixels = 24
)

// Cursor reads the cursor's image, with pixels when withPixels is set.
func Cursor(withPixels bool) (CursorImage, error) {
	c, err := cursorConn()
	if err != nil {
		return CursorImage{}, err
	}
	if lib.fixesGetCursorImage == 0 {
		return CursorImage{}, errors.New("x11: no XFixes")
	}
	p := call(lib.fixesGetCursorImage, c.dpy)
	if p == 0 {
		return CursorImage{}, errors.New("x11: XFixesGetCursorImage failed")
	}
	defer call(lib.xfree, p)
	ci := CursorImage{
		X: int(field[int16](p, curX)), Y: int(field[int16](p, curY)),
		W: int(field[uint16](p, curWidth)), H: int(field[uint16](p, curHeight)),
		HotX: int(field[uint16](p, curXHot)), HotY: int(field[uint16](p, curYHot)),
		Serial: field[uint64](p, curSerial),
	}
	if withPixels && ci.W > 0 && ci.H > 0 {
		// Each pixel is an unsigned long holding 32 bits of ARGB.
		px := unsafe.Slice((*uint64)(ptr(field[uintptr](p, curPixels))), ci.W*ci.H)
		ci.ARGB = make([]uint32, len(px))
		for i, v := range px {
			ci.ARGB[i] = uint32(v)
		}
	}
	return ci, nil
}

var cursorState struct {
	mu sync.Mutex
	c  *conn
}

// cursorConn is the connection the cursor readers share, opened once.
func cursorConn() (*conn, error) {
	cursorState.mu.Lock()
	defer cursorState.mu.Unlock()
	if cursorState.c != nil {
		return cursorState.c, nil
	}
	c, err := connect()
	if err != nil {
		return nil, err
	}
	cursorState.c = c
	return c, nil
}
