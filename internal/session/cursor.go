package session

import (
	"errors"
	"slices"
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
	var report cursorReport
	var warp warpCapture
	var trace cursorTrace
	var desk cursor.Rect
	settle := 0
	for {
		epoch := st.h.itrace.epoch()
		s, err := read()
		if errors.Is(err, cursor.ErrUnsupported) {
			return
		}
		if err == nil && (s.Desktop != desk || st.areaStale.Swap(false)) {
			desk, settle = s.Desktop, areaSettlePolls
			st.syncInputArea()
		} else if settle > 0 && !st.areaSettled(desk) {
			settle--
			st.syncInputArea()
		}
		if err == nil && shapes != nil && s.Handle != 0 && s.Handle != handle {
			if sh, err := readShape(s); err == nil {
				if id := sh.Hash(); id == shape || shapes.send(sh, id, st.sess.SendControl) == nil {
					handle, shape = s.Handle, id
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
			warped := warp.observe(m, moves, uint16(target>>16), uint16(target))
			m.Captured = warped || m.Captured
			act := report.decide(m)
			if act == reportEmit && report.started && report.last.Captured && !m.Captured && !m.HasPos {
				m.X, m.Y, m.HasPos = clampOnto(s, cursor.Rect{X: d.X, Y: d.Y, W: d.W, H: d.H})
			}
			if act == reportEmit {
				if st.sess.SendControl(&m) == nil {
					report.sent(m)
				} else {
					act = reportSendFailed
				}
			}
			if st.h.itrace.on() {
				trace.poll(st.h, epoch, s, m, warped, act)
			}
		}
		select {
		case <-st.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// areaSettlePolls is how many polls after the system's desktop or the
// picture changed the cursor loop goes on asking where the captured
// display sits, while its answer lags the desktop the cursor reports or
// the picture's size.
const areaSettlePolls = 60

// syncInputArea points absolute moves, and the cursor's place, at where
// the captured display sits now, when that is not where the pipe found
// it: a full-screen game's mode switch the pipe recovered from in place,
// or one it opened across before the layout settled.
func (st *stream) syncInputArea() {
	st.mu.Lock()
	id := st.info.DisplayID
	st.mu.Unlock()
	if id == "" {
		return
	}
	area := st.h.cfg.InputArea
	if area == nil {
		area = liveInputArea
	}
	d, dk, err := area(id)
	if err != nil || d.W <= 0 || d.H <= 0 {
		return
	}
	st.areaMu.Lock()
	st.mu.Lock()
	if st.info.DisplayID != id || st.info.Display == d && st.info.Desktop == dk {
		st.mu.Unlock()
		st.areaMu.Unlock()
		return
	}
	st.info.Display, st.info.Desktop = d, dk
	info := st.info
	st.mu.Unlock()
	st.h.targetInput(info)
	st.areaMu.Unlock()
	st.traceMeta()
}

// areaSettled reports whether the input area has caught up with the
// system: its desktop is the one the cursor reports, where the cursor
// reports one, and its display is the picture's size. Other displays can
// keep the desktop's bounds through the captured one's mode switch.
func (st *stream) areaSettled(desk cursor.Rect) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.info.DisplayID == "" {
		return true
	}
	if d := st.info.Display; st.info.Width > 0 && st.info.Height > 0 && (d.W != st.info.Width || d.H != st.info.Height) {
		return false
	}
	dk := st.info.Desktop
	return desk.W <= 0 || desk.H <= 0 || dk.X == desk.X && dk.Y == desk.Y && dk.W == desk.W && dk.H == desk.H
}

// cursorReport decides which polls the client hears of: the first, and
// each that shows, hides, captures or frees the cursor. A cursor hiding
// alone waits a poll: an app changing its cursor's shape can show none for
// an instant, and each hide takes the client's pointer relative and back.
type cursorReport struct {
	last          wire.CursorState
	started, held bool
}

func (r *cursorReport) due(m wire.CursorState) bool { return r.decide(m) == reportEmit }

// reportAction is what cursorReport made of a poll.
type reportAction uint8

const (
	// reportSame changed nothing the client hears of.
	reportSame reportAction = iota
	reportEmit
	// reportHoldHide waits a poll before telling of a hide.
	reportHoldHide
	// reportDropFlicker is the poll after a held hide, the cursor back.
	reportDropFlicker
	// reportSendFailed was due, and its message did not go.
	reportSendFailed
)

func (r *cursorReport) decide(m wire.CursorState) reportAction {
	if r.started && m.Visible == r.last.Visible && m.Captured == r.last.Captured {
		held := r.held
		r.held = false
		if held {
			return reportDropFlicker
		}
		return reportSame
	}
	if r.started && r.last.Visible && !m.Visible && m.Captured == r.last.Captured && !r.held {
		r.held = true
		return reportHoldHide
	}
	return reportEmit
}

func (r *cursorReport) sent(m wire.CursorState) { r.last, r.started, r.held = m, true, false }

// shapeCache is the last wire.CursorCacheSize cursor shapes of a session,
// the most recent first. The client's takes each message the host's makes
// in the same order, so the host's shapes are always the client's first
// ones, even after the host's starts afresh.
type shapeCache struct{ shapes []wire.CursorShape }

// send sends the message that shows sh, named id, and only once send
// takes it does the shape become the most recent: a message the client
// never gets leaves the caches alike.
func (c *shapeCache) send(sh cursor.Shape, id uint64, send func(wire.Message) error) error {
	m, err := c.message(sh, id)
	if err != nil {
		return err
	}
	if err := send(&m); err != nil {
		return err
	}
	c.resolve(m)
	return nil
}

// message is the message that shows sh, named id: a reference to it when
// it was sent before, else the whole shape, halved until it fits a control
// frame, DPI and hotspot with it, so that it keeps its size on the client.
func (c *shapeCache) message(sh cursor.Shape, id uint64) (wire.CursorShape, error) {
	if c.has(id) {
		return wire.CursorShape{ID: id}, nil
	}
	for {
		m := wire.CursorShape{
			ID: id, Width: uint16(sh.W), Height: uint16(sh.H), HotX: uint16(sh.HotX), HotY: uint16(sh.HotY),
			DPI: uint16(min(max(sh.DPI, 1), 0xffff)), RGBA: sh.RGBA,
		}
		_, err := wire.AppendMessage(nil, &m)
		if !errors.Is(err, wire.ErrTooLarge) || max(sh.W, sh.H) <= 1 {
			return m, err
		}
		sh = cursor.Fit(sh, max(sh.W, sh.H)/2)
	}
}

func (c *shapeCache) has(id uint64) bool {
	return slices.ContainsFunc(c.shapes, func(s wire.CursorShape) bool { return s.ID == id })
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

// clampOnto is s's place moved onto display at its nearest point. A
// release of a captured cursor off the display carries it: the client puts
// its own cursor there, where its next absolute move starts from, rather
// than where relative mode held it, the picture's centre.
func clampOnto(s cursor.State, display cursor.Rect) (x, y uint16, ok bool) {
	if display.W <= 0 || display.H <= 0 {
		return 0, 0, false
	}
	cx := min(max(s.X, display.X), display.X+display.W-1)
	cy := min(max(s.Y, display.Y), display.Y+display.H-1)
	return cursor.Normalize(cx, cy, display)
}

// Bounds of warpCapture, across the streamed display's 0..65535.
const (
	warpFar   = 65535 / 50
	warpNear  = 65535 / 100
	warpPolls = 3
	// warpSweep is the least the moves span on an axis, the cursor
	// staying still on it, that tells a warp from a follower.
	warpSweep = warpNear / 2
	// warpStallPolls is how long a cursor held where it last followed a
	// move takes to count: the host's input stalling looks like a warp
	// back to there for as long as the stall lasts.
	warpStallPolls = 16
)

// warpCapture tells a game that holds the mouse by warping its hidden
// cursor back to one place, without confining it, from a cursor hidden for
// any other reason: a page's cursor: none, a video player, typing. The
// client's absolute moves put a hidden cursor where they say, unless an
// app takes it back; warpCapture holds the mouse captured once warpPolls
// polls in a row, each after an absolute move, find the cursor at least
// warpFar from the move and within warpNear of one point inside the
// display, while the moves themselves spread, and lets it go when
// the cursor shows, is confined, or follows the moves warpPolls polls in a
// row. Only moves the host injected count. A point the cursor last went to
// with a move takes warpStallPolls.
//
// The spread tells a warp from a cursor that follows each move a fixed
// distance or fraction off, as one placed by a stale display does: that
// cursor goes with the moves. The moves spread when on one axis they span
// warpFar, which a follower cannot do and stay within warpNear of its
// point, or span warpSweep while the cursor moves under a quarter of
// that, as a follower's at least half the moves' rate does not; a warped
// one stays put however narrow the moves' band.
type warpCapture struct {
	started bool
	abs     uint32
	streak  int
	ax, ay  uint16
	// span is the box of the streak's move targets, at the box of the
	// cursor's places.
	span, at [4]uint16
	held     bool
	// followed is whether fx, fy is where the cursor last followed a move.
	followed bool
	fx, fy   uint16
}

// observe takes a poll of the cursor, m as the system reports it, and the
// count of absolute moves injected so far with the last one's target, and
// reports whether the cursor is held by warping.
func (w *warpCapture) observe(m wire.CursorState, abs uint32, tx, ty uint16) bool {
	moved := w.started && abs != w.abs
	w.started, w.abs = true, abs
	if m.Visible || m.Captured || !m.HasPos {
		w.held, w.streak, w.followed = false, 0, false
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
	if near(m.X, m.Y, tx, ty, warpFar) {
		w.streak, w.followed, w.fx, w.fy = 0, true, m.X, m.Y
		return false
	}
	if !inside(m.X, warpNear) || !inside(m.Y, warpNear) {
		w.streak = 0
		return false
	}
	if w.streak == 0 || !near(m.X, m.Y, w.ax, w.ay, warpNear) {
		w.streak, w.ax, w.ay = 0, m.X, m.Y
		w.span = [4]uint16{tx, ty, tx, ty}
		w.at = [4]uint16{m.X, m.Y, m.X, m.Y}
	}
	w.streak++
	w.span = grow(w.span, tx, ty)
	w.at = grow(w.at, m.X, m.Y)
	need := warpPolls
	if w.followed && near(w.ax, w.ay, w.fx, w.fy, warpNear) {
		need = warpStallPolls
	}
	if w.streak >= need && (spreads(w.span[0], w.span[2], w.at[0], w.at[2]) || spreads(w.span[1], w.span[3], w.at[1], w.at[3])) {
		w.held, w.streak = true, 0
	}
	return w.held
}

// grow is box b, as x0, y0, x1, y1, grown to take in x, y.
func grow(b [4]uint16, x, y uint16) [4]uint16 {
	return [4]uint16{min(b[0], x), min(b[1], y), max(b[2], x), max(b[3], y)}
}

// spreads reports whether moves spanning t0..t1 on an axis, the cursor
// spanning c0..c1 on it, spread as warpCapture has it.
func spreads(t0, t1, c0, c1 uint16) bool {
	t, c := int(t1)-int(t0), int(c1)-int(c0)
	return t >= warpFar || t >= warpSweep && c*4 <= t
}

// near reports whether a and b are within d of each other on both axes.
func near(ax, ay, bx, by uint16, d int) bool {
	return abs(int(ax)-int(bx)) < d && abs(int(ay)-int(by)) < d
}

// inside reports whether v is at least margin from either edge.
func inside(v uint16, margin int) bool { return int(v) >= margin && int(v) <= 65535-margin }
