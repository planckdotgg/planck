//go:build linux && !android && (amd64 || arm64)

package gpusurface

import "encoding/binary"

// yuvToXRGB converts d, NV12 or P010 in memory (BT.709 video range), into
// dst, XRGB8888 rows of stride bytes, for a compositor that takes neither
// in wl_shm. P010 converts from its 8 high bits.
func yuvToXRGB(dst []byte, stride int, d *DMABuf) {
	w, h := d.Width, d.Height
	luma, chroma := d.plane(0, h), d.plane(1, (h+1)/2)
	lp, cp := int(d.Pitches[0]), int(d.Pitches[1])
	sample, step := 0, 1
	if d.Fourcc == FourccP010 {
		sample, step = 1, 2
	}
	for r := range h {
		yrow := luma[r*lp:]
		crow := chroma[(r/2)*cp:]
		out := dst[r*stride : r*stride+4*w]
		for x := range w {
			c := (x / 2) * 2 * step
			binary.LittleEndian.PutUint32(out[4*x:], bt709(int(yrow[x*step+sample]), int(crow[c+sample]), int(crow[c+step+sample])))
		}
	}
}

// bt709 is one video-range BT.709 sample as 0xffRRGGBB, in 16.16 fixed
// point.
func bt709(y, u, v int) uint32 {
	c := (y - 16) * 76309
	u, v = u-128, v-128
	r := clamp8((c + 117489*v + 1<<15) >> 16)
	g := clamp8((c - 13954*u - 34903*v + 1<<15) >> 16)
	b := clamp8((c + 138438*u + 1<<15) >> 16)
	return 0xff000000 | r<<16 | g<<8 | b
}

func clamp8(v int) uint32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint32(v)
}
