package ref

import (
	"math"

	"github.com/planckdotgg/planck/pkg/pyrowave"
)

// chromaZero is where chroma sits for a neutral colour after the DC shift
// back, 128/255 as in upstream's decoder.
const chromaZero float32 = 128.0 / 255.0

// Matrix holds a YCbCr transform's luma weights.
type Matrix struct{ Kr, Kb float32 }

var (
	BT709  = Matrix{0.2126, 0.0722}
	BT2020 = Matrix{0.2627, 0.0593}
)

func MatrixOf(code uint8) Matrix {
	if code == pyrowave.MatrixBT2020 {
		return BT2020
	}
	return BT709
}

// ToYCbCr converts full-range RGB in [0,1] to Y, Cb, Cr in [0,1].
func (m Matrix) ToYCbCr(r, g, b float32) (y, cb, cr float32) {
	y = m.Kr*r + (1-m.Kr-m.Kb)*g + m.Kb*b
	cb = (b-y)/(2-2*m.Kb) + chromaZero
	cr = (r-y)/(2-2*m.Kr) + chromaZero
	return
}

// ToRGB is the inverse of ToYCbCr.
func (m Matrix) ToRGB(y, cb, cr float32) (r, g, b float32) {
	cb -= chromaZero
	cr -= chromaZero
	kg := 1 - m.Kr - m.Kb
	r = y + (2-2*m.Kr)*cr
	b = y + (2-2*m.Kb)*cb
	g = y - (m.Kb*(2-2*m.Kb)/kg)*cb - (m.Kr*(2-2*m.Kr)/kg)*cr
	return
}

// Image is a picture as three planes of samples in [0,1], each the size of
// the visible picture (chroma at half size under 4:2:0).
type Image struct {
	Width, Height int
	Chroma420     bool
	Planes        [3][]float32
}

func (img *Image) PlaneSize(c int) (int, int) {
	if c != 0 && img.Chroma420 {
		return img.Width / 2, img.Height / 2
	}
	return img.Width, img.Height
}

// FromRGB builds an image from a full-range RGB sampler; 4:2:0 chroma is the
// mean of each 2x2 quad (centre siting).
func FromRGB(w, h int, chroma420 bool, m Matrix, rgb func(x, y int) (r, g, b float32)) *Image {
	img := &Image{Width: w, Height: h, Chroma420: chroma420}
	img.Planes[0] = make([]float32, w*h)
	cw, ch := img.PlaneSize(1)
	img.Planes[1] = make([]float32, cw*ch)
	img.Planes[2] = make([]float32, cw*ch)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := rgb(x, y)
			Y, cb, cr := m.ToYCbCr(r, g, b)
			img.Planes[0][y*w+x] = Y
			if !chroma420 {
				img.Planes[1][y*w+x] = cb
				img.Planes[2][y*w+x] = cr
			}
		}
	}
	if chroma420 {
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				var r, g, b float32
				for d := 0; d < 4; d++ {
					pr, pg, pb := rgb(2*x+d&1, 2*y+d>>1)
					r, g, b = r+pr, g+pg, b+pb
				}
				_, cb, cr := m.ToYCbCr(r/4, g/4, b/4)
				img.Planes[1][y*cw+x] = cb
				img.Planes[2][y*cw+x] = cr
			}
		}
	}
	return img
}

// FromBGRA8 converts a tightly packed BGRA8 picture.
func FromBGRA8(px []byte, w, h int, chroma420 bool, m Matrix) *Image {
	return FromRGB(w, h, chroma420, m, func(x, y int) (r, g, b float32) {
		p := px[(y*w+x)*4:]
		return float32(p[2]) / 255, float32(p[1]) / 255, float32(p[0]) / 255
	})
}

// RGB returns the full-range RGB of pixel (x, y), upsampling 4:2:0 chroma
// bilinearly with centre siting.
func (img *Image) RGB(x, y int, m Matrix) (r, g, b float32) {
	Y := img.Planes[0][y*img.Width+x]
	var cb, cr float32
	if img.Chroma420 {
		cw, ch := img.Width/2, img.Height/2
		fx := float32(x)*0.5 - 0.25
		fy := float32(y)*0.5 - 0.25
		cb = bilinear(img.Planes[1], cw, ch, fx, fy)
		cr = bilinear(img.Planes[2], cw, ch, fx, fy)
	} else {
		cb = img.Planes[1][y*img.Width+x]
		cr = img.Planes[2][y*img.Width+x]
	}
	return m.ToRGB(Y, cb, cr)
}

func bilinear(p []float32, w, h int, fx, fy float32) float32 {
	x0 := int(math.Floor(float64(fx)))
	y0 := int(math.Floor(float64(fy)))
	tx, ty := fx-float32(x0), fy-float32(y0)
	at := func(x, y int) float32 {
		return p[min(max(y, 0), h-1)*w+min(max(x, 0), w-1)]
	}
	top := at(x0, y0)*(1-tx) + at(x0+1, y0)*tx
	bot := at(x0, y0+1)*(1-tx) + at(x0+1, y0+1)*tx
	return top*(1-ty) + bot*ty
}

func clamp01(v float32) float32 { return min(max(v, 0), 1) }

// BGRA8 renders the image to tightly packed BGRA8.
func (img *Image) BGRA8(m Matrix) []byte {
	out := make([]byte, img.Width*img.Height*4)
	for y := 0; y < img.Height; y++ {
		for x := 0; x < img.Width; x++ {
			r, g, b := img.RGB(x, y, m)
			p := out[(y*img.Width+x)*4:]
			p[0] = uint8(clamp01(b)*255 + 0.5)
			p[1] = uint8(clamp01(g)*255 + 0.5)
			p[2] = uint8(clamp01(r)*255 + 0.5)
			p[3] = 255
		}
	}
	return out
}

// PSNR compares two BGRA8 pictures over their colour channels, in dB.
func PSNR(a, b []byte) float64 {
	var sum float64
	n := 0
	for i := 0; i+3 < len(a) && i+3 < len(b); i += 4 {
		for c := 0; c < 3; c++ {
			d := float64(a[i+c]) - float64(b[i+c])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255*float64(n)/sum)
}
