package ui

import (
	"slices"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// OverlayState is what the stream overlay draws over the stream, as
// OnOverlayChange hears it.
type OverlayState struct {
	// Shown is whether it draws anything.
	Shown bool
	// Rest is set while all it draws is the stream button at rest: the
	// docked tab, or the floating button neither hovered, pressed nor
	// open. A video path the
	// window's UI cannot draw over draws these shapes into its frames.
	Rest *OverlayRest
}

// Live reports whether the overlay draws what only the window's UI can
// show.
func (s OverlayState) Live() bool { return s.Shown && s.Rest == nil }

// OverlayRest is the resting button in the view's coordinates, the view
// ViewW x ViewH: Boxes back to front, then the Planck mark in Mark at
// MarkOpacity when Mark has a size.
type OverlayRest struct {
	ViewW, ViewH float32
	Boxes        []RestBox
	Mark         RestRect
	MarkOpacity  float32
}

// RestRect is a rect in the view, in DIPs.
type RestRect struct{ X, Y, W, H float32 }

// RestBox is a rounded rect filled with Color: red, green, blue and alpha
// from 0 to 1, not premultiplied.
type RestBox struct {
	RestRect
	Radius float32
	Color  [4]float32
}

func (r *OverlayRest) equal(o *OverlayRest) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.ViewW == o.ViewW && r.ViewH == o.ViewH && r.Mark == o.Mark && r.MarkOpacity == o.MarkOpacity &&
		slices.Equal(r.Boxes, o.Boxes)
}

type restKind uint8

const (
	restNone restKind = iota
	restTab
	restButton
)

// markIdleOpacity is the mark's opacity in the button at rest.
const markIdleOpacity = 0.72

func colorOf(c mui.Color, alpha float32) [4]float32 {
	return [4]float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255 * alpha}
}

// restShapes is the button at rest, as bubble and dockTab draw it, less
// the soft shadow and hairline border.
func (o *overlay) restShapes() *OverlayRest {
	r := &OverlayRest{ViewW: o.vw, ViewH: o.vh}
	switch o.restAt {
	case restTab:
		tab, _, _ := o.dockGeom()
		// The tab is rounded on its inner corners only: the box runs
		// past the edge by its radius.
		const radius = 6
		glass := tab
		switch o.dock {
		case DockLeft:
			glass.X, glass.W = glass.X-radius, glass.W+radius
		case DockRight:
			glass.W += radius
		case DockTop:
			glass.Y, glass.H = glass.Y-radius, glass.H+radius
		case DockBottom:
			glass.H += radius
		}
		w, h := float32(2), float32(16)
		if o.dock == DockTop || o.dock == DockBottom {
			w, h = h, w
		}
		w, h = min(w, tab.W), min(h, tab.H)
		r.Boxes = []RestBox{
			{RestRect: restRect(glass), Radius: radius, Color: colorOf(theme.HUDGlassTab, 1)},
			{RestRect: RestRect{X: tab.X + (tab.W-w)/2, Y: tab.Y + (tab.H-h)/2, W: w, H: h}, Radius: 1, Color: colorOf(theme.RoleAccent, 1)},
		}
	case restButton:
		mw, mh := float32(13), float32(20)
		r.Boxes = []RestBox{{RestRect: RestRect{X: o.btn.X, Y: o.btn.Y, W: btnSize, H: btnSize}, Radius: 12,
			Color: colorOf(theme.HUDGlassIdle, o.restOpacity)}}
		r.Mark = RestRect{X: o.btn.X + (btnSize-mw)/2, Y: o.btn.Y + (btnSize-mh)/2, W: mw, H: mh}
		r.MarkOpacity = markIdleOpacity * o.restOpacity
	default:
		return nil
	}
	return r
}

func restRect(r mui.Rect) RestRect { return RestRect{X: r.X, Y: r.Y, W: r.W, H: r.H} }

// rest is the overlay's resting shapes this frame, or nil while it draws
// more than the button at rest.
func (a *App) rest(now time.Time) *OverlayRest {
	o := &a.ovl
	toast := !o.toast.at.IsZero() && now.Sub(o.toast.at) < toastIn+toastHold+toastOut
	if o.menuOpen || o.metrics.shown || toast || now.Before(o.tipUntil) {
		return nil
	}
	return o.restShapes()
}
