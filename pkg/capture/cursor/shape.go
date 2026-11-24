package cursor

import (
	"encoding/binary"
	"hash/fnv"
)

// MaxSide bounds a Shape's width and height: a larger cursor is scaled
// down to fit.
const MaxSide = 256

// Shape is the image of the system cursor.
type Shape struct {
	W, H       int
	HotX, HotY int
	// DPI is the display's the cursor was sized for; 96 is 100%.
	DPI int
	// RGBA is the image, not premultiplied, rows top down.
	RGBA []byte
}

// ReadShape reads the image of the cursor s reports, s.Handle.
func ReadShape(s State) (Shape, error) { return readShape(s) }

// Hash names the shape by its size, hotspot, DPI and pixels.
func (s Shape) Hash() uint64 {
	h := fnv.New64a()
	var hdr [20]byte
	for i, v := range []int{s.W, s.H, s.HotX, s.HotY, s.DPI} {
		binary.BigEndian.PutUint32(hdr[i*4:], uint32(v))
	}
	h.Write(hdr[:])
	h.Write(s.RGBA)
	return h.Sum64()
}

// Pixels of a cursor's masks: the AND mask keeps what is under the
// cursor, the XOR mask, or a color without alpha, flips it.
const (
	pxOpaque = iota
	pxClear
	pxInvert
)

// fromColor makes RGBA of a color cursor's BGRA pixels and its AND mask,
// one byte per pixel, 1 where the screen shows through. Pixels with alpha
// carry their own transparency; a color cursor without alpha has its mask
// say which pixels are clear, and a set mask under a color inverts the
// screen.
func fromColor(bgra, and []byte, w, h int) []byte {
	out := make([]byte, w*h*4)
	alpha := false
	for i := 3; i < len(bgra); i += 4 {
		if bgra[i] != 0 {
			alpha = true
			break
		}
	}
	kind := make([]byte, w*h)
	for i := range w * h {
		b, g, r, a := bgra[i*4], bgra[i*4+1], bgra[i*4+2], bgra[i*4+3]
		switch {
		case alpha:
			out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = r, g, b, a
			continue
		case and[i] == 0:
			out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = r, g, b, 255
			continue
		case r|g|b == 0:
			kind[i] = pxClear
		default:
			kind[i] = pxInvert
		}
	}
	if !alpha {
		drawInverted(out, kind, w, h)
	}
	return out
}

// fromMono makes RGBA of a monochrome cursor's mask, w x 2h, one byte per
// pixel: the AND mask over the XOR mask. AND 0 draws the XOR bit, black or
// white; AND 1 leaves the screen, inverted where XOR is 1.
func fromMono(mask []byte, w, h int) []byte {
	out := make([]byte, w*h*4)
	kind := make([]byte, w*h)
	for i := range w * h {
		and, xor := mask[i], mask[w*h+i]
		switch {
		case and == 0:
			c := 255 * xor
			out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = c, c, c, 255
		case xor == 0:
			kind[i] = pxClear
		default:
			kind[i] = pxInvert
		}
	}
	drawInverted(out, kind, w, h)
	return out
}

// drawInverted draws the pixels that invert the screen, which no image
// can, black, and the clear pixels around them white, so that an I-beam
// shows on a dark picture and on a light one.
func drawInverted(out, kind []byte, w, h int) {
	for y := range h {
		for x := range w {
			i := y*w + x
			switch kind[i] {
			case pxInvert:
				out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = 0, 0, 0, 255
			case pxClear:
				if besideInvert(kind, w, h, x, y) {
					out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = 255, 255, 255, 255
				}
			}
		}
	}
}

func besideInvert(kind []byte, w, h, x, y int) bool {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			nx, ny := x+dx, y+dy
			if nx >= 0 && ny >= 0 && nx < w && ny < h && kind[ny*w+nx] == pxInvert {
				return true
			}
		}
	}
	return false
}

func fit(s Shape) Shape { return Fit(s, MaxSide) }

// Fit scales a shape larger than side down to fit, nearest pixel, and
// its DPI with it, each pixel the one at its centre, so that it keeps its size on the client.
func Fit(s Shape, side int) Shape {
	side = max(side, 1)
	n := max(s.W, s.H)
	if n <= side {
		return s
	}
	w, h := max(s.W*side/n, 1), max(s.H*side/n, 1)
	px := make([]byte, w*h*4)
	for y := range h {
		for x := range w {
			sx, sy := (2*x+1)*s.W/(2*w), (2*y+1)*s.H/(2*h)
			copy(px[(y*w+x)*4:][:4], s.RGBA[(sy*s.W+sx)*4:][:4])
		}
	}
	return Shape{
		W: w, H: h, HotX: min(s.HotX*w/s.W, w-1), HotY: min(s.HotY*h/s.H, h-1),
		DPI: max(s.DPI*side/n, 1), RGBA: px,
	}
}

// unpackMask makes one byte a pixel, 0 or 1, of a monochrome DIB's rows of
// stride bytes, the leftmost pixel in each byte's high bit.
func unpackMask(bits []byte, stride, w, h int) []byte {
	out := make([]byte, w*h)
	for y := range h {
		for x := range w {
			out[y*w+x] = bits[y*stride+x/8] >> (7 - x%8) & 1
		}
	}
	return out
}
