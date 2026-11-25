package ui

import (
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/discovery"
)

func label(c *mui.Context, s string) mui.Element {
	return mui.Text(c, s).Font(theme.FontUI).FontSize(theme.TextBody).TextColor(theme.RoleText)
}

func caption(c *mui.Context, s string) mui.Element {
	return mui.Text(c, s).Font(theme.FontUI).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
}

func secondary(c *mui.Context, s string) mui.Element {
	return mui.Text(c, s).Font(theme.FontUI).FontSize(theme.TextCaption + 1).TextColor(theme.RoleText2)
}

func mono(c *mui.Context, s string) mui.Element {
	return mui.Text(c, s).Font(theme.FontMono).FontSize(theme.TextBody).TextColor(theme.RoleText).SingleLine()
}

func heading(c *mui.Context, s string, size float32) mui.Element {
	e := mui.Text(c, s).Font(theme.FontDisplay).FontSize(size).FontWeight(600).TextColor(theme.RoleText)
	if size >= theme.TextDisplay {
		e.LetterSpacing(-0.01 * size)
	}
	return e
}

// The title block is pageTitleLine + pageHeaderGap + pageSubLine = 52 px
// tall, and the first row under it starts pageRowGap below. Every rail
// screen starts at pagePadTop, so the first row's top is the same
// pageRowTop on all of them and the rail aligns to it.
const (
	pagePadTop    = 24
	pageTitleLine = 34
	pageSubLine   = 14
	pageHeaderGap = 4
	pageHeaderH   = pageTitleLine + pageHeaderGap + pageSubLine
	pageRowGap    = 24
	pageRowTop    = pagePadTop + pageHeaderH + pageRowGap
	pageFirstRowH = 48
)

// pageHeader is the title block every rail screen opens with.
func pageHeader(c *mui.Context, title, sub string, trailing func()) {
	mui.Row(c).FillWidth().AlignItems(mui.End).Children(func() {
		mui.Column(c).Gap(pageHeaderGap).Grow(1).Children(func() {
			heading(c, title, theme.TextReadout).FixedLineHeight(pageTitleLine)
			if sub != "" {
				secondary(c, sub).SingleLine().FixedLineHeight(pageSubLine)
			}
		})
		if trailing != nil {
			trailing()
		}
	})
}

type buttonKind int

const (
	buttonPrimary buttonKind = iota
	buttonSecondary
	buttonGhost
	buttonDestructive
)

// button is the design system's button: primary (one per view),
// secondary, ghost and destructive. Height is SizeControl unless large.
func button(c *mui.Context, kind buttonKind, text string, large bool) mui.Element {
	b := mui.ButtonBase(c).Label(text).Radius(theme.RadiusSm).Cursor(mui.CursorPointer)
	h, pad, size := theme.SizeControl, theme.Space3, theme.TextBody
	if large {
		h, pad, size = theme.SizeControlLg+4, theme.Space5, theme.TextTitle
	}
	b.Height(h).PaddingX(pad)
	fg := theme.RoleText
	switch kind {
	case buttonPrimary:
		bg := theme.RoleAccent
		if b.Pressed() {
			bg = theme.RoleAccentPressed
		} else if b.Hovered() {
			bg = theme.RoleAccentHover
		}
		b.Background(bg)
		fg = theme.RoleOnAccent
	case buttonSecondary:
		bg := theme.RoleRaised
		if b.Hovered() {
			bg = theme.RoleHover
		}
		b.Background(bg).Border(1, theme.RoleBorderStrong)
	case buttonGhost:
		fg = theme.RoleText2
		if b.Hovered() {
			b.Background(theme.RoleHover)
			fg = theme.RoleText
		}
	case buttonDestructive:
		b.Border(1, theme.RoleFault)
		if b.Hovered() {
			b.Background(theme.RoleFault.Alpha(0.12))
		}
		fg = theme.RoleFault
	}
	b.Children(func() {
		mui.Text(c, text).Font(theme.FontUI).FontSize(size).FontWeight(600).TextColor(fg).SingleLine()
	})
	return b
}

// sliceMeter draws the stacked-bar glyph: bars of 4 (or 3) filled from the
// top in col, the rest in the strong border color.
func sliceMeter(c *mui.Context, filled, total int, col mui.Color) mui.Element {
	const barH, gap, w = 2, 2, 12
	h := float32(total*barH + (total-1)*gap)
	return mui.Box(c).Size(w, h).Draw(func(p *mui.Painter, r mui.Rect) {
		for i := 0; i < total; i++ {
			bc := theme.RoleBorderStrong
			if i < filled {
				bc = col
			}
			p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*(barH+gap)), W: w, H: barH}, bc, 0)
		}
	})
}

func signalColor(k discovery.PathKind) mui.Color {
	if k == discovery.PathTailscaleDERP {
		return theme.RoleRelay
	}
	return theme.RoleDirect
}

func divider(c *mui.Context) mui.Element {
	return mui.Box(c).FillWidth().Height(1).Background(theme.RoleBorder)
}

// segmented is the design system's segmented control: a 30 px well, the
// chosen segment on the hover fill in weight 600. It reports whether the
// choice changed.
func segmented(c *mui.Context, name string, sel *int, labels ...string) bool {
	parts := mui.SegmentedBase(c, sel, len(labels))
	track := parts.Track.Label(name).Height(30).Padding(2).Gap(2).Radius(theme.RadiusSm).Background(theme.RoleWell)
	track.Children(func() {
		for i, l := range labels {
			s := parts.Segment(i).Height(26).PaddingX(10).
				Radius(4).Cursor(mui.CursorPointer).AlignItems(mui.Center).Label(name + ": " + l)
			weight, col := 500, theme.RoleText3
			if i == *sel {
				s.Background(theme.RoleHover)
				weight, col = 600, theme.RoleText
			} else if s.Hovered() {
				col = theme.RoleText2
			}
			s.Children(func() {
				mui.Text(c, l).Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(weight).TextColor(col).SingleLine()
			})
		}
	})
	return track.Changed()
}

// switchTrack draws the 32 x 18 toggle: ember with an ink knob when on,
// the control edge when off.
func switchTrack(c *mui.Context, on bool) {
	mui.Box(c).Size(32, 18).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		const knob = 12
		y := r.Y + (r.H-knob)/2
		if on {
			p.Fill(r, theme.RoleAccent, r.H/2)
			p.Fill(mui.Rect{X: r.X + r.W - knob - 3, Y: y, W: knob, H: knob}, theme.RoleOnAccent, knob/2)
			return
		}
		p.Stroke(r, theme.RoleControlEdge, r.H/2, 1.5)
		p.Fill(mui.Rect{X: r.X + 3, Y: y, W: knob, H: knob}, theme.RoleControlEdge, knob/2)
	})
}

// tooltip shows a design-system tip beside anchor, to its right.
func tooltip(c *mui.Context, anchor mui.Element, fn func()) {
	mui.TooltipBase(c, anchor, func(tip mui.Element) {
		tip.AttachTo(anchor, mui.AnchorEnd, mui.AnchorStart).MarginStart(theme.Space2).
			Padding(theme.Space2, theme.Space3).Radius(theme.RadiusSm).Background(theme.RoleRaised).
			Border(1, theme.RoleBorderStrong).Shadow(0, 16, 40, 0, theme.FloatShadow).
			Children(fn)
	})
}
