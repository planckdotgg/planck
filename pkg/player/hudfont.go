package player

import (
	"fmt"
	"image"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/planckdotgg/planck/assets"
)

// hudGlyphs are the characters the HUD draws; any other shows as a space.
const hudGlyphs = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz.:/%·-"

const (
	hudFontFile = "fonts/MartianMono-SemiCondensedMedium.ttf"
	hudFontPt   = 11
	hudAtlasW   = 512
)

type hudGlyph struct {
	// x, y are the glyph's top left from the pen on the baseline, w, h its
	// size, in pixels; uv its place in the atlas.
	x, y, w, h int
	uv         [4]float32
	ok         bool
}

// hudFont is Martian Mono at one backing scale, rasterized into an R8
// coverage atlas. The font is monospaced: every glyph advances the same.
type hudFont struct {
	scale     float64
	advance   int
	capHeight int
	glyphs    [128]hudGlyph // by rune; middleDot holds '·'
	middleDot hudGlyph
	atlas     []byte
	aw, ah    int
}

func (f *hudFont) glyph(r rune) *hudGlyph {
	switch {
	case r == '·':
		return &f.middleDot
	case r >= 0 && r < 128:
		return &f.glyphs[r]
	}
	return nil
}

func newHUDFont(scale float64) (*hudFont, error) {
	data, err := assets.FS.ReadFile(hudFontFile)
	if err != nil {
		return nil, err
	}
	otf, err := opentype.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("player: HUD font: %w", err)
	}
	face, err := opentype.NewFace(otf, &opentype.FaceOptions{Size: hudFontPt * scale, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("player: HUD font: %w", err)
	}
	defer face.Close()
	f := &hudFont{scale: scale, capHeight: face.Metrics().CapHeight.Round()}
	if adv, ok := face.GlyphAdvance('0'); ok {
		f.advance = adv.Round()
	}
	// Glyph reuses its mask from one call to the next: the first pass
	// places the glyphs, the second draws each as it gets it.
	const gap = 1
	x, y, rowH := gap, gap, 0
	var at [len(hudGlyphs)]image.Point
	for i, r := range hudGlyphs {
		dr, _, _, _, ok := face.Glyph(fixed.Point26_6{}, r)
		if !ok || dr.Empty() {
			continue
		}
		w, h := dr.Dx(), dr.Dy()
		if x+w+gap > hudAtlasW {
			x, y, rowH = gap, y+rowH+gap, 0
		}
		*f.glyph(r) = hudGlyph{x: dr.Min.X, y: dr.Min.Y, w: w, h: h, ok: true}
		at[i] = image.Pt(x, y)
		x += w + gap
		rowH = max(rowH, h)
	}
	f.aw, f.ah = hudAtlasW, y+rowH+gap
	img := image.NewAlpha(image.Rect(0, 0, f.aw, f.ah))
	for i, r := range hudGlyphs {
		g := f.glyph(r)
		if !g.ok {
			continue
		}
		_, mask, mp, _, _ := face.Glyph(fixed.Point26_6{}, r)
		box := image.Rectangle{Min: at[i], Max: at[i].Add(image.Pt(g.w, g.h))}
		draw.Draw(img, box, mask, mp, draw.Src)
		g.uv = [4]float32{float32(box.Min.X) / float32(f.aw), float32(box.Min.Y) / float32(f.ah),
			float32(box.Max.X) / float32(f.aw), float32(box.Max.Y) / float32(f.ah)}
	}
	f.atlas = img.Pix
	return f, nil
}

func px(pt, scale float64) int { return int(math.Round(pt * scale)) }
