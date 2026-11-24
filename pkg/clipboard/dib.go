package clipboard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
)

var errDIB = errors.New("clipboard: unsupported bitmap")

const (
	biRGB       = 0
	biBitfields = 3
	maxPixels   = 1 << 26
)

// dibToPNG converts a Windows CF_DIB or CF_DIBV5 bitmap (a BITMAPINFOHEADER
// or larger header, then pixels) of 24 or 32 bits per pixel to PNG. 32-bit
// pixels keep their alpha only when some pixel has a non-zero one: most
// apps leave it zero and mean opaque.
func dibToPNG(dib []byte) ([]byte, error) {
	if len(dib) < 40 {
		return nil, errDIB
	}
	le := binary.LittleEndian
	hdr := int(le.Uint32(dib))
	w := int(int32(le.Uint32(dib[4:])))
	h := int(int32(le.Uint32(dib[8:])))
	bpp := int(le.Uint16(dib[14:]))
	comp := le.Uint32(dib[16:])
	colors := int(le.Uint32(dib[32:]))
	if hdr < 40 || hdr > len(dib) || w <= 0 || h == 0 || (bpp != 24 && bpp != 32) || colors != 0 {
		return nil, errDIB
	}
	topDown := h < 0
	if topDown {
		h = -h
	}
	if w*h > maxPixels {
		return nil, errDIB
	}
	masks := [4]uint32{0x00ff0000, 0x0000ff00, 0x000000ff, 0xff000000}
	off := hdr
	switch {
	case comp == biBitfields && bpp == 32 && hdr >= 56:
		for i := range 4 {
			masks[i] = le.Uint32(dib[40+4*i:])
		}
	case comp == biBitfields && bpp == 32 && hdr == 40 && len(dib) >= 52:
		// The three masks follow a plain BITMAPINFOHEADER.
		for i := range 3 {
			masks[i] = le.Uint32(dib[40+4*i:])
		}
		masks[3] = 0
		off = 52
	case comp != biRGB:
		return nil, errDIB
	}
	stride := (w*bpp/8 + 3) &^ 3
	if off+stride*h > len(dib) {
		return nil, errDIB
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	hasAlpha := false
	for y := range h {
		row := dib[off+stride*y:]
		dy := h - 1 - y
		if topDown {
			dy = y
		}
		for x := range w {
			var c color.NRGBA
			if bpp == 24 {
				c = color.NRGBA{row[3*x+2], row[3*x+1], row[3*x], 0xff}
			} else {
				v := le.Uint32(row[4*x:])
				c = color.NRGBA{field(v, masks[0]), field(v, masks[1]), field(v, masks[2]), field(v, masks[3])}
				hasAlpha = hasAlpha || c.A != 0
			}
			img.SetNRGBA(x, dy, c)
		}
	}
	if bpp == 32 && !hasAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// field extracts the 8-bit value of mask from v.
func field(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := 0
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	bits := 0
	for m := mask; m&1 == 1; m >>= 1 {
		bits++
	}
	x := (v >> shift) & mask
	if bits >= 8 {
		return uint8(x >> (bits - 8))
	}
	return uint8(x * 255 / mask)
}

// pngToDIB converts a PNG to a CF_DIB bitmap: a BITMAPINFOHEADER and
// bottom-up 32-bit BGRA rows, for apps that do not read the PNG format.
func pngToDIB(data []byte) ([]byte, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, errDIB
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	r := img.Bounds()
	w, h := r.Dx(), r.Dy()
	dib := make([]byte, 40+4*w*h)
	le := binary.LittleEndian
	le.PutUint32(dib, 40)
	le.PutUint32(dib[4:], uint32(w))
	le.PutUint32(dib[8:], uint32(h))
	le.PutUint16(dib[12:], 1)
	le.PutUint16(dib[14:], 32)
	le.PutUint32(dib[20:], uint32(4*w*h))
	for y := range h {
		row := dib[40+4*w*(h-1-y):]
		for x := range w {
			c := color.NRGBAModel.Convert(img.At(r.Min.X+x, r.Min.Y+y)).(color.NRGBA)
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = c.B, c.G, c.R, c.A
		}
	}
	return dib, nil
}
