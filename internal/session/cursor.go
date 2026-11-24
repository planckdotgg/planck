package session

import (
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/wire"
)

// cursorPoll is how often the host looks at its cursor: a game taking or
// letting go of the mouse reaches the client within a frame or two.
const cursorPoll = 16 * time.Millisecond

// cursorLoop tells the client how the host's cursor stands, at the start
// and each time it shows, hides, or an app captures or frees it. It ends at
// once where the platform cannot tell.
func (st *stream) cursorLoop() {
	read := st.h.cfg.ReadCursor
	if read == nil {
		read = cursor.Read
	}
	t := time.NewTicker(cursorPoll)
	defer t.Stop()
	var last wire.CursorState
	var warp warpCapture
	sent := false
	for {
		s, err := read()
		if errors.Is(err, cursor.ErrUnsupported) {
			return
		}
		if err == nil {
			st.mu.Lock()
			d := st.info.Display
			st.mu.Unlock()
			m := cursorMessage(s, cursor.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
			moves := st.h.absMoves.Load()
			t := st.h.absTarget.Load()
			m.Captured = warp.observe(m, moves, uint16(t>>16), uint16(t)) || m.Captured
			if !sent || m.Visible != last.Visible || m.Captured != last.Captured {
				if st.sess.SendControl(&m) == nil {
					last, sent = m, true
				}
			}
		}
		select {
		case <-st.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cursorMessage is s for the client, its place on the streamed display
// when it is on it.
func cursorMessage(s cursor.State, display cursor.Rect) wire.CursorState {
	m := wire.CursorState{Visible: s.Visible, Captured: s.Captured}
	m.X, m.Y, m.HasPos = cursor.Normalize(s.X, s.Y, display)
	return m
}

// Bounds of warpCapture, across the streamed display's 0..65535.
const (
	warpFar   = 65535 / 50
	warpNear  = 65535 / 100
	warpPolls = 3
)

// warpCapture tells a game that holds the mouse by warping its hidden
// cursor back to one place, without confining it, from a cursor hidden for
// any other reason: a page's cursor: none, a video player, typing. The
// client's absolute moves put a hidden cursor where they say, unless an
// app takes it back; warpCapture holds the mouse captured once warpPolls
// polls in a row, each after an absolute move, find the cursor at least
// warpFar from the move and within warpNear of one point inside the
// display, and lets it go when the cursor shows, is confined, or follows
// the moves warpPolls polls in a row. Only moves the host injected count.
type warpCapture struct {
	started bool
	abs     uint32
	streak  int
	ax, ay  uint16
	held    bool
}

// observe takes a poll of the cursor, m as the system reports it, and the
// count of absolute moves injected so far with the last one's target, and
// reports whether the cursor is held by warping.
func (w *warpCapture) observe(m wire.CursorState, abs uint32, tx, ty uint16) bool {
	moved := w.started && abs != w.abs
	w.started, w.abs = true, abs
	if m.Visible || m.Captured || !m.HasPos {
		w.held, w.streak = false, 0
		return false
	}
	if !moved {
		return w.held
	}
	if w.held {
		if near(m.X, m.Y, tx, ty, warpNear) {
			w.streak++
		} else {
			w.streak = 0
		}
		if w.streak >= warpPolls {
			w.held, w.streak = false, 0
		}
		return w.held
	}
	if near(m.X, m.Y, tx, ty, warpFar) || !inside(m.X, warpNear) || !inside(m.Y, warpNear) {
		w.streak = 0
		return false
	}
	if w.streak == 0 || !near(m.X, m.Y, w.ax, w.ay, warpNear) {
		w.streak, w.ax, w.ay = 0, m.X, m.Y
	}
	w.streak++
	if w.streak >= warpPolls {
		w.held, w.streak = true, 0
	}
	return w.held
}

// near reports whether a and b are within d of each other on both axes.
func near(ax, ay, bx, by uint16, d int) bool {
	return abs(int(ax)-int(bx)) < d && abs(int(ay)-int(by)) < d
}

// inside reports whether v is at least margin from either edge.
func inside(v uint16, margin int) bool { return int(v) >= margin && int(v) <= 65535-margin }
