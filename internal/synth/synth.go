// Package synth draws deterministic desktop-like test frames: gradients,
// fine text-like detail, and a vertical scroll per frame index, so encoders
// see realistic spatial detail and motion without a live display.
package synth

// BGRA fills a width*height*4 buffer for frame index i.
func BGRA(dst []byte, width, height, i int) {
	scroll := i * 7
	for y := 0; y < height; y++ {
		sy := y + scroll
		row := dst[y*width*4:]
		for x := 0; x < width; x++ {
			r, g, b := pixel(x, sy, width, height, i)
			row[x*4+0] = b
			row[x*4+1] = g
			row[x*4+2] = r
			row[x*4+3] = 255
		}
	}
}

func hash(x, y uint32) uint32 {
	h := x*374761393 + y*668265263
	h = (h ^ h>>13) * 1274126177
	return h ^ h>>16
}

func pixel(x, y, width, height, i int) (r, g, b uint8) {
	r = uint8(x * 255 / width)
	g = uint8((y % height) * 255 / height)
	b = uint8(128 + (x+y)%64)
	// Text-like blocks in the middle band: 8x16 cells of 1-pixel strokes.
	if x > width/8 && x < width*7/8 && (y/16)%3 != 0 {
		cell := hash(uint32(x/8), uint32(y/16))
		if cell&3 != 0 && hash(uint32(x), uint32(y))&(cell>>8|1)&1 != 0 {
			return 20, 20, 24
		}
	}
	// A moving square stands in for game motion.
	cx, cy := (i*13)%width, height/2
	if x > cx && x < cx+height/4 && y%height > cy-height/8 && y%height < cy+height/8 {
		return 230, 80, 40
	}
	return
}

// yuv converts with BT.709 limited range and 8-bit output.
func yuv(r, g, b uint8) (y, u, v int) {
	R, G, B := int(r), int(g), int(b)
	y = (47*R+157*G+16*B)/256 + 16
	u = (-26*R-87*G+112*B)/256 + 128
	v = (112*R-102*G-10*B)/256 + 128
	return
}

// NV12 fills a Y plane (width*height) then interleaved UV (width*height/2).
func NV12(dst []byte, width, height, i int) {
	planar(width, height, i, func(x, y, Y int) { dst[y*width+x] = byte(Y) },
		func(x, y, U, V int) {
			o := width*height + y*width + x
			dst[o], dst[o+1] = byte(U), byte(V)
		})
}

// P010 is NV12 with 16-bit little-endian samples holding 10 bits in the top bits.
func P010(dst []byte, width, height, i int) {
	put := func(o, v int) {
		w := uint16(v<<2) << 6
		dst[o], dst[o+1] = byte(w), byte(w>>8)
	}
	planar(width, height, i, func(x, y, Y int) { put((y*width+x)*2, Y) },
		func(x, y, U, V int) {
			o := (width*height + y*width + x) * 2
			put(o, U)
			put(o+2, V)
		})
}

func planar(width, height, i int, luma func(x, y, Y int), chroma func(x, y, U, V int)) {
	scroll := i * 7
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b := pixel(x, y+scroll, width, height, i)
			Y, U, V := yuv(r, g, b)
			luma(x, y, Y)
			if x%2 == 0 && y%2 == 0 {
				chroma(x, y/2, U, V)
			}
		}
	}
}

// RGB10A2 packs DXGI_FORMAT_R10G10B10A2_UNORM pixels.
func RGB10A2(dst []byte, width, height, i int) {
	scroll := i * 7
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b := pixel(x, y+scroll, width, height, i)
			w := uint32(r)<<2 | uint32(g)<<12 | uint32(b)<<22 | 3<<30
			o := (y*width + x) * 4
			dst[o], dst[o+1], dst[o+2], dst[o+3] = byte(w), byte(w>>8), byte(w>>16), byte(w>>24)
		}
	}
}
