//go:build linux && !android && (amd64 || arm64)

package x11

import (
	"errors"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/planckdotgg/planck/pkg/capture"
)

func TestModeRate(t *testing.T) {
	// 1920x1080 at 60 Hz (CVT reduced blanking) and 2560x1440 at 143.97.
	if r := modeRate(138500000, 2080, 1111, 0); r != 59.93 {
		t.Fatalf("1080p mode: %v Hz", r)
	}
	if r := modeRate(592500000, 2720, 1513, 0); r != 143.97 {
		t.Fatalf("1440p mode: %v Hz", r)
	}
	if r := modeRate(148500000, 2200, 1125, flagIntl); r != 120 {
		t.Fatalf("interlaced mode: %v Hz", r)
	}
}

func TestParseDPI(t *testing.T) {
	if d := parseDPI("Xcursor.size:\t24\nXft.dpi:\t144\n"); d != 144 {
		t.Fatalf("Xft.dpi 144 read as %d", d)
	}
	if d := parseDPI("Xcursor.size:\t24\n"); d != 96 {
		t.Fatalf("no Xft.dpi read as %d", d)
	}
}

func needX(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
}

// TestCaptureFrames grabs the primary monitor at 60 Hz and checks the
// frames are BGRX of the monitor's size with something drawn in them,
// and that the source paces them.
func TestCaptureFrames(t *testing.T) {
	needX(t)
	all, err := Displays()
	if err != nil || len(all) == 0 {
		t.Fatalf("displays %v: %v", all, err)
	}
	t.Logf("displays: %+v", all)
	src, err := Open("", 60)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	d := src.Display()
	drawMark(t, d)
	var took []time.Duration
	start := time.Now()
	const n = 30
	for i := range n {
		f, err := src.Next(time.Second)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Width != d.Width || f.Height != d.Height || f.Format != capture.FormatBGRA8 {
			t.Fatalf("frame %d: %dx%d %v", i, f.Width, f.Height, f.Format)
		}
		took = append(took, f.Acquired-f.Presented)
		if i == 2 {
			checkMark(t, f, d)
		}
		src.Release()
	}
	el := time.Since(start)
	slices.Sort(took)
	t.Logf("%s %dx%d@%.2f: %d frames in %v, XShmGetImage p50 %v max %v", d.ID, d.Width, d.Height, d.Refresh, n, el, took[n/2], took[n-1])
	if el < 400*time.Millisecond {
		t.Fatalf("%d frames at 60 Hz took %v: not paced", n, el)
	}
}

func TestOpenUnknownMonitor(t *testing.T) {
	needX(t)
	if _, err := Open("no-such-monitor", 60); !errors.Is(err, ErrNoDisplay) {
		t.Fatalf("got %v", err)
	}
}

func TestCursor(t *testing.T) {
	needX(t)
	x, y, err := Pointer()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Cursor(true)
	if err != nil {
		t.Skip(err)
	}
	t.Logf("pointer %d,%d; cursor %dx%d hot %d,%d serial %d at %d,%d", x, y, c.W, c.H, c.HotX, c.HotY, c.Serial, c.X, c.Y)
	if len(c.ARGB) != c.W*c.H {
		t.Fatalf("%d pixels for %dx%d", len(c.ARGB), c.W, c.H)
	}
}

// mark is a rectangle the test draws on the root window, in a colour no
// desktop background has.
var mark = struct{ x, y, w, h int }{37, 21, 64, 48}

const markRGB = 0xff20c0

// drawMark fills mark on the root window with markRGB on its own
// connection, as another client would draw.
func drawMark(t *testing.T, d capture.Display) {
	t.Helper()
	x, err := purego.Dlopen("libX11.so.6", purego.RTLD_NOW)
	if err != nil {
		t.Fatal(err)
	}
	sym := func(name string) uintptr {
		p, err := purego.Dlsym(x, name)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	gc := call(sym("XCreateGC"), c.dpy, c.root, 0, 0)
	call(sym("XSetForeground"), c.dpy, gc, markRGB)
	call(sym("XFillRectangle"), c.dpy, c.root, gc, uintptr(d.X+mark.x), uintptr(d.Y+mark.y), uintptr(mark.w), uintptr(mark.h))
	call(sym("XFreeGC"), c.dpy, gc)
	call(lib.xsync, c.dpy, 0)
}

// checkMark finds mark in the frame at its place, in BGRX, with the
// pixels around it not of its colour.
func checkMark(t *testing.T, f capture.Frame, d capture.Display) {
	t.Helper()
	b := capture.DMABufOf(f.Surface)
	px := func(x, y int) uint32 {
		return *(*uint32)(unsafe.Add(b.CPU[0], y*int(b.Pitches[0])+x*4)) & 0xffffff
	}
	all := unsafe.Slice((*byte)(b.CPU[0]), int(b.Pitches[0])*f.Height)
	if !slices.ContainsFunc(all, func(v byte) bool { return v != 0 }) {
		t.Fatalf("the frame of %s is all zero: a rootless X server (XWayland) keeps no root image", d.ID)
	}
	for _, p := range [][2]int{{mark.x, mark.y}, {mark.x + mark.w - 1, mark.y + mark.h - 1}, {mark.x + mark.w/2, mark.y + mark.h/2}} {
		if got := px(p[0], p[1]); got != markRGB {
			t.Fatalf("pixel %v: %06x, want %06x", p, got, markRGB)
		}
	}
	for _, p := range [][2]int{{mark.x - 1, mark.y}, {mark.x + mark.w, mark.y + mark.h}} {
		if px(p[0], p[1]) == markRGB {
			t.Fatalf("pixel %v outside the mark has its colour", p)
		}
	}
}
