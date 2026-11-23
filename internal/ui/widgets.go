package ui

import (
	"fmt"

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

// pageHeader is the title block every rail screen opens with.
func pageHeader(c *mui.Context, title, sub string, trailing func()) {
	mui.Row(c).FillWidth().AlignItems(mui.End).Children(func() {
		mui.Column(c).Gap(theme.Space1).Grow(1).Children(func() {
			heading(c, title, theme.TextReadout)
			if sub != "" {
				secondary(c, sub)
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

// pathBadge is the split pill: meter glyph and path name, then the round
// trip in mono after a hairline. A DERP relay drops the value and takes a
// relay-yellow outline.
func pathBadge(c *mui.Context, k discovery.PathKind, rttMs float64) mui.Element {
	col := signalColor(k)
	relay := k == discovery.PathTailscaleDERP
	edge := theme.RoleBorderStrong
	if relay {
		edge = theme.RoleRelay
	}
	return mui.Row(c).Height(26).AlignItems(mui.Center).Radius(theme.RadiusPill).Border(1, edge).
		Background(theme.RoleRaised).Children(func() {
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).PaddingX(theme.Space3).Children(func() {
			sliceMeter(c, pathBars(k), 3, col)
			t := label(c, pathLabel(k)).SingleLine()
			if relay {
				t.TextColor(theme.RoleRelay)
			}
		})
		if !relay && rttMs > 0 {
			mui.Box(c).Width(1).FillHeight().Background(theme.RoleBorderStrong)
			mui.Box(c).PaddingX(theme.Space3).Children(func() {
				mono(c, fmt.Sprintf("%s ms", formatMs(rttMs))).TextColor(col)
			})
		}
	})
}

// capPill labels a capability or a reading in mono.
func capPill(c *mui.Context, s string) mui.Element {
	return mui.Box(c).Height(24).PaddingX(theme.Space2).AlignItems(mui.Center).Justify(mui.Center).
		Radius(theme.RadiusXs).Border(1, theme.RoleBorderStrong).Children(func() {
		mui.Text(c, s).Font(theme.FontMono).FontSize(theme.TextCaption).TextColor(theme.RoleText).SingleLine()
	})
}

// gpuBadge shows a GPU model on a raised fill, staying neutral rather than
// taking a vendor's color.
func gpuBadge(c *mui.Context, gpu string) mui.Element {
	return mui.Box(c).Height(26).PaddingX(theme.Space2).AlignItems(mui.Center).Justify(mui.Center).
		Radius(theme.RadiusXs).Background(theme.RoleRaised).Children(func() {
		mono(c, gpu).FontSize(theme.TextCaption + 1)
	})
}

// panel is a raised panel with an optional head.
func panel(c *mui.Context, title, trailing string, fn func()) mui.Element {
	return mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).
		Radius(theme.RadiusMd).Clip().Children(func() {
		if title != "" {
			mui.Row(c).FillWidth().AlignItems(mui.Center).Padding(theme.Space4, theme.Space5).Children(func() {
				heading(c, title, theme.TextHeading).Grow(1)
				if trailing != "" {
					caption(c, trailing)
				}
			})
			divider(c)
		}
		fn()
	})
}

func divider(c *mui.Context) mui.Element {
	return mui.Box(c).FillWidth().Height(1).Background(theme.RoleBorder)
}

// settingRow is one line of a settings panel: title and description on
// the left, its control on the right.
func settingRow(c *mui.Context, title, desc string, control func()) {
	mui.Row(c).FillWidth().MinHeight(theme.SizeRow).AlignItems(mui.Center).Gap(theme.Space4).
		Padding(theme.Space3, theme.Space5).Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			label(c, title).FontWeight(500)
			if desc != "" {
				secondary(c, desc)
			}
		})
		control()
	})
}

// segmented is the design system's segmented control: a well, the chosen
// segment on the hover fill in weight 600. It reports whether the choice
// changed.
func segmented(c *mui.Context, name string, sel *int, labels ...string) bool {
	parts := mui.SegmentedBase(c, sel, len(labels))
	track := parts.Track.Label(name).Padding(3).Gap(2).Radius(theme.RadiusSm).
		Background(theme.RoleWell).Border(1, theme.RoleBorderStrong)
	track.Children(func() {
		for i, l := range labels {
			s := parts.Segment(i).Height(theme.SizeControl - 8).PaddingX(theme.Space3).
				Radius(theme.RadiusXs).Cursor(mui.CursorPointer).AlignItems(mui.Center)
			weight, col := 400, theme.RoleText2
			if i == *sel {
				s.Background(theme.RoleHover)
				weight, col = 600, theme.RoleText
			} else if s.Hovered() {
				col = theme.RoleText
			}
			s.Children(func() {
				mui.Text(c, l).Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(weight).TextColor(col).SingleLine()
			})
		}
	})
	return track.Changed()
}

// switchRow is a whole row that toggles *on: its title names the switch
// for assistive technology, and a click anywhere on it flips it. pad gives
// it a settings panel's row padding. It reports whether it changed.
func switchRow(c *mui.Context, title, desc string, on *bool, pad bool) bool {
	r := mui.SwitchBase(c, on).FillWidth().AlignItems(mui.Center).Gap(theme.Space4).Label(title).
		Cursor(mui.CursorPointer)
	if pad {
		r.MinHeight(theme.SizeRow).Padding(theme.Space3, theme.Space5)
	}
	if r.Hovered() && pad {
		r.Background(theme.RoleRaised)
	}
	r.Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			label(c, title).FontWeight(500)
			if desc != "" {
				secondary(c, desc)
			}
		})
		switchTrack(c, *on)
	})
	return r.Changed()
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
