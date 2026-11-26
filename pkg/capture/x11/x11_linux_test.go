//go:build linux && !android && (amd64 || arm64)

package x11

import (
	"errors"
	"slices"
	"testing"
	"time"
	"unsafe"

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
		if i == 0 {
			b := capture.DMABufOf(f.Surface)
			row := unsafe.Slice((*byte)(b.CPU[0]), int(b.Pitches[0])*f.Height)
			if !slices.ContainsFunc(row, func(v byte) bool { return v != 0 }) {
				t.Fatal("first frame is all zero")
			}
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
