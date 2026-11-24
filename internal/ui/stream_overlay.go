package ui

import (
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// The floating button's geometry and timings, in DIPs.
const (
	btnSize     = 44
	btnDragSize = 48
	// btnInset holds the floating button inside the view; a popped-out
	// button sits popGap off its edge.
	btnInset = 12
	popGap   = 6
	// dockReach is how near an edge the button's centre must come to rest
	// for it to dock there.
	dockReach = 56
	// dockHysteresis is how much nearer than the lit edge another must be,
	// or how far past dockReach the lit edge must go, for a drag to light
	// another or none.
	dockHysteresis = 12
	// tabLong and tabShort size the docked tab, hitLong and hitShort its
	// hit zone; a tab stays dockCorner from the view's corners.
	tabLong, tabShort = 40, 8
	hitLong, hitShort = 72, 24
	dockCorner        = 16
	// idleReach is how near the pointer keeps the button from dimming.
	idleReach = 120
)

const (
	dwellTime  = 80 * time.Millisecond
	popOutTime = 140 * time.Millisecond
	popBack    = 110 * time.Millisecond
	meltTime   = 160 * time.Millisecond
	lingerTime = 900 * time.Millisecond
	idleTime   = 6 * time.Second
	dimTime    = 160 * time.Millisecond
	toastIn    = 160 * time.Millisecond
	toastHold  = 2400 * time.Millisecond
	toastOut   = 110 * time.Millisecond
	tipFirst   = 4 * time.Second
)

var easeOverlay = mui.CubicBezier(0.16, 1, 0.3, 1)

// overlay is the stream overlay's state, read and written by the view
// alone.
type overlay struct {
	hidden   bool
	toast    toast
	tipUntil time.Time
	// cursorLocked is whether the mouse lock was on last frame, and
	// cursorTold whether this stream's toast said how to release it.
	cursorLocked, cursorTold bool
	// sysKeysTold is the system key capture's problem a toast told of.
	sysKeysTold string
	// btn is the button's top-left corner in the view while it floats or
	// is popped out of its dock; off is how far a drag moved it from
	// there, folded into btn once it comes to rest, and fold is set for
	// the frame that does it. btnAt is where it floats when not docked,
	// which btn follows while it is at rest.
	btn      mui.Point
	btnAt    Placement
	off      mui.Point
	fold     bool
	dock     DockEdge
	dockFrac float32
	popped   bool
	// held is whether the button was pressed, dragged or focused last
	// frame, which keeps it out of its dock.
	held bool
	// morph moves the button between its bubble and its tab.
	morph tween
	// dwellAt is when the pointer went into the hit zone, leftAt when it
	// left the button and the hit zone, nearAt when it last moved near the
	// button.
	dwellAt, leftAt, nearAt time.Time
	px, py                  float32
	pointerOver             bool
	// released is set by snap as a drag lets go, with where it docks.
	released    bool
	releaseDock DockEdge
	releaseFrac float32
	// target is the edge and place the dragged button's dock target lights,
	// where letting go docks it.
	target      DockEdge
	targetFrac  float32
	snapFn      func(mui.Point) mui.Point
	vw, vh      float32
	menuOpen    bool
	menuWasOpen bool
	metrics     metricsWindow
	saved       OverlayLayout
}

// toast is a short note across the top of the stream: a title, and the
// overlay key that undoes it between lead and tail.
type toast struct {
	at                     time.Time
	title, lead, key, tail string
}

// tween is a rect moving from one place to another over dur.
type tween struct {
	from, to mui.Rect
	at       time.Time
	dur      time.Duration
}

func (t tween) active(now time.Time) bool { return !t.at.IsZero() && now.Sub(t.at) < t.dur }

func (t tween) progress(now time.Time) float32 {
	if t.dur <= 0 {
		return 1
	}
	k := float32(now.Sub(t.at)) / float32(t.dur)
	return easeOverlay(min(max(k, 0), 1))
}

func (t tween) rect(now time.Time) mui.Rect {
	k := t.progress(now)
	lerp := func(a, b float32) float32 { return a + (b-a)*k }
	return mui.Rect{X: lerp(t.from.X, t.to.X), Y: lerp(t.from.Y, t.to.Y), W: lerp(t.from.W, t.to.W), H: lerp(t.from.H, t.to.H)}
}

// begin sets the overlay up for a new stream from the layout kept: shown,
// whatever it was, and the menu closed.
func (o *overlay) begin(l OverlayLayout, now time.Time, vw, vh float32) {
	*o = overlay{vw: vw, vh: vh, nearAt: now, saved: l, metrics: o.metrics}
	o.snapFn = o.snap
	o.metrics.begin(l, vw, vh)
	o.btnAt = Placement{AnchorX: AnchorStart, AnchorY: AnchorCenter, OffsetX: 24}
	switch {
	case !l.Placed:
		o.tipUntil = now.Add(tipFirst)
	case l.ButtonPlace.set():
		o.btnAt = l.ButtonPlace
	case l.ButtonX != 0 || l.ButtonY != 0:
		b := btnBounds(vw, vh)
		p := clampPoint(mui.Point{X: round32(l.ButtonX*vw - btnSize/2), Y: round32(l.ButtonY*vh - btnSize/2)}, b)
		o.btnAt = placementAt(p, btnSize, btnSize, vw, vh, b, btnInset)
	}
	if l.Placed {
		o.dock, o.dockFrac = l.Dock, l.DockFrac
	}
	o.btn = o.floatAt()
}

// btnBounds is where the floating button's top-left corner may go.
func btnBounds(vw, vh float32) mui.Rect {
	return mui.Rect{X: btnInset, Y: btnInset, W: max(vw-2*btnInset-btnSize, 0), H: max(vh-2*btnInset-btnSize, 0)}
}

// floatAt is where the button floats at rest: at btnAt, held inside the
// view.
func (o *overlay) floatAt() mui.Point {
	return clampPoint(o.btnAt.resolve(o.vw, o.vh, btnSize, btnSize), btnBounds(o.vw, o.vh))
}

// layout is what the next stream starts from. It depends on nothing the
// window's size changes, so a resize stores nothing.
func (o *overlay) layout() OverlayLayout {
	l := OverlayLayout{Placed: true, Dock: o.dock, DockFrac: o.dockFrac, ButtonPlace: o.btnAt}
	o.metrics.store(&l)
	return l
}

// dockFor is the edge a button resting with its centre at cx, cy docks
// to, and where along it: left and right before top and bottom.
func dockFor(cx, cy, vw, vh float32) (DockEdge, float32) {
	l, r, t, b := cx, vw-cx, cy, vh-cy
	e := DockNone
	switch {
	case min(l, r) <= dockReach && l <= r:
		e = DockLeft
	case min(l, r) <= dockReach:
		e = DockRight
	case min(t, b) <= dockReach && t <= b:
		e = DockTop
	case min(t, b) <= dockReach:
		e = DockBottom
	}
	return e, edgeFrac(e, cx, cy, vw, vh)
}

// dockCandidate is dockFor for a button being dragged whose target lights
// prev: prev keeps the lead until another edge is dockHysteresis nearer, or
// it is dockHysteresis past dockReach, so that a corner lights one edge.
func dockCandidate(cx, cy, vw, vh float32, prev DockEdge) (DockEdge, float32) {
	e, _ := dockFor(cx, cy, vw, vh)
	if prev != DockNone && e != prev {
		d := edgeDist(prev, cx, cy, vw, vh)
		if d <= dockReach+dockHysteresis && (e == DockNone || edgeDist(e, cx, cy, vw, vh) > d-dockHysteresis) {
			e = prev
		}
	}
	return e, edgeFrac(e, cx, cy, vw, vh)
}

// edgeDist is how far cx, cy is from edge e.
func edgeDist(e DockEdge, cx, cy, vw, vh float32) float32 {
	switch e {
	case DockLeft:
		return cx
	case DockRight:
		return vw - cx
	case DockTop:
		return cy
	case DockBottom:
		return vh - cy
	}
	return 0
}

// edgeFrac is where along edge e a tab level with cx, cy rests.
func edgeFrac(e DockEdge, cx, cy, vw, vh float32) float32 {
	switch e {
	case DockLeft, DockRight:
		return dockFrac(cy, vh)
	case DockTop, DockBottom:
		return dockFrac(cx, vw)
	}
	return 0
}

// dockFrac is where a tab centred at v along an edge of length n rests, as
// a fraction of n, dockCorner from the corners.
func dockFrac(v, n float32) float32 {
	if n <= 0 {
		return 0.5
	}
	lo := float32(dockCorner + tabLong/2)
	return min(max(v, lo), max(n-lo, lo)) / n
}

// snap docks the button where its target shows, or, with none lit, where
// the throw to p carries it.
func (o *overlay) snap(p mui.Point) mui.Point {
	o.released = true
	if o.target != DockNone {
		o.releaseDock, o.releaseFrac = o.target, o.targetFrac
		return p
	}
	o.releaseDock, o.releaseFrac = dockFor(o.btn.X+p.X+btnSize/2, o.btn.Y+p.Y+btnSize/2, o.vw, o.vh)
	return p
}

// dockGeom is where the docked tab, its hit zone and the popped-out button
// are in the view.
func (o *overlay) dockGeom() (tab, hit, out mui.Rect) {
	return dockRects(o.dock, o.dockFrac, o.vw, o.vh)
}

func dockRects(e DockEdge, frac, vw, vh float32) (tab, hit, out mui.Rect) {
	switch e {
	case DockLeft, DockRight:
		c := frac * vh
		tab = mui.Rect{Y: c - tabLong/2, W: tabShort, H: tabLong}
		hit = mui.Rect{Y: c - hitLong/2, W: hitShort, H: hitLong}
		out = mui.Rect{X: popGap, Y: c - btnSize/2, W: btnSize, H: btnSize}
		if e == DockRight {
			tab.X, hit.X, out.X = vw-tabShort, vw-hitShort, vw-popGap-btnSize
		}
	case DockTop, DockBottom:
		c := frac * vw
		tab = mui.Rect{X: c - tabLong/2, W: tabLong, H: tabShort}
		hit = mui.Rect{X: c - hitLong/2, W: hitLong, H: hitShort}
		out = mui.Rect{X: c - btnSize/2, Y: popGap, W: btnSize, H: btnSize}
		if e == DockBottom {
			tab.Y, hit.Y, out.Y = vh-tabShort, vh-hitShort, vh-popGap-btnSize
		}
	}
	return tab, hit, out
}

func inside(r mui.Rect, x, y float32) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

func near(r mui.Rect, x, y, d float32) bool {
	return inside(mui.Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}, x, y)
}

// overlayView draws the overlay over the stream in well: the metrics
// window, the button or its tab, and the menu. While hidden it draws
// nothing but the toast saying so, and the stream gets every press.
func (a *App) overlayView(c *mui.Context, well mui.Element, tel Telemetry) {
	o := &a.ovl
	now := c.Now()
	o.vw, o.vh = c.Size()
	a.overlayShortcuts(c)
	a.watchCursorLock()
	a.watchSystemKeys()
	if o.hidden {
		a.toastView(c, now)
		a.noteMenu(false)
		a.noteOverlay(now.Before(o.toast.at.Add(toastIn + toastHold + toastOut)))
		return
	}
	a.noteOverlay(true)
	x, y, over := well.PointerPosition()
	if over && (x != o.px || y != o.py || !o.pointerOver) {
		if near(o.bubbleRect(), x, y, idleReach) {
			o.nearAt = now
		}
	}
	o.px, o.py, o.pointerOver = x, y, over
	mui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).PassThrough().Children(func() {
		if o.metrics.shown {
			a.metricsView(c, tel, now)
		}
		btn := a.overlayButton(c, now)
		a.streamMenu(c, tel, btn)
	})
	a.noteMenu(o.menuOpen)
	a.toastView(c, now)
	if l := o.layout(); l != o.saved && !o.moving(now) {
		o.saved = l
		if err := a.backend.SaveOverlayLayout(l); err != nil {
			a.mu.Lock()
			a.settingsErr = err.Error()
			a.mu.Unlock()
		}
	}
}

// moving reports whether the button or the window is on its way
// somewhere, so the layout is not stored mid-move.
func (o *overlay) moving(now time.Time) bool {
	return o.morph.active(now) || o.metrics.dragging || o.off != (mui.Point{})
}

// bubbleRect is where the round button is, or would pop out to.
func (o *overlay) bubbleRect() mui.Rect {
	if o.dock != DockNone {
		_, _, out := o.dockGeom()
		return out
	}
	return mui.Rect{X: o.btn.X, Y: o.btn.Y, W: btnSize, H: btnSize}
}

// overlayButton draws the button: floating, docked as a tab, popping out
// of its tab or melting into it. It returns the round button, nil while
// only the tab shows.
func (a *App) overlayButton(c *mui.Context, now time.Time) mui.Element {
	o := &a.ovl
	if o.dock == DockNone {
		return a.bubble(c, now)
	}
	tab, hit, out := o.dockGeom()
	onHit := o.pointerOver && inside(hit, o.px, o.py)
	onOut := o.pointerOver && o.popped && inside(out, o.px, o.py)
	switch {
	case !onHit:
		o.dwellAt = time.Time{}
	case o.dwellAt.IsZero():
		o.dwellAt = now
	}
	if !o.popped && !o.dwellAt.IsZero() {
		if left := o.dwellAt.Add(dwellTime).Sub(now); left > 0 {
			c.After(left)
		} else {
			o.popOut(now, a.motion(c, popOutTime), tab, out)
		}
	}
	if o.menuOpen && !o.popped {
		o.popOut(now, a.motion(c, popOutTime), tab, out)
	}
	if o.popped {
		held := onHit || onOut || o.menuOpen || o.held
		switch {
		case held:
			o.leftAt = time.Time{}
		case o.leftAt.IsZero():
			o.leftAt = now
		}
		if !o.leftAt.IsZero() {
			if left := o.leftAt.Add(lingerTime).Sub(now); left > 0 {
				c.After(left)
			} else {
				o.popped, o.leftAt = false, time.Time{}
				o.morph = tween{from: out, to: tab, at: now, dur: a.motion(c, popBack)}
			}
		}
	}

	hitZone := mui.ButtonBase(c.Key("overlay-hit-zone")).Absolute().Left(hit.X).Top(hit.Y).Size(hit.W, hit.H).
		Label("Show stream controls").TabStop(false)
	if hitZone.Clicked() {
		o.popOut(now, a.motion(c, popOutTime), tab, out)
		o.menuOpen = true
	}
	var btn mui.Element
	switch {
	case o.morph.active(now):
		a.morphBox(c, now)
	case o.popped:
		a.traceLine(c, tab)
		btn = a.bubble(c, now)
	default:
		a.dockTab(c, tab)
	}
	return btn
}

func (o *overlay) popOut(now time.Time, d time.Duration, tab, out mui.Rect) {
	if o.popped {
		return
	}
	o.popped, o.leftAt = true, time.Time{}
	o.btn = mui.Point{X: out.X, Y: out.Y}
	o.morph = tween{from: tab, to: out, at: now, dur: d}
}

// motion is d, or nothing while the desktop asks for less motion.
func (a *App) motion(c *mui.Context, d time.Duration) time.Duration {
	if c.Preferences().ReduceMotion {
		return 0
	}
	return d
}

// bubble is the round button, which the pointer drags anywhere in the
// view and throws into an edge to dock it.
func (a *App) bubble(c *mui.Context, now time.Time) mui.Element {
	o := &a.ovl
	b := mui.ButtonBase(c.Key("overlay-button")).Label("Stream controls").Description(a.shortcut(false, "O")).
		Expanded(o.menuOpen).Cursor(mui.CursorPointer)
	dragging := b.Dragging()
	pressed := b.Pressed() || dragging
	if !pressed {
		o.menuWasOpen = o.menuOpen
	}
	inset := float32(btnInset)
	if o.dock != DockNone {
		inset = popGap
		if !pressed && o.off == (mui.Point{}) {
			_, _, out := o.dockGeom()
			o.btn = mui.Point{X: out.X, Y: out.Y}
		}
	} else if !pressed && o.off == (mui.Point{}) {
		o.btn = o.floatAt()
	}
	// The button sits at btn, where AttachTo sees it, and drags by off.
	bounds := mui.Rect{X: inset - o.btn.X, Y: inset - o.btn.Y, W: max(o.vw-2*inset-btnSize, 0), H: max(o.vh-2*inset-btnSize, 0)}
	anim := mui.Animation{}
	if o.fold {
		anim, o.fold = mui.Animation{Duration: time.Nanosecond}, false
	}
	size, radius, mark := float32(btnSize), float32(12), [2]float32{13, 20}
	if dragging {
		size, radius, mark = btnDragSize, 13, [2]float32{14, 22}
		b.Margin(-2, 0, 0, -2)
	}
	b.Absolute().Left(o.btn.X).Top(o.btn.Y).Size(size, size).Radius(radius).AlignItems(mui.Center).Justify(mui.Center)
	b.Draggable(&o.off, mui.DraggableOptions{Bounds: &bounds, Rigid: true, Snap: o.snapFn, Animation: anim})

	hover := b.Hovered() || b.Focused()
	o.held = pressed || b.Focused()
	dim := !hover && !dragging && !o.menuOpen && o.dock == DockNone
	idleFor := now.Sub(o.nearAt) - idleTime
	switch {
	case dragging:
		b.Background(theme.HUDGlass).Border(1, theme.RoleText.Alpha(0.22)).Shadow(0, 20, 48, 0, mui.RGBA(0, 0, 0, 0.60))
	case hover || o.menuOpen || o.popped:
		b.Background(theme.HUDGlass).Border(1, theme.HUDEdgeLit).Shadow(0, 16, 40, 0, theme.FloatShadow)
	default:
		b.Background(theme.HUDGlassIdle).Border(1, theme.HUDEdge).Shadow(0, 10, 28, 0, mui.RGBA(0, 0, 0, 0.45))
	}
	if dim {
		if idleFor < 0 {
			c.After(-idleFor)
		} else {
			k := min(float32(idleFor)/float32(a.motion(c, dimTime)+1), 1)
			if k < 1 {
				c.AnimationFrame()
			}
			b.Opacity(1 - 0.65*k)
		}
	}
	b.Children(func() {
		m := mui.Image(c, theme.Mark).Size(mark[0], mark[1])
		if !hover && !dragging && !o.menuOpen {
			m.Opacity(0.72)
		}
	})
	if o.released {
		a.release(b, now, a.motion(c, meltTime))
	}
	if !pressed && !b.Transitioning() && o.off != (mui.Point{}) {
		o.btn = mui.Point{X: o.btn.X + o.off.X, Y: o.btn.Y + o.off.Y}
		o.off, o.fold = mui.Point{}, true
		if o.dock == DockNone {
			bb := btnBounds(o.vw, o.vh)
			o.btnAt = placementAt(clampPoint(o.btn, bb), btnSize, btnSize, o.vw, o.vh, bb, btnInset)
		}
		c.Invalidate()
	}
	if dragging {
		a.dockTarget(c, b)
	} else {
		o.target = DockNone
	}
	if b.Clicked() {
		o.menuOpen = !o.menuWasOpen
	}
	a.buttonTip(c, b, now)
	return b
}

// release docks the button where snap said, melting it into the edge, or
// leaves it floating.
func (a *App) release(b mui.Element, now time.Time, melt time.Duration) {
	o := &a.ovl
	o.released = false
	at := b.DragOffset()
	from := mui.Rect{X: o.btn.X + at.X, Y: o.btn.Y + at.Y, W: btnSize, H: btnSize}
	if o.releaseDock == DockNone {
		o.dock, o.popped = DockNone, false
		return
	}
	o.dock, o.dockFrac, o.popped = o.releaseDock, o.releaseFrac, false
	o.off = mui.Point{}
	tab, _, _ := o.dockGeom()
	o.morph = tween{from: from, to: tab, at: now, dur: melt}
	o.menuOpen = false
}

// morphBox draws the button on its way between bubble and tab.
func (a *App) morphBox(c *mui.Context, now time.Time) {
	o := &a.ovl
	r := o.morph.rect(now)
	if left := o.morph.at.Add(o.morph.dur).Sub(now); left > 0 {
		c.After(left)
		c.AnimationFrame()
	}
	mui.Box(c).Absolute().Left(r.X).Top(r.Y).Size(r.W, r.H).Radius(min(r.W, r.H) / 4).Background(theme.HUDGlass).
		IgnoreInput(true).AlignItems(mui.Center).Justify(mui.Center).Children(func() {
		emberSlice(c, o.dock, r)
	})
}

// emberSlice is the 2 x 16 ember bar the docked tab carries, across the
// tab's short side.
func emberSlice(c *mui.Context, e DockEdge, r mui.Rect) {
	w, h := float32(2), float32(16)
	if e == DockTop || e == DockBottom {
		w, h = h, w
	}
	mui.Box(c).Size(min(w, r.W), min(h, r.H)).Radius(1).Background(theme.RoleAccent)
}

// dockTab is the button melted into its edge: 8 px of glass carrying the
// ember slice, rounded on its inner corners.
func (a *App) dockTab(c *mui.Context, tab mui.Rect) {
	t := mui.Box(c).Absolute().Left(tab.X).Top(tab.Y).Size(tab.W, tab.H).Background(theme.HUDGlassTab).
		Shadow(0, 4, 14, 0, mui.RGBA(0, 0, 0, 0.35)).IgnoreInput(true).AlignItems(mui.Center).Justify(mui.Center)
	switch a.ovl.dock {
	case DockLeft:
		t.Radius(0, 6, 6, 0)
	case DockRight:
		t.Radius(6, 0, 0, 6)
	case DockTop:
		t.Radius(0, 0, 6, 6)
	case DockBottom:
		t.Radius(6, 6, 0, 0)
	}
	t.Children(func() { emberSlice(c, a.ovl.dock, tab) })
}

// traceLine is the 1 px ember line left on the edge behind a popped-out
// button.
func (a *App) traceLine(c *mui.Context, tab mui.Rect) {
	r := tab
	switch a.ovl.dock {
	case DockLeft:
		r.W = 1
	case DockRight:
		r.X, r.W = r.X+r.W-1, 1
	case DockTop:
		r.H = 1
	case DockBottom:
		r.Y, r.H = r.Y+r.H-1, 1
	}
	mui.Box(c).Absolute().Left(r.X).Top(r.Y).Size(r.W, r.H).Background(theme.HUDTrace).IgnoreInput(true)
}

// dockTarget lights the edge the dragged button docks to if let go where
// it is: an ember lip, a wash from the edge, and the tab it would melt
// into, level with the button.
func (a *App) dockTarget(c *mui.Context, b mui.Element) {
	o := &a.ovl
	at := b.DragOffset()
	e, frac := dockCandidate(o.btn.X+at.X+btnSize/2, o.btn.Y+at.Y+btnSize/2, o.vw, o.vh, o.target)
	o.target, o.targetFrac = e, frac
	if e == DockNone {
		return
	}
	tab, _, _ := dockRects(e, frac, o.vw, o.vh)
	const lipLong, glow = 124, 88
	lip, wash := tab, tab
	angle := float32(90)
	switch e {
	case DockLeft, DockRight:
		lip.Y, lip.W, lip.H = tab.Y+tab.H/2-lipLong/2, 3, lipLong
		wash.Y, wash.W, wash.H = lip.Y, glow, lipLong
		if e == DockRight {
			lip.X, wash.X, angle = o.vw-3, o.vw-glow, 270
		}
	default:
		lip.X, lip.W, lip.H = tab.X+tab.W/2-lipLong/2, lipLong, 3
		wash.X, wash.W, wash.H, angle = lip.X, lipLong, glow, 180
		if e == DockBottom {
			lip.Y, wash.Y, angle = o.vh-3, o.vh-glow, 0
		}
	}
	mui.Box(c).Absolute().Left(wash.X).Top(wash.Y).Size(wash.W, wash.H).IgnoreInput(true).
		Draw(func(p *mui.Painter, r mui.Rect) {
			p.FillGradient(r, mui.LinearGradient{From: theme.HUDDockGlow, To: theme.HUDDockGlow.Alpha(0), Angle: angle}, 0)
		})
	mui.Box(c).Absolute().Left(lip.X).Top(lip.Y).Size(lip.W, lip.H).Radius(2).Background(theme.RoleAccent).IgnoreInput(true)
	mui.Box(c).Absolute().Left(tab.X).Top(tab.Y).Size(tab.W, tab.H).Radius(3).Background(theme.RoleAccent.Alpha(0.38)).
		IgnoreInput(true).Label("Dock target")
}

// buttonTip names the button and its key: on hover, at once on keyboard
// focus, and for a while on the first stream ever.
func (a *App) buttonTip(c *mui.Context, b mui.Element, now time.Time) {
	o := &a.ovl
	body := func(tip mui.Element) {
		at, self := mui.AnchorEnd, mui.AnchorStart
		if o.menuOpensLeft() {
			at, self = mui.AnchorStart, mui.AnchorEnd
		}
		tip.AttachTo(b, at, self).Margin(0, theme.Space2, 0, theme.Space2).Padding(theme.Space1+1, theme.Space2+2).
			Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong).
			Shadow(0, 16, 40, 0, theme.FloatShadow).IgnoreInput(true).Children(func() {
			mui.Row(c).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
				mui.Text(c, "Stream controls").Font(theme.FontUI).FontSize(theme.TextCaption + 1).TextColor(theme.RoleText).SingleLine()
				badge(c, a.shortcut(false, "O"))
			})
		})
	}
	if o.menuOpen || b.Dragging() {
		return
	}
	if now.Before(o.tipUntil) {
		c.After(o.tipUntil.Sub(now))
		mui.Overlay(c, func() { body(mui.Box(c.Key("overlay-first-tip"))) })
		return
	}
	mui.TooltipBase(c, b, body)
}

// menuOpensLeft reports whether the menu and the tip open towards the
// left: docked right, or floating in the right half.
func (o *overlay) menuOpensLeft() bool {
	switch o.dock {
	case DockRight:
		return true
	case DockNone:
		return o.btn.X+o.off.X+btnSize/2 > o.vw/2
	}
	return false
}

// badge is a shortcut's keys in a hairline box.
func badge(c *mui.Context, keys string) mui.Element {
	return mui.Row(c).Height(18).PaddingX(5).AlignItems(mui.Center).Radius(3).Border(1, theme.RoleBorder).Children(func() {
		mui.Text(c, keys).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText3).SingleLine()
	})
}

// shortcut prints a three-key chord as this platform does: "⌃⌥P" on
// macOS, "Ctrl Alt P" elsewhere.
func (a *App) shortcut(shift bool, key string) string {
	if a.goos == "darwin" {
		if shift {
			return "⌃⌥⇧" + key
		}
		return "⌃⌥" + key
	}
	if shift {
		return "Ctrl Alt Shift " + key
	}
	return "Ctrl Alt " + key
}

// toastView shows the toast, if one is due, for a moment. It never takes
// the pointer.
func (a *App) toastView(c *mui.Context, now time.Time) {
	o := &a.ovl
	t := now.Sub(o.toast.at)
	end := toastIn + toastHold + toastOut
	if o.toast.at.IsZero() || t >= end {
		return
	}
	c.After(end - t)
	opacity := float32(1)
	switch {
	case t < toastIn:
		opacity = float32(t) / float32(toastIn)
		c.AnimationFrame()
	case t > toastIn+toastHold:
		opacity = 1 - float32(t-toastIn-toastHold)/float32(toastOut)
		c.AnimationFrame()
	}
	if c.Preferences().ReduceMotion {
		opacity = 1
	}
	note := func(s string) {
		if s != "" {
			mui.Text(c, s).Font(theme.FontUI).FontSize(theme.TextCaption + 1).TextColor(theme.RoleText3).SingleLine()
		}
	}
	mui.Row(c).Absolute().Left(0).Right(0).Top(26).Justify(mui.Center).IgnoreInput(true).Children(func() {
		mui.Row(c).Height(36).Padding(0, 14).Gap(9).AlignItems(mui.Center).Radius(theme.RadiusPill).
			Background(theme.HUDGlass).Border(1, theme.HUDEdge).Shadow(0, 16, 40, 0, theme.FloatShadow).
			Opacity(opacity).IgnoreInput(true).Children(func() {
			mui.Image(c, theme.Mark).Size(10, 15).Opacity(0.8)
			mui.Box(c).Size(1, 16).Background(theme.RoleText.Alpha(0.12))
			mui.Text(c, o.toast.title).Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(500).TextColor(theme.RoleText).SingleLine()
			note(o.toast.lead)
			if o.toast.key != "" {
				mui.Row(c).Height(18).PaddingX(5).AlignItems(mui.Center).Radius(3).Border(1, theme.RoleBorderStrong).Children(func() {
					mui.Text(c, a.shortcut(false, o.toast.key)).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText2).SingleLine()
				})
			}
			note(o.toast.tail)
		})
	})
}

func (a *App) showToast(t toast) {
	t.at = a.frameNow
	a.ovl.toast = t
}

// overlayShortcuts are the overlay's keys, which work while it is hidden.
func (a *App) overlayShortcuts(c *mui.Context) {
	chord := mui.Ctrl | mui.Alt
	c.OnWindowShortcut(chord, mui.KeyO, func() {
		o := &a.ovl
		if o.hidden {
			o.hidden = false
			o.menuOpen = true
			return
		}
		o.menuOpen = !o.menuOpen
	})
	c.OnWindowShortcut(chord, mui.KeyP, a.toggleHidden)
	c.OnWindowShortcut(chord, mui.KeyH, a.toggleHidden)
	c.OnWindowShortcut(chord, mui.KeyM, func() {
		o := &a.ovl
		o.hidden = false
		o.metrics.toggle(o.vh)
	})
	c.OnWindowShortcut(chord, mui.KeyU, func() { go a.ToggleMic() })
	c.OnWindowShortcut(chord, mui.KeyF, a.toggleFullscreen)
	c.OnWindowShortcut(chord, mui.KeyI, func() {
		s := a.backend.Settings()
		before := s
		s.SystemKeysCapture = !s.SystemKeysCapture
		a.saveSettings(before, s)
	})
	c.OnWindowShortcut(chord, mui.KeyG, a.toggleCursorLock)
	c.OnWindowShortcut(chord|mui.Shift, mui.KeyD, a.disconnect)
}

// toggleCursorLock locks the mouse to the stream, or releases it, and
// says how to undo it.
func (a *App) toggleCursorLock() {
	if a.cursor == nil {
		return
	}
	on := !a.cursor.CursorLocked()
	a.cursor.SetCursorLocked(on)
	a.ovl.cursorLocked, a.ovl.cursorTold = on, true
	if on {
		a.showToast(toast{title: "Cursor locked", lead: "press", key: "G", tail: "to release"})
		return
	}
	a.showToast(toast{title: "Cursor released", key: "G", tail: "or click the stream to lock"})
}

// watchCursorLock says, the first time a stream's game locks the mouse,
// how to release it.
func (a *App) watchCursorLock() {
	if a.cursor == nil {
		return
	}
	o := &a.ovl
	on := a.cursor.CursorLocked()
	if on && !o.cursorLocked && !o.cursorTold {
		o.cursorTold = true
		a.showToast(toast{title: "Cursor locked", lead: "press", key: "G", tail: "to release"})
	}
	o.cursorLocked = on
}

// watchSystemKeys says, once for each new reason, that immersive mode
// cannot capture the system's keys and what to do about it.
func (a *App) watchSystemKeys() {
	if a.sysKeys == nil {
		return
	}
	p := a.sysKeys.SystemKeysProblem()
	if p != "" && p != a.ovl.sysKeysTold {
		a.showToast(toast{title: "System keys stay on this " + machineWord(a.goos), tail: p})
	}
	a.ovl.sysKeysTold = p
}

// noteMenu frees the mouse while the stream's menu is open.
func (a *App) noteMenu(open bool) {
	if a.cursor != nil {
		a.cursor.SetMenuOpen(open)
	}
}

// toggleHidden hides the whole overlay, or brings it back; hiding shows
// the toast that says how to bring it back.
func (a *App) toggleHidden() {
	o := &a.ovl
	o.hidden = !o.hidden
	o.menuOpen = false
	o.toast = toast{}
	if o.hidden {
		a.showToast(toast{title: "Overlay hidden", lead: "press", key: "P", tail: "to show"})
	}
}

func (a *App) toggleFullscreen() {
	if a.window != nil {
		a.setStreamFullscreen(!a.window.Fullscreen())
	}
}

// noteOverlay tells OnOverlayChange's callback when the overlay starts or
// stops drawing over the stream.
func (a *App) noteOverlay(shown bool) {
	a.mu.Lock()
	changed := shown != a.overlayShown
	a.overlayShown = shown
	cb := a.onOverlay
	a.mu.Unlock()
	if changed && cb != nil {
		cb(shown)
	}
}
