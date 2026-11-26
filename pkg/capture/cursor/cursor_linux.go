//go:build linux && !android && (amd64 || arm64)

package cursor

import (
	"sync"

	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/capture/x11"
)

// Under X11, XFixes gives the cursor's image and serial and XQueryPointer
// its place. Under Wayland the portal draws the cursor into the frames
// and no client may read it. X11 says nothing of grabs, so Captured is
// never set.
var visible struct {
	mu     sync.Mutex
	serial uint64
	shows  bool
}

func read() (State, error) {
	if display.Backend() != display.BackendX11 {
		return State{}, ErrUnsupported
	}
	x, y, err := x11.Pointer()
	if err != nil {
		return State{}, err
	}
	s := State{X: x, Y: y}
	visible.mu.Lock()
	defer visible.mu.Unlock()
	c, err := x11.Cursor(false)
	if err != nil {
		return s, err
	}
	if c.Serial != visible.serial || visible.serial == 0 {
		// A hidden cursor is one of fully transparent pixels, under its
		// own serial: look at its pixels once per serial.
		full, err := x11.Cursor(true)
		if err != nil {
			return s, err
		}
		visible.serial, visible.shows = full.Serial, anyAlpha(full.ARGB)
	}
	s.Visible = visible.shows
	if s.Visible {
		s.Handle = uintptr(visible.serial) + 1
	}
	return s, nil
}

func anyAlpha(argb []uint32) bool {
	for _, p := range argb {
		if p>>24 != 0 {
			return true
		}
	}
	return false
}

var dpi = sync.OnceValue(x11.DPI)

func readShape(State) (Shape, error) {
	c, err := x11.Cursor(true)
	if err != nil {
		return Shape{}, err
	}
	return fit(Shape{W: c.W, H: c.H, HotX: c.HotX, HotY: c.HotY, DPI: dpi(), RGBA: straightRGBA(c.ARGB)}), nil
}

// straightRGBA makes RGBA of XFixes' premultiplied ARGB words.
func straightRGBA(argb []uint32) []byte {
	out := make([]byte, len(argb)*4)
	for i, p := range argb {
		a := p >> 24
		r, g, b := p>>16&0xff, p>>8&0xff, p&0xff
		if a != 0 && a != 0xff {
			r, g, b = min(r*255/a, 255), min(g*255/a, 255), min(b*255/a, 255)
		}
		out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = byte(r), byte(g), byte(b), byte(a)
	}
	return out
}
