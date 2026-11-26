//go:build linux && !android && (amd64 || arm64)

package gpusurface

import (
	"image"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
)

// pictureMode passes frames to a MyGo Bitmap through three NV12 buffers,
// as overlayRects passes rects: the presenting thread fills its back
// buffer and swaps it with the middle one, the main thread swaps the
// middle one with its front buffer when it is newer and shows that,
// which MyGo keeps reading until the next update.
type pictureMode struct {
	redraw func()
	bitmap *ui.Bitmap

	mu     sync.Mutex // the presenting side
	bufs   [3]pictureBuf
	back   uint32
	front  uint32
	middle atomic.Uint32
}

type pictureBuf struct {
	pix           []byte
	width, height int
}

func newPictureMode(redraw func()) *pictureMode {
	p := &pictureMode{redraw: redraw, bitmap: ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))}
	p.back, p.front = 0, 2
	p.middle.Store(1)
	return p
}

// Bitmap is the bitmap the frames show in, in picture mode, nil when the
// surface shows them in a subsurface of its own. Draw it under the UI,
// filling the content, as ui.Image(c, b).Fill().Fit(ui.Contain), and call
// Refresh first in the same view. The bitmap shows black until the first
// frame.
func (s *Surface) Bitmap() *ui.Bitmap {
	if s.pic == nil {
		return nil
	}
	return s.pic.bitmap
}

// Refresh puts the newest frame in the bitmap, if one came since, and
// reports whether it did. Call it on the main thread, in the view that
// draws Bitmap.
func (s *Surface) Refresh() bool {
	if s.pic == nil {
		return false
	}
	return s.pic.refresh()
}

func (p *pictureMode) refresh() bool {
	if p.middle.Load()&overlayFresh == 0 {
		return false
	}
	p.front = p.middle.Swap(p.front) &^ overlayFresh
	b := &p.bufs[p.front]
	if b.width <= 0 || b.height <= 0 {
		return false
	}
	w, h := b.width, b.height
	cw := (w + 1) / 2 * 2
	p.bitmap.UpdateYUV(&ui.YUVImage{
		Format: ui.NV12, Width: w, Height: h, Matrix: ui.BT709,
		Planes:  [3][]byte{b.pix[:w*h], b.pix[w*h:]},
		Strides: [3]int{w, cw},
	})
	return true
}

// present copies d into the back buffer as 8-bit NV12, tightly packed,
// and passes it on.
func (p *pictureMode) present(d *DMABuf) error {
	if !d.hasCPU() || d.Width <= 0 || d.Height <= 0 {
		return ErrFormat
	}
	switch d.Fourcc {
	case FourccNV12, FourccP010:
	default:
		return ErrFormat
	}
	p.mu.Lock()
	b := &p.bufs[p.back]
	w, h := d.Width, d.Height
	cw, ch := (w+1)/2*2, (h+1)/2
	if need := w*h + cw*ch; cap(b.pix) < need {
		b.pix = make([]byte, need)
	} else {
		b.pix = b.pix[:need]
	}
	b.width, b.height = w, h
	copyNV12(b.pix[:w*h], w, b.pix[w*h:], cw, d, w, h)
	p.back = p.middle.Swap(p.back|overlayFresh) &^ overlayFresh
	p.mu.Unlock()
	if p.redraw != nil {
		p.redraw()
	}
	return nil
}

// copyNV12 copies d's planes, NV12 or P010, into 8-bit luma y (rows of
// ys bytes) and interleaved chroma c (rows of cs bytes).
func copyNV12(y []byte, ys int, c []byte, cs int, d *DMABuf, w, h int) {
	ch := (h + 1) / 2
	cw := (w + 1) / 2 * 2
	luma, chroma := d.plane(0, h), d.plane(1, ch)
	lp, cp := int(d.Pitches[0]), int(d.Pitches[1])
	if d.Fourcc == FourccNV12 {
		for r := range h {
			copy(y[r*ys:r*ys+w], luma[r*lp:])
		}
		for r := range ch {
			copy(c[r*cs:r*cs+cw], chroma[r*cp:])
		}
		return
	}
	// P010: 16-bit little-endian samples, the 10 bits high.
	for r := range h {
		src, dst := luma[r*lp:], y[r*ys:r*ys+w]
		for x := range dst {
			dst[x] = src[2*x+1]
		}
	}
	for r := range ch {
		src, dst := chroma[r*cp:], c[r*cs:r*cs+cw]
		for x := range dst {
			dst[x] = src[2*x+1]
		}
	}
}
