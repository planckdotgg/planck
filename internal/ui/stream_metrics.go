package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

const (
	metricsW, metricsH = 300, 361
	compactW, compactH = 196, 36
	// metricsInset holds the window inside the view; it stays
	// metricsKeepOut from the edge the button is docked to.
	metricsInset   = 12
	metricsKeepOut = 28
	metricsSnap    = 20
	metricsMargin  = 16
	// metricsSlop is how far a press on the head moves before it drags, so
	// that a click on its buttons never moves the window.
	metricsSlop = 4
	// compactBelow is the view height under which the window opens
	// compact.
	compactBelow = 720
	sparkLen     = 120
	sampleEvery  = 100 * time.Millisecond
	textEvery    = 250 * time.Millisecond
	// lossFault is the loss above which the LOSS cell shows a fault.
	lossFault = 0.5
)

// metricsWindow is the floating metrics window: where it is, how big, and
// the readings it shows, formatted four times a second and sampled for the
// sparklines ten times a second.
type metricsWindow struct {
	shown, compact, placed bool
	// at is where the window was left. pos is where it is drawn: at, held
	// inside the view, or where a drag has it; only a drag's end changes
	// at. pull is how far a press on the head moved short of metricsSlop.
	at       Placement
	pos      mui.Point
	pull     mui.Point
	dragging bool

	samples  [5][sparkLen]float32
	head, n  int
	sampleAt time.Time
	textAt   time.Time
	text     metricsText
	paths    [5]mui.Path
	draws    [5]func(*mui.Painter, mui.Rect)
	sparkCol [5]mui.Color

	dropped   uint64
	droppedAt time.Time
}

// metricsText is a reading, formatted on the 4 Hz tick.
type metricsText struct {
	total, fps                       string
	stages                           [5]string
	bitrate, loss, fec, rtt, ceiling string
	format, gpu, size, datagram      string
	ceilingFrac                      float32
	late, dropping, lossy, fecRaised bool
}

func (w *metricsWindow) begin(l OverlayLayout, vw, vh float32) {
	draws := w.draws
	*w = metricsWindow{shown: l.Metrics, compact: l.MetricsCompact || vh < compactBelow, draws: draws}
	switch {
	case l.MetricsPlace.set():
		w.at, w.placed = l.MetricsPlace, true
	case l.MetricsX != 0 || l.MetricsY != 0:
		sw, sh := w.size()
		b := w.bounds(vw, vh, l.Dock)
		p := clampPoint(mui.Point{X: round32(l.MetricsX * vw), Y: round32(l.MetricsY * vh)}, b)
		w.at, w.placed = placementAt(p, sw, sh, vw, vh, b, metricsInset), true
	}
	if w.shown && !w.placed {
		w.place()
	}
}

func (w *metricsWindow) store(l *OverlayLayout) {
	l.Metrics, l.MetricsCompact = w.shown, w.compact
	if w.placed {
		l.MetricsPlace = w.at
	}
}

// place opens the window top-right, 16 px in.
func (w *metricsWindow) place() {
	w.at = Placement{AnchorX: AnchorEnd, AnchorY: AnchorStart, OffsetX: metricsMargin, OffsetY: metricsMargin}
	w.placed = true
}

func (w *metricsWindow) toggle(vh float32) {
	w.shown = !w.shown
	if !w.shown {
		return
	}
	if vh < compactBelow {
		w.compact = true
	}
	if !w.placed {
		w.place()
	}
}

// cycle steps the HUD through its modes: off, the full window, the
// compact bar, and off again. A view too short for the full window opens
// compact.
func (w *metricsWindow) cycle(vh float32) {
	switch {
	case !w.shown:
		w.shown, w.compact = true, vh < compactBelow
		if !w.placed {
			w.place()
		}
	case !w.compact:
		w.compact = true
	default:
		w.shown = false
	}
}

func (w *metricsWindow) size() (float32, float32) {
	if w.compact {
		return compactW, compactH
	}
	return metricsW, metricsH
}

// bounds is where the window's top-left corner may go: inside the view,
// and short of the edge the button is docked to.
func (w *metricsWindow) bounds(vw, vh float32, dock DockEdge) mui.Rect {
	sw, sh := w.size()
	x0, y0, x1, y1 := float32(metricsInset), float32(metricsInset), vw-metricsInset-sw, vh-metricsInset-sh
	switch dock {
	case DockLeft:
		x0 = metricsKeepOut
	case DockRight:
		x1 = vw - metricsKeepOut - sw
	case DockTop:
		y0 = metricsKeepOut
	case DockBottom:
		y1 = vh - metricsKeepOut - sh
	}
	return mui.Rect{X: x0, Y: y0, W: max(x1-x0, 0), H: max(y1-y0, 0)}
}

func clampPoint(p mui.Point, b mui.Rect) mui.Point {
	return mui.Point{X: min(max(p.X, b.X), b.X+b.W), Y: min(max(p.Y, b.Y), b.Y+b.H)}
}

// snapCorner pulls a window let go within metricsSnap of a corner of its
// bounds into that corner.
func snapCorner(p mui.Point, b mui.Rect) mui.Point {
	nearX, nearY := false, false
	for _, x := range []float32{b.X, b.X + b.W} {
		if abs32(p.X-x) <= metricsSnap {
			p.X, nearX = x, true
			break
		}
	}
	for _, y := range []float32{b.Y, b.Y + b.H} {
		if abs32(p.Y-y) <= metricsSnap {
			p.Y, nearY = y, true
			break
		}
	}
	if nearX && nearY {
		return p
	}
	return mui.Point{}
}

func round32(v float32) float32 { return float32(math.Round(float64(v))) }

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// sample keeps the stage times for the sparklines and formats the reading
// when their ticks are due.
func (w *metricsWindow) sample(c *mui.Context, tel Telemetry, now time.Time) {
	if tel.FramesDropped > w.dropped {
		w.droppedAt = now
	}
	w.dropped = tel.FramesDropped
	if !now.Before(w.sampleAt) {
		w.sampleAt = now.Add(sampleEvery)
		for i, s := range stages(tel) {
			w.samples[i][w.head] = float32(s.ms)
		}
		w.head = (w.head + 1) % sparkLen
		w.n = min(w.n+1, sparkLen)
	}
	if !now.Before(w.textAt) {
		w.textAt = now.Add(textEvery)
		w.text = readMetrics(tel, now.Sub(w.droppedAt) < time.Second && !w.droppedAt.IsZero())
	}
	c.After(min(w.sampleAt.Sub(now), w.textAt.Sub(now)))
}

func readMetrics(tel Telemetry, dropping bool) metricsText {
	t := metricsText{total: formatMs(tel.TotalMs), fps: fmt.Sprintf("%.0f", tel.FPS), dropping: dropping}
	for i, s := range stages(tel) {
		t.stages[i] = fmt.Sprintf("%.2f", s.ms)
	}
	t.bitrate = fmt.Sprintf("%.1f", tel.BitrateMbps)
	t.loss = fmt.Sprintf("%.1f", tel.LossPct)
	t.fec = fmt.Sprintf("%.0f", tel.FECPct)
	t.rtt = fmt.Sprintf("%.2f", tel.RTTMs)
	t.lossy = tel.LossPct > lossFault
	if tel.CeilingMbps > 0 {
		t.ceilingFrac = min(float32(tel.BitrateMbps/tel.CeilingMbps), 1)
		t.ceiling = fmt.Sprintf("%.0f %% of ceiling", 100*tel.BitrateMbps/tel.CeilingMbps)
	}
	fps := tel.FPS
	p := tel.Profile
	if p != nil && p.FPS > 0 {
		fps = float64(p.FPS)
	}
	t.late = fps > 0 && tel.TotalMs > 2*1000/fps
	if p != nil {
		t.fecRaised = p.FEC.Adaptive && tel.FECPct > float64(p.FEC.MinParityPercent)+0.5
		depth := "8-bit"
		if p.BitDepth == 10 {
			depth = "10-bit"
		}
		ch := "4:2:0"
		if p.Chroma444 {
			ch = "4:4:4"
		}
		t.format = strings.Join([]string{codecName(p.Codec), depth, ch}, " · ")
		t.gpu = shortGPU(p.EncodeAdapter.Name)
		if p.VirtualDisplay {
			t.gpu = strings.TrimSpace(t.gpu + " VDD")
		}
	} else if s := codecLabel(tel.Codec, tel.Chroma); s != "" {
		t.format = s
	}
	if tel.Width > 0 && tel.Height > 0 {
		t.size = fmt.Sprintf("%d × %d @ %.0f fps", tel.Width, tel.Height, fps)
	}
	if tel.DatagramBytes > 0 {
		t.datagram = fmt.Sprintf("%d B", tel.DatagramBytes)
	}
	return t
}

// figureColor is glass to glass in ember, or the state that degrades it,
// which a chip names as well.
func (t *metricsText) figureColor() (mui.Color, string) {
	switch {
	case t.dropping:
		return theme.RoleFault, "dropping"
	case t.late:
		return theme.RoleRelay, "late"
	}
	return theme.RoleAccent, ""
}

// metricsView draws the window where it was left, kept inside the view;
// only its head drags it.
func (a *App) metricsView(c *mui.Context, tel Telemetry, now time.Time) {
	o := &a.ovl
	w := &o.metrics
	w.sample(c, tel, now)
	if w.draws[0] == nil {
		for i := range w.draws {
			w.draws[i] = func(p *mui.Painter, r mui.Rect) { w.drawSpark(p, r, i) }
		}
	}
	b := w.bounds(o.vw, o.vh, o.dock)
	sw, sh := w.size()
	if !w.dragging {
		w.pos = clampPoint(w.at.resolve(o.vw, o.vh, sw, sh), b)
	}
	win := mui.Column(c.Key("metrics-window")).Absolute().Left(w.pos.X).Top(w.pos.Y).Size(sw, sh).
		Radius(theme.RadiusMd).Clip().Background(theme.HUDGlass).Border(1, theme.HUDEdge).
		Shadow(0, 16, 40, 0, theme.FloatShadow).Label("Metrics")
	win.Children(func() {
		var head mui.Element
		if w.compact {
			head = a.compactMetrics(c, tel)
		} else {
			head = a.metricsHead(c, tel)
			a.metricsBody(c, tel)
		}
		a.dragMetrics(head, b)
	})
}

// dragMetrics moves the window by its head alone, pulls it into a corner
// it is let go near, and anchors it where it rests.
func (a *App) dragMetrics(head mui.Element, b mui.Rect) {
	o := &a.ovl
	w := &o.metrics
	dx, dy, pressed := head.Dragged()
	switch {
	case pressed && w.dragging:
		w.pos = clampPoint(mui.Point{X: w.pos.X + dx, Y: w.pos.Y + dy}, b)
	case pressed:
		w.pull = mui.Point{X: w.pull.X + dx, Y: w.pull.Y + dy}
		if float32(math.Hypot(float64(w.pull.X), float64(w.pull.Y))) >= metricsSlop {
			w.dragging = true
			w.pos = clampPoint(mui.Point{X: w.pos.X + w.pull.X, Y: w.pos.Y + w.pull.Y}, b)
			w.pull = mui.Point{}
		}
	case w.dragging:
		w.dragging = false
		if p := snapCorner(w.pos, b); p != (mui.Point{}) {
			w.pos = p
		}
		sw, sh := w.size()
		w.at = placementAt(w.pos, sw, sh, o.vw, o.vh, b, metricsInset)
	default:
		w.pull = mui.Point{}
	}
}

func grip(c *mui.Context) {
	mui.Box(c).Size(10, 14).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		for i, l := range [4]float32{10, 7, 10, 5} {
			p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*4), W: l, H: 2}, theme.Carbon500, 0)
		}
	})
}

func iconButton(c *mui.Context, label string, icon *mui.SVG, size float32, rotate float32) mui.Element {
	b := mui.ButtonBase(c).Size(size, size).Radius(4).AlignItems(mui.Center).Justify(mui.Center).Label(label).
		Cursor(mui.CursorPointer)
	if b.Hovered() {
		b.Background(theme.HUDRowHover)
	}
	b.Children(func() {
		ic := mui.Icon(c, icon).Size(12, 12).TextColor(theme.RoleText2)
		if rotate != 0 {
			ic.Rotate(rotate)
		}
	})
	return b
}

func (a *App) metricsHead(c *mui.Context, tel Telemetry) mui.Element {
	w := &a.ovl.metrics
	return mui.Row(c.Key("metrics-head")).FillWidth().Height(34).Padding(0, 6, 0, 10).Gap(8).AlignItems(mui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(theme.HUDDivider).Cursor(mui.CursorGrab).Label("Metrics window bar").Children(func() {
		grip(c)
		mui.Text(c, tel.HostName).Font(theme.FontUI).FontSize(theme.TextCaption + 1).FontWeight(600).TextColor(theme.RoleText).SingleLine().Shrink(1)
		pathBadge(c, tel.PathKind, pathLabel(tel.PathKind), theme.TextMicro)
		mui.Box(c).Grow(1)
		if iconButton(c, "Compact metrics", theme.IconChevron, 20, 180).Clicked() {
			w.compact = true
		}
		if iconButton(c, "Close metrics", theme.IconClose, 20, 0).Clicked() {
			w.shown = false
		}
	})
}

func (a *App) compactMetrics(c *mui.Context, tel Telemetry) mui.Element {
	w := &a.ovl.metrics
	t := &w.text
	col, state := t.figureColor()
	return mui.Row(c.Key("metrics-head")).Fill().Padding(0, 8).Gap(7).AlignItems(mui.Center).Cursor(mui.CursorGrab).
		Label("Metrics window bar").Children(func() {
		grip(c)
		mui.Row(c).AlignItems(mui.Baseline).Gap(2).Shrink(0).Label(state).Children(func() {
			mono(c, t.total).FontSize(15).FontWeight(500).TextColor(col)
			mono(c, "ms").FontSize(theme.TextMicro).TextColor(theme.RoleText3)
		})
		mui.Box(c).Size(1, 16).Background(theme.RoleText.Alpha(0.12))
		mui.Row(c).AlignItems(mui.Baseline).Gap(2).Shrink(0).Children(func() {
			mono(c, t.fps).FontSize(theme.TextCaption)
			mono(c, "fps").FontSize(theme.TextMicro).TextColor(theme.RoleText3)
		})
		mui.Box(c).Size(1, 16).Background(theme.RoleText.Alpha(0.12))
		mui.Row(c).Gap(4).AlignItems(mui.Center).Grow(1).Shrink(0).Label(pathLabel(tel.PathKind)).Children(func() {
			mui.Box(c).Size(6, 6).Radius(3).Background(signalColor(tel.PathKind))
			mono(c, pathShort(tel.PathKind)).FontSize(theme.TextMicro).TextColor(signalColor(tel.PathKind)).Shrink(0)
		})
		if iconButton(c, "Expand metrics", theme.IconChevron, 18, 0).Shrink(0).Clicked() {
			w.compact = false
		}
	})
}

func (a *App) metricsBody(c *mui.Context, tel Telemetry) {
	w := &a.ovl.metrics
	t := &w.text
	small := func(s string, col mui.Color) mui.Element {
		return mono(c, s).FontSize(theme.TextMini).TextColor(col)
	}
	mui.Column(c).FillWidth().Padding(10, 12, 0, 12).Gap(3).Children(func() {
		mui.Row(c).FillWidth().Children(func() {
			small(t.format, theme.RoleText2).Grow(1)
			small(t.gpu, theme.RoleText3)
		})
		mui.Row(c).FillWidth().Children(func() {
			small(t.size, theme.RoleText2).Grow(1)
			small(t.datagram, theme.RoleText3)
		})
	})
	col, state := t.figureColor()
	mui.Row(c).FillWidth().Padding(12, 12, 0, 12).AlignItems(mui.Baseline).Gap(4).Children(func() {
		mono(c, t.total).FontSize(theme.TextReadout).FontWeight(500).TextColor(col).Label("Glass to glass " + t.total + " ms")
		mono(c, "ms").FontSize(theme.TextCaption).TextColor(theme.RoleText3)
		if state != "" {
			mui.Row(c).Height(18).PaddingX(7).AlignItems(mui.Center).Radius(theme.RadiusPill).Border(1, col.Alpha(0.55)).Children(func() {
				mono(c, state).FontSize(theme.TextMicro).TextColor(col)
			})
		}
		mui.Box(c).Grow(1)
		mui.Text(c, "glass to glass").Font(theme.FontUI).FontSize(theme.TextCaption).TextColor(theme.RoleText3).SingleLine()
	})
	mui.Box(c).FillWidth().Padding(10, 12, 0, 12).Children(func() { stageBar(c, tel) })
	w.sparkCol = [5]mui.Color{}
	mui.Column(c).FillWidth().Padding(11, 12, 0, 12).Gap(3).Children(func() {
		for i, s := range stages(tel) {
			w.sparkCol[i] = s.col
			if i == 4 && t.dropping {
				w.sparkCol[i] = theme.RoleFault
			}
			mui.Row(c).FillWidth().Height(19).AlignItems(mui.Center).Gap(8).Label(s.title).Children(func() {
				mui.Box(c).Size(2, 10).Radius(1).Background(s.col)
				mui.Text(c, s.title).Font(theme.FontUI).FontSize(theme.TextCaption).TextColor(theme.RoleText2).Width(54).SingleLine()
				mui.Box(c).Size(54, 12).Draw(w.draws[i])
				mui.Box(c).Grow(1)
				mono(c, t.stages[i]).FontSize(theme.TextCaption)
				mono(c, "ms").FontSize(theme.TextMicro).TextColor(theme.RoleText3)
			})
		}
	})
	mui.Column(c).FillWidth().Padding(12, 12, 0, 12).Gap(9).Children(func() {
		mui.Box(c).FillWidth().Height(1).Background(theme.HUDDivider)
		mui.Row(c).FillWidth().Height(28).Children(func() {
			lossCol, fecCol := theme.RoleText, theme.RoleText
			if t.lossy {
				lossCol = theme.RoleFault
			}
			if t.fecRaised {
				fecCol = theme.RoleRelay
			}
			healthCell(c, "BITRATE", t.bitrate, "Mbps", theme.RoleText, false)
			healthCell(c, "LOSS", t.loss, "%", lossCol, true)
			healthCell(c, "FEC", t.fec, "%", fecCol, true)
			healthCell(c, "RTT", t.rtt, "ms", theme.RoleText, true)
		})
		frac := t.ceilingFrac
		mui.Box(c).FillWidth().Height(3).Draw(func(p *mui.Painter, r mui.Rect) {
			p.Fill(r, theme.RoleHover, 2)
			p.Fill(mui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, theme.RoleAccent.Alpha(0.55), 2)
		})
	})
	mui.Box(c).Grow(1)
	mui.Row(c).FillWidth().Height(28).Padding(0, 10, 0, 12).AlignItems(mui.Center).BorderWidth(1, 0, 0, 0).BorderColor(theme.HUDDivider).Children(func() {
		foot := "drag the bar"
		if t.ceiling != "" {
			foot += " · " + t.ceiling
		}
		mono(c, foot).FontSize(theme.TextMicro).TextColor(theme.RoleText3).Grow(1)
		mui.Row(c).Height(16).PaddingX(4).AlignItems(mui.Center).Radius(3).Border(1, theme.RoleBorder).Children(func() {
			mono(c, a.shortcut(false, "M")).FontSize(theme.TextMicro).TextColor(theme.RoleText3)
		})
	})
}

// healthCell is one reading of the health strip; a fault colours its
// label, value and unit alike, so the state does not rest on the value's
// colour alone.
func healthCell(c *mui.Context, name, value, unit string, col mui.Color, rule bool) {
	mui.Row(c).Grow(1).Basis(0).FillHeight().Gap(8).Children(func() {
		if rule {
			mui.Box(c).Width(1).FillHeight().Background(theme.HUDDivider)
		}
		label := theme.RoleText3
		if col == theme.RoleFault {
			label = col
		}
		mui.Column(c).Gap(2).Label(name + " " + value + " " + unit).Children(func() {
			mono(c, name).FontSize(theme.TextMicro).LetterSpacing(0.06 * theme.TextMicro).TextColor(label)
			mui.Row(c).AlignItems(mui.Baseline).Gap(2).Children(func() {
				mono(c, value).FontSize(theme.TextCaption).TextColor(col)
				mono(c, unit).FontSize(theme.TextMicro).TextColor(label)
			})
		})
	})
}

// drawSpark draws stage i's last samples as a line, scaled to their range.
func (w *metricsWindow) drawSpark(p *mui.Painter, r mui.Rect, i int) {
	if path := w.sparkPath(r, i); path != nil {
		p.StrokePath(path, 1.2, w.sparkCol[i])
	}
}

// sparkPath lays stage i's samples out in r, in a path that allocates
// nothing once it has grown to its size; nil before two samples.
func (w *metricsWindow) sparkPath(r mui.Rect, i int) *mui.Path {
	if w.n < 2 {
		return nil
	}
	ring := &w.samples[i]
	lo, hi := float32(1e9), float32(-1e9)
	for k := 0; k < w.n; k++ {
		v := ring[(w.head-w.n+k+sparkLen)%sparkLen]
		lo, hi = min(lo, v), max(hi, v)
	}
	span := hi - lo
	if span < 1e-3 {
		span = 1
	}
	path := &w.paths[i]
	path.Reset()
	step := r.W / float32(sparkLen-1)
	x0 := r.X + r.W - step*float32(w.n-1)
	for k := 0; k < w.n; k++ {
		v := ring[(w.head-w.n+k+sparkLen)%sparkLen]
		x, y := x0+step*float32(k), r.Y+r.H-1-(r.H-2)*(v-lo)/span
		if k == 0 {
			path.MoveTo(x, y)
		} else {
			path.LineTo(x, y)
		}
	}
	return path
}
