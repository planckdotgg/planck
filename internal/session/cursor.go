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
	readShape := st.h.cfg.ReadCursorShape
	if readShape == nil {
		readShape = cursor.ReadShape
	}
	var shapes *shapeCache
	if st.sess.PeerFeatures()&wire.FeatureCursorShape != 0 {
		shapes = &shapeCache{}
	}
	var handle uintptr
	var shape uint64
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
		if err == nil && shapes != nil && s.Handle != 0 && s.Handle != handle {
			if sh, err := readShape(s); err == nil {
				handle = s.Handle
				if id := sh.Hash(); id != shape {
					m := shapes.message(sh, id)
					if st.sess.SendControl(&m) == nil {
						shape = id
					}
				}
			}
		}
		if err == nil {
			st.mu.Lock()
			d := st.info.Display
			st.mu.Unlock()
			m := cursorMessage(s, cursor.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
			moves := st.h.absMoves.Load()
			target := st.h.absTarget.Load()
			m.Captured = warp.observe(m, moves, uint16(target>>16), uint16(target)) || m.Captured
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

// shapeCache is the last wire.CursorCacheSize cursor shapes of a session,
// the most recent first. The client's takes each message the host's makes
// in the same order, so the host's shapes are always the client's first
// ones, even after the host's starts afresh.
type shapeCache struct{ shapes []wire.CursorShape }

// message is the message that shows sh, named id: a reference to it when
// it was sent before, else the whole shape. Either way it becomes the
// most recent.
func (c *shapeCache) message(sh cursor.Shape, id uint64) wire.CursorShape {
	if _, ok := c.use(id); ok {
		return wire.CursorShape{ID: id}
	}
	m := wire.CursorShape{
		ID: id, Width: uint16(sh.W), Height: uint16(sh.H), HotX: uint16(sh.HotX), HotY: uint16(sh.HotY),
		DPI: uint16(min(max(sh.DPI, 1), 0xffff)), RGBA: sh.RGBA,
	}
	c.add(m)
	return m
}

// resolve is the whole shape m shows, from the cache when m refers to one;
// ok is false for a reference to a shape the cache lacks.
func (c *shapeCache) resolve(m wire.CursorShape) (wire.CursorShape, bool) {
	if m.Ref() {
		return c.use(m.ID)
	}
	c.add(m)
	return m, true
}

func (c *shapeCache) use(id uint64) (wire.CursorShape, bool) {
	for i, s := range c.shapes {
		if s.ID == id {
			copy(c.shapes[1:i+1], c.shapes[:i])
			c.shapes[0] = s
			return s, true
		}
	}
	return wire.CursorShape{}, false
}

func (c *shapeCache) add(m wire.CursorShape) {
	c.shapes = append(c.shapes, wire.CursorShape{})
	copy(c.shapes[1:], c.shapes)
	c.shapes[0] = m
	if len(c.shapes) > wire.CursorCacheSize {
		c.shapes[len(c.shapes)-1] = wire.CursorShape{}
		c.shapes = c.shapes[:wire.CursorCacheSize]
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
