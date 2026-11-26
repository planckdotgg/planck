//go:build darwin || windows

package main

import (
	"image/color"
	"math"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// The mark's two fills, as planck-mark-glyph.svg has them.
var (
	markBone  = [3]float32{0xF2, 0xEF, 0xEA}
	markEmber = [3]float32{0xFF, 0x7A, 0x1F}
)

// markAtlas is the Planck mark at w x h pixels as overlay coverage: its
// bone fill in the atlas's left half and its ember fill in the right.
type markAtlas struct {
	w, h        int
	pix         []byte
	aw, ah      int
	bone, ember [4]float32 // UVs
}

// newMarkAtlas draws the mark and splits each pixel into how much of it
// each fill covers, which its green and blue tell apart.
func newMarkAtlas(w, h int) *markAtlas {
	img := theme.Mark.Image(w, h, color.White)
	const gap = 1
	m := &markAtlas{w: w, h: h, aw: 2*w + 3*gap, ah: h + 2*gap}
	m.pix = make([]byte, m.aw*m.ah)
	det := markBone[1]*markEmber[2] - markEmber[1]*markBone[2]
	for y := range h {
		for x := range w {
			i := img.PixOffset(x, y)
			g, b := float32(img.Pix[i+1]), float32(img.Pix[i+2])
			bone := (g*markEmber[2] - markEmber[1]*b) / det
			ember := (markBone[1]*b - markBone[2]*g) / det
			row := (y + gap) * m.aw
			m.pix[row+gap+x] = coverage(bone)
			m.pix[row+2*gap+w+x] = coverage(ember)
		}
	}
	aw, ah := float32(m.aw), float32(m.ah)
	m.bone = [4]float32{gap / aw, gap / ah, float32(gap+w) / aw, float32(gap+h) / ah}
	m.ember = [4]float32{float32(2*gap+w) / aw, gap / ah, float32(2*gap+2*w) / aw, float32(gap+h) / ah}
	return m
}

func coverage(v float32) byte { return byte(math.Round(float64(min(max(v, 0), 1) * 255))) }

// markSize is the mark's size in pixels in a dw-wide drawable showing a
// view of rest's.
func markSize(rest *ui.OverlayRest, dw, dh int) (int, int) {
	sx, sy := float32(dw)/rest.ViewW, float32(dh)/rest.ViewH
	return int(math.Round(float64(rest.Mark.W * sx))), int(math.Round(float64(rest.Mark.H * sy)))
}

// restRects lays rest out in a dw x dh drawable, mark its mark at
// markSize, or nil when rest has none.
func restRects(rest *ui.OverlayRest, dw, dh int, mark *markAtlas) []gpusurface.OverlayRect {
	if rest == nil || rest.ViewW <= 0 || rest.ViewH <= 0 {
		return nil
	}
	sx, sy := float32(dw)/rest.ViewW, float32(dh)/rest.ViewH
	out := make([]gpusurface.OverlayRect, 0, len(rest.Boxes)+2)
	for _, b := range rest.Boxes {
		out = append(out, gpusurface.OverlayRect{X: b.X * sx, Y: b.Y * sy, W: b.W * sx, H: b.H * sy,
			Radius: b.Radius * min(sx, sy), Color: b.Color})
	}
	if mark != nil && rest.Mark.W > 0 {
		x, y := float32(math.Round(float64(rest.Mark.X*sx))), float32(math.Round(float64(rest.Mark.Y*sy)))
		w, h := float32(mark.w), float32(mark.h)
		fill := func(c [3]float32) [4]float32 { return [4]float32{c[0] / 255, c[1] / 255, c[2] / 255, rest.MarkOpacity} }
		out = append(out,
			gpusurface.OverlayRect{X: x, Y: y, W: w, H: h, Color: fill(markBone), UV: mark.bone},
			gpusurface.OverlayRect{X: x, Y: y, W: w, H: h, Color: fill(markEmber), UV: mark.ember})
	}
	return out
}
