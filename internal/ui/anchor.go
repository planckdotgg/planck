package ui

import mui "github.com/egoist/mygo/ui"

// Anchor is what a placed widget keeps its distance from along one axis
// of the view: the start edge (left or top), the centre, or the end edge
// (right or bottom).
type Anchor uint8

const (
	// AnchorNone is an axis not placed yet.
	AnchorNone Anchor = iota
	AnchorStart
	AnchorCenter
	AnchorEnd
)

// centerReach is how near the view's centre a widget's centre must come
// to rest for it to keep to the centre on that axis.
const centerReach = 32

// Placement is where a widget sits in the view, by an anchor and an offset
// in DIPs on each axis: from the start edge to the widget's start edge,
// from the end edge to the widget's end edge, or, signed, from the view's
// centre to the widget's centre. It does not change as the view or the
// widget resizes.
type Placement struct {
	AnchorX, AnchorY Anchor
	OffsetX, OffsetY float32
}

func (p Placement) set() bool { return p.AnchorX != AnchorNone && p.AnchorY != AnchorNone }

// resolve is the top-left corner of a sw x sh widget placed by p in a
// vw x vh view.
func (p Placement) resolve(vw, vh, sw, sh float32) mui.Point {
	return mui.Point{X: resolveAxis(p.AnchorX, p.OffsetX, vw, sw), Y: resolveAxis(p.AnchorY, p.OffsetY, vh, sh)}
}

func resolveAxis(a Anchor, off, n, s float32) float32 {
	switch a {
	case AnchorEnd:
		return n - s - off
	case AnchorCenter:
		return n/2 + off - s/2
	}
	return off
}

// placementAt anchors a sw x sh widget let go with its top-left corner at
// p, where b is where that corner may go. A widget held against a side of
// b keeps inset from that edge, so that a wider limit there, such as the
// docked button's, is applied as the view is drawn and not kept.
func placementAt(p mui.Point, sw, sh, vw, vh float32, b mui.Rect, inset float32) Placement {
	ax, ox := anchorAxis(p.X, sw, vw, b.X, b.X+b.W, inset)
	ay, oy := anchorAxis(p.Y, sh, vh, b.Y, b.Y+b.H, inset)
	return Placement{AnchorX: ax, AnchorY: ay, OffsetX: ox, OffsetY: oy}
}

func anchorAxis(v, s, n, lo, hi, inset float32) (Anchor, float32) {
	start, end := v, n-v-s
	atLo, atHi := v <= lo, v >= hi
	if atLo && atHi {
		atLo, atHi = start <= end, start > end
	}
	switch {
	case atLo:
		return AnchorStart, inset
	case atHi:
		return AnchorEnd, inset
	case abs32(v+s/2-n/2) <= centerReach:
		return AnchorCenter, v + s/2 - n/2
	case start <= end:
		return AnchorStart, start
	}
	return AnchorEnd, end
}
