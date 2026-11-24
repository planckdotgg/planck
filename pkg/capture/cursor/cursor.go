// Package cursor reads the host's system cursor: whether it shows, whether
// an app holds the mouse, and where it is.
package cursor

import "errors"

// ErrUnsupported is what Read returns where the platform cannot tell.
var ErrUnsupported = errors.New("cursor: not supported on this platform")

// Rect is a rectangle of the desktop, in pixels.
type Rect struct{ X, Y, W, H int }

// State is the system cursor.
type State struct {
	Visible bool
	// Captured is set while an app holds the mouse, as a game does: the
	// cursor is hidden, or confined to less than the whole desktop.
	Captured bool
	// X and Y are the cursor's place on the desktop, in physical pixels.
	X, Y int
}

// Read reports the system cursor now.
func Read() (State, error) { return read() }

// classify makes a State of what the system reports: whether the cursor
// shows, the rectangle it is confined to, and the desktop's.
func classify(showing bool, clip, desktop Rect) State {
	confined := clip.W > 0 && clip.H > 0 && desktop.W > 0 && desktop.H > 0 &&
		(clip.X > desktop.X || clip.Y > desktop.Y || clip.X+clip.W < desktop.X+desktop.W || clip.Y+clip.H < desktop.Y+desktop.H)
	return State{Visible: showing, Captured: !showing || confined}
}

// Normalize maps a desktop point onto 0..65535 across display, as absolute
// pointer moves address it; ok is false off the display.
func Normalize(x, y int, display Rect) (nx, ny uint16, ok bool) {
	if display.W <= 0 || display.H <= 0 || x < display.X || y < display.Y || x >= display.X+display.W || y >= display.Y+display.H {
		return 0, 0, false
	}
	span := func(v, n int) uint16 {
		if n <= 1 {
			return 0
		}
		return uint16(v * 65535 / (n - 1))
	}
	return span(x-display.X, display.W), span(y-display.Y, display.H), true
}
