//go:build linux && !android && (amd64 || arm64)

package gpusurface

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// On Linux a frame is a *DMABuf, passed to Present as a uintptr. The
// surface shows it one of two ways, picked at New:
//
//   - Wayland: in a desync wl_subsurface of the window's own, under its
//     toplevel wl_surface, which a MyGo window with Transparent shows
//     through where its UI paints nothing, the Windows Composite contract.
//     A frame in DMA-BUFs becomes a zwp_linux_dmabuf_v1 wl_buffer, made
//     once for each frame of the decoder's pool, which the compositor
//     samples (or scans out) as it is: no copy, YUV converted by the
//     compositor. A frame in memory goes into a wl_shm buffer, as NV12 or
//     P010 where the compositor takes them, else converted to XRGB8888.
//     wp_viewporter fits the frame to the window's content and a black
//     backdrop fills the bars; wp_presentation reports when each frame
//     reached the screen.
//   - Picture mode, on X11 and on compositors without subsurfaces or
//     viewporter: X11 cannot blend MyGo's UI over another window, so the
//     frames go into a MyGo Bitmap the app draws under its UI (Bitmap,
//     Refresh), uploaded as NV12 and converted by MyGo's renderer. It
//     needs the frames' planes in memory (DMABuf.CPU), and P010 shows with
//     its 8 high bits.

// DMABuf describes a frame in DMA-BUF objects or in memory. Its layout is
// fixed: producers in other modules declare the same struct (Planck's
// capture.DMABuf). FDs hold each plane's DMA-BUF (0 for none), CPU each
// plane's bytes in memory (nil for none), at Offsets and Pitches. Memory
// says FDs are memfds holding the CPU planes. Serial tells frames apart
// for the surface's cache of what it made of them.
type DMABuf struct {
	Fourcc        uint32
	Modifier      uint64
	Width, Height int
	Planes        int
	FDs           [4]int32
	Offsets       [4]uint32
	Pitches       [4]uint32
	CPU           [4]unsafe.Pointer
	Memory        bool
	Serial        uint64
}

// DRM fourccs the surface takes.
const (
	FourccNV12     uint32 = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	FourccP010     uint32 = 'P' | '0'<<8 | '1'<<16 | '0'<<24
	FourccXRGB8888 uint32 = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	FourccARGB8888 uint32 = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
)

const modifierInvalid uint64 = 1<<56 - 1

func frameOf(surface uintptr) *DMABuf { return (*DMABuf)(ptr(surface)) }

func (d *DMABuf) hasFDs() bool { return d.Planes > 0 && d.FDs[0] > 0 && !d.Memory }
func (d *DMABuf) hasCPU() bool { return d.Planes > 0 && d.CPU[0] != nil }

// plane is plane i's bytes in memory, rows rows long.
func (d *DMABuf) plane(i, rows int) []byte {
	return unsafe.Slice((*byte)(d.CPU[i]), int(d.Pitches[i])*rows)
}

// Surface presents frames into a MyGo window on Linux.
type Surface struct {
	timings timings
	window  uintptr

	// layoutMu guards native's area, which Layout reads on the main
	// thread and the presenting thread applies.
	layoutMu sync.Mutex
	native   Native
	moved    bool

	way *waySurface
	pic *pictureMode

	presented atomic.Pointer[func(tag, submitted, presented int64)]
	closed    bool
}

// New makes a surface on a MyGo window: opts.Window is its GtkWindow*, as
// Window.NativeHandle returns it. Call it on the main thread, with the
// window realized. Options.Adapter, Composite, Drawable10Bit and HDR10
// are not used on Linux.
func New(opts Options) (*Surface, error) {
	n, err := NativeOf(opts.Window)
	if err != nil {
		return nil, err
	}
	s := &Surface{window: opts.Window, native: n, timings: timings{recs: make([]Timing, opts.Timings)}}
	if n.Wayland {
		way, err := newWaySurface(s, opts)
		if err == nil {
			s.way = way
			return s, nil
		}
		if !errors.Is(err, errNoSubsurfaces) {
			return nil, err
		}
	}
	s.pic = newPictureMode(opts.Redraw)
	return s, nil
}

// Accepts reports whether the surface shows DMA-BUFs of fourcc and
// modifier as they are, which only a Wayland compositor with
// zwp_linux_dmabuf_v1 does; others need the frames' planes in memory.
func (s *Surface) Accepts(fourcc uint32, modifier uint64) bool {
	return s.way != nil && s.way.dmabuf != 0 && s.way.takesDMABuf(fourcc, modifier)
}

// Wayland reports whether the surface shows frames in a subsurface of its
// own; otherwise it is in picture mode (Bitmap).
func (s *Surface) Wayland() bool { return s.way != nil }

// Layout reads the place of MyGo's content in the window again, after the
// window changed size; Resize applies it. Call it on the main thread.
func (s *Surface) Layout() {
	s.layoutMu.Lock()
	defer s.layoutMu.Unlock()
	before := s.native
	s.native.layout(s.window)
	if s.native != before {
		s.moved = true
	}
	if s.way != nil {
		s.way.queueParentCommit()
	}
}

// area returns the content's place and whether it changed since asked
// last.
func (s *Surface) area() (n Native, moved bool) {
	s.layoutMu.Lock()
	defer s.layoutMu.Unlock()
	n, moved, s.moved = s.native, s.moved, false
	return n, moved
}

// Size is the drawable size in pixels: the content's, at its scale.
func (s *Surface) Size() (w, h int) {
	s.layoutMu.Lock()
	defer s.layoutMu.Unlock()
	return s.native.AreaW * s.native.Scale, s.native.AreaH * s.native.Scale
}

// Scale is the content's scale, pixels per DIP.
func (s *Surface) Scale() float64 {
	s.layoutMu.Lock()
	defer s.layoutMu.Unlock()
	return float64(s.native.Scale)
}

// Resize takes the content's place as Layout last read it; w and h are
// not used: the surface always fills the content. Call it from the
// presenting thread.
func (s *Surface) Resize(w, h int) error {
	if s.way != nil {
		return s.way.applyLayout(true)
	}
	return nil
}

// OnPresented has f called with each frame's tag, as PresentTagged was
// given it, when Present was called, and when it reached the screen, or 0
// for presented when a newer frame replaced it first. Only Wayland
// compositors with wp_presentation tell; f runs on the surface's event
// goroutine.
func (s *Surface) OnPresented(f func(tag, submitted, presented int64)) {
	if f == nil {
		s.presented.Store(nil)
		return
	}
	s.presented.Store(&f)
}

// Present shows frame, a *DMABuf.
func (s *Surface) Present(frame uintptr) error { return s.PresentTagged(frame, 0) }

// PresentTagged presents frame with tag, which OnPresented hands back.
func (s *Surface) PresentTagged(frame uintptr, tag int64) error {
	if frame == 0 {
		return ErrFormat
	}
	d := frameOf(frame)
	submitted := Now()
	rec := s.timings.start(submitted)
	var err error
	if s.way != nil {
		err = s.way.present(d, tag, submitted, rec)
	} else {
		err = s.pic.present(d)
		if rec >= 0 {
			s.timings.recs[rec].Encoded = Now()
			s.timings.resolve(rec, 0)
		}
	}
	return err
}

// Close frees the surface. Call it on the main thread, once nothing
// presents.
func (s *Surface) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.way != nil {
		s.way.close()
	}
}

// Now reads CLOCK_MONOTONIC, in nanoseconds: the clock of Timing and
// OnPresented, and the one Wayland compositors report presentation on.
func Now() int64 { return clockNow(clockMonotonic) }

const clockMonotonic = 1

func clockNow(clock int32) int64 {
	var ts syscall.Timespec
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, uintptr(clock), uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Nano()
}

// SetDirect returns ErrUnsupported: the compositor decides on its own
// when the subsurface scans out.
func (s *Surface) SetDirect(bool) error { return ErrUnsupported }

// Direct reports false.
func (s *Surface) Direct() bool { return false }

// SetOverlay does nothing: MyGo's UI draws over the frames.
func (s *Surface) SetOverlay([]OverlayRect) {}

// SetOverlayAtlas returns ErrUnsupported: MyGo's UI draws over the frames.
func (s *Surface) SetOverlayAtlas([]byte, int, int) error { return ErrUnsupported }

// ScreenSize returns 0, 0: GTK 3 does not tell it.
func (s *Surface) ScreenSize() (w, h int) { return 0, 0 }
