package main

import "github.com/planckdotgg/planck/pkg/wire"

// cursorCache keeps the native cursors made of the host's shapes, the most
// recently used first, up to wire.CursorCacheSize, and frees each one it
// drops. Main thread.
type cursorCache[K comparable, V any] struct {
	entries []cursorEntry[K, V]
	free    func(V)
}

type cursorEntry[K comparable, V any] struct {
	key K
	val V
}

// get returns the cursor of k, made by build on a miss; ok is false when
// build fails.
func (c *cursorCache[K, V]) get(k K, build func() (V, bool)) (v V, ok bool) {
	for i, e := range c.entries {
		if e.key == k {
			copy(c.entries[1:i+1], c.entries[:i])
			c.entries[0] = e
			return e.val, true
		}
	}
	if v, ok = build(); !ok {
		return v, false
	}
	c.entries = append(c.entries, cursorEntry[K, V]{})
	copy(c.entries[1:], c.entries)
	c.entries[0] = cursorEntry[K, V]{k, v}
	if n := len(c.entries); n > wire.CursorCacheSize {
		c.free(c.entries[n-1].val)
		c.entries = c.entries[:n-1]
	}
	return v, true
}

// close frees every cursor.
func (c *cursorCache[K, V]) close() {
	for _, e := range c.entries {
		c.free(e.val)
	}
	c.entries = nil
}

// cursorScale is how much larger than its pixels the host's cursor shows
// on a display of dpi: as large as on the host, in the size of the
// client's interface rather than of the picture.
func cursorScale(s *wire.CursorShape, dpi int) float64 {
	return float64(dpi) / float64(max(s.DPI, 1))
}

// scaleRGBA resamples a w x h RGBA image, not premultiplied, to nw x nh,
// bilinear, mixing premultiplied so that clear pixels add no dark fringe.
func scaleRGBA(px []byte, w, h, nw, nh int) []byte {
	out := make([]byte, nw*nh*4)
	at := func(x, y int) (r, g, b, a float64) {
		x, y = min(max(x, 0), w-1), min(max(y, 0), h-1)
		p := px[(y*w+x)*4:]
		a = float64(p[3]) / 255
		return float64(p[0]) * a, float64(p[1]) * a, float64(p[2]) * a, a
	}
	for y := range nh {
		sy := (float64(y)+0.5)*float64(h)/float64(nh) - 0.5
		y0 := int(sy)
		if sy < 0 {
			y0 = -1
		}
		fy := sy - float64(y0)
		for x := range nw {
			sx := (float64(x)+0.5)*float64(w)/float64(nw) - 0.5
			x0 := int(sx)
			if sx < 0 {
				x0 = -1
			}
			fx := sx - float64(x0)
			var acc [4]float64
			for _, s := range [4]struct {
				dx, dy int
				k      float64
			}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
				r, g, b, a := at(x0+s.dx, y0+s.dy)
				acc[0] += r * s.k
				acc[1] += g * s.k
				acc[2] += b * s.k
				acc[3] += a * s.k
			}
			o := out[(y*nw+x)*4:]
			if acc[3] > 0 {
				o[0], o[1], o[2] = byte(min(acc[0]/acc[3]+0.5, 255)), byte(min(acc[1]/acc[3]+0.5, 255)), byte(min(acc[2]/acc[3]+0.5, 255))
			}
			o[3] = byte(min(acc[3]*255+0.5, 255))
		}
	}
	return out
}
