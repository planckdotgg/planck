//go:build linux && !android && (amd64 || arm64)

package gpusurface

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/egoist/mygo/gpusurface/wl"
)

var errNoSubsurfaces = errors.New("gpusurface: the compositor has no wl_subcompositor or wp_viewporter")

// wl_shm's own codes for its two first formats; the others are fourccs.
const (
	shmARGB8888 = 0
	shmXRGB8888 = 1
)

// waySurface is the Wayland half of a Surface: a backdrop subsurface of
// the toplevel, below it, filling the content with black, and the video's
// subsurface above the backdrop, fitted in it. Both are desync, and take
// no input. The video's place is the backdrop's state, which the surface
// commits itself; the backdrop's place is the toplevel's, which GTK
// commits as it redraws (queueParentCommit asks it to).
type waySurface struct {
	s *Surface
	q *wl.Queue

	compositor, subcompositor, viewporter, shm, dmabuf, presentation wl.Proxy

	back, backSub, backView    wl.Proxy
	video, videoSub, videoView wl.Proxy
	black                      *shmBuffer

	formatsMu  sync.Mutex
	shmFormats map[uint32]bool
	modifiers  map[uint32][]uint64

	// fit is the video's place in the backdrop as last committed.
	fit [4]int

	// clock is the clock wp_presentation reports on.
	clock atomic.Int32

	imports map[uint64]*importedBuffer
	shmBufs []*shmBuffer

	draw uintptr // gtk_widget_queue_draw
}

// importedBuffer is a frame's wl_buffer, made of its DMA-BUFs or its
// memfd, kept for the frame's pool.
type importedBuffer struct {
	buf    wl.Proxy
	failed bool
}

// shmBuffer is one of the surface's own wl_shm buffers, which frames in
// memory are copied or converted into.
type shmBuffer struct {
	buf            wl.Proxy
	mem            []byte
	width, height  int
	stride, format uint32
	busy           atomic.Bool
}

func newWaySurface(s *Surface, opts Options) (*waySurface, error) {
	q, err := wl.NewQueue(s.native.WLDisplay)
	if err != nil {
		return nil, err
	}
	w := &waySurface{s: s, q: q, shmFormats: map[uint32]bool{}, modifiers: map[uint32][]uint64{}, imports: map[uint64]*importedBuffer{}}
	w.compositor, _ = q.Bind(wl.Core("wl_compositor"), 4)
	w.subcompositor, _ = q.Bind(wl.Core("wl_subcompositor"), 1)
	w.viewporter, _ = q.Bind(wl.WpViewporter, 1)
	if w.compositor == 0 || w.subcompositor == 0 || w.viewporter == 0 {
		w.destroyGlobals()
		q.Close()
		return nil, errNoSubsurfaces
	}
	w.shm, _ = q.Bind(wl.Core("wl_shm"), 1)
	if w.shm != 0 {
		w.shm.Listen(func(op uint32, e wl.Event) {
			if op == 0 {
				w.formatsMu.Lock()
				w.shmFormats[e.Uint(0)] = true
				w.formatsMu.Unlock()
			}
		})
	}
	w.dmabuf, _ = q.Bind(wl.ZwpLinuxDmabufV1, 3)
	if w.dmabuf != 0 {
		w.dmabuf.Listen(func(op uint32, e wl.Event) {
			w.formatsMu.Lock()
			defer w.formatsMu.Unlock()
			switch op {
			case 0: // format, before version 3
				w.modifiers[e.Uint(0)] = append(w.modifiers[e.Uint(0)], modifierInvalid)
			case 1: // modifier
				w.modifiers[e.Uint(0)] = append(w.modifiers[e.Uint(0)], uint64(e.Uint(1))<<32|uint64(e.Uint(2)))
			}
		})
	}
	w.presentation, _ = q.Bind(wl.WpPresentation, 1)
	w.clock.Store(clockMonotonic)
	if w.presentation != 0 {
		w.presentation.Listen(func(op uint32, e wl.Event) {
			if op == 0 {
				w.clock.Store(int32(e.Uint(0)))
			}
		})
	}
	if err := q.Roundtrip(); err != nil {
		w.destroyGlobals()
		q.Close()
		return nil, err
	}
	if w.shm == 0 {
		w.destroyGlobals()
		q.Close()
		return nil, errNoSubsurfaces
	}
	if err := w.makeSurfaces(); err != nil {
		w.close()
		return nil, err
	}
	if g, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
		w.draw, _ = purego.Dlsym(g, "gtk_widget_queue_draw")
	}
	go q.Run()
	w.applyLayout(true)
	w.queueParentCommit()
	return w, nil
}

func (w *waySurface) makeSurfaces() error {
	var err error
	if w.back, w.backSub, w.backView, err = w.subsurface(wl.Proxy(w.s.native.WLSurface)); err != nil {
		return err
	}
	if w.video, w.videoSub, w.videoView, err = w.subsurface(w.back); err != nil {
		return err
	}
	w.backSub.Marshal(wl.SubsurfacePlaceBelow, nil, 0, w.s.native.WLSurface)
	black, err := w.newShmBuffer(1, 1, shmXRGB8888, 4)
	if err != nil {
		return err
	}
	w.black = black
	w.back.Marshal(wl.SurfaceAttach, nil, 0, uintptr(black.buf), 0, 0)
	w.back.Marshal(wl.SurfaceDamageBuffer, nil, 0, 0, 0, 1, 1)
	return nil
}

// subsurface makes a desync subsurface of parent, with a viewport and no
// input.
func (w *waySurface) subsurface(parent wl.Proxy) (surf, sub, view wl.Proxy, err error) {
	surf = w.compositor.Marshal(wl.CompositorCreateSurface, wl.Core("wl_surface"), w.compositor.Version(), 0)
	if surf == 0 {
		return 0, 0, 0, errors.New("gpusurface: wl_compositor.create_surface failed")
	}
	sub = w.subcompositor.Marshal(wl.SubcompositorGetSubsurface, wl.Core("wl_subsurface"), 1, 0, uintptr(surf), uintptr(parent))
	view = w.viewporter.Marshal(wl.ViewporterGetViewport, wl.WpViewport, 1, 0, uintptr(surf))
	if sub == 0 || view == 0 {
		return surf, sub, view, errors.New("gpusurface: no subsurface")
	}
	sub.Marshal(wl.SubsurfaceSetDesync, nil, 0)
	empty := w.compositor.Marshal(wl.CompositorCreateRegion, wl.Core("wl_region"), w.compositor.Version(), 0)
	surf.Marshal(wl.SurfaceSetInputRegion, nil, 0, uintptr(empty))
	empty.Destroy(0)
	return surf, sub, view, nil
}

// queueParentCommit has GTK redraw the window, which commits the
// toplevel and with it the backdrop's place. Main thread.
func (w *waySurface) queueParentCommit() {
	if w.draw != 0 {
		call(w.draw, w.s.window)
	}
}

// applyLayout places and sizes the backdrop on the content.
func (w *waySurface) applyLayout(force bool) error {
	n, moved := w.s.area()
	if !moved && !force {
		return nil
	}
	if n.AreaW <= 0 || n.AreaH <= 0 {
		return nil
	}
	w.backSub.Marshal(wl.SubsurfaceSetPosition, nil, 0, wl.Int(n.AreaX), wl.Int(n.AreaY))
	w.backView.Marshal(wl.ViewportSetDest, nil, 0, wl.Int(n.AreaW), wl.Int(n.AreaH))
	w.setOpaque(w.back, n.AreaW, n.AreaH)
	w.back.Marshal(wl.SurfaceCommit, nil, 0)
	w.fit = [4]int{}
	w.q.Flush()
	return nil
}

func (w *waySurface) setOpaque(surf wl.Proxy, width, height int) {
	r := w.compositor.Marshal(wl.CompositorCreateRegion, wl.Core("wl_region"), w.compositor.Version(), 0)
	r.Marshal(wl.RegionAdd, nil, 0, 0, 0, wl.Int(width), wl.Int(height))
	surf.Marshal(wl.SurfaceSetOpaque, nil, 0, uintptr(r))
	r.Destroy(0)
}

func (w *waySurface) present(d *DMABuf, tag, submitted int64, rec int) error {
	w.applyLayout(false)
	buf, err := w.bufferFor(d)
	if err != nil {
		return err
	}
	n, _ := w.s.area()
	x, y, fw, fh := FitRect(d.Width, d.Height, n.AreaW, n.AreaH)
	moved := false
	if fit := [4]int{x, y, fw, fh}; fit != w.fit {
		w.fit, moved = fit, true
		w.videoSub.Marshal(wl.SubsurfaceSetPosition, nil, 0, wl.Int(x), wl.Int(y))
		w.videoView.Marshal(wl.ViewportSetDest, nil, 0, wl.Int(max(fw, 1)), wl.Int(max(fh, 1)))
		w.setOpaque(w.video, fw, fh)
	}
	w.video.Marshal(wl.SurfaceAttach, nil, 0, uintptr(buf), 0, 0)
	w.video.Marshal(wl.SurfaceDamageBuffer, nil, 0, 0, 0, wl.Int(d.Width), wl.Int(d.Height))
	if w.presentation != 0 && (rec >= 0 || w.s.presented.Load() != nil) {
		w.feedback(tag, submitted, rec)
	}
	w.video.Marshal(wl.SurfaceCommit, nil, 0)
	if moved {
		// The video's place is the backdrop's state.
		w.back.Marshal(wl.SurfaceCommit, nil, 0)
	}
	w.q.Flush()
	if rec >= 0 {
		w.s.timings.recs[rec].Encoded = Now()
	}
	return nil
}

// feedback asks when the commit that follows reaches the screen.
func (w *waySurface) feedback(tag, submitted int64, rec int) {
	fb := w.presentation.Marshal(wl.PresentationFeedback, wl.WpPresentationFeedback, 1, uintptr(w.video), 0)
	if fb == 0 {
		return
	}
	fb.Listen(func(op uint32, e wl.Event) {
		var at int64
		switch op {
		case 1: // presented: tv_sec_hi, tv_sec_lo, tv_nsec, refresh, seq_hi, seq_lo, flags
			at = (int64(e.Uint(0))<<32|int64(e.Uint(1)))*int64(time.Second) + int64(e.Uint(2))
			if c := w.clock.Load(); c != clockMonotonic {
				// Now's clock, by the offset between the two now.
				at += Now() - clockNow(c)
			}
		case 2: // discarded
		default:
			return
		}
		fb.Free()
		w.s.timings.resolve(rec, at)
		if f := w.s.presented.Load(); f != nil {
			(*f)(tag, submitted, at)
		}
	})
}

// bufferFor is the wl_buffer showing d.
func (w *waySurface) bufferFor(d *DMABuf) (wl.Proxy, error) {
	if d.Width <= 0 || d.Height <= 0 || d.Planes <= 0 {
		return 0, ErrFormat
	}
	if d.hasFDs() && w.takesDMABuf(d.Fourcc, d.Modifier) {
		if b, err := w.imported(d, w.importDMABuf); err == nil {
			return b, nil
		}
	}
	if d.Memory && d.FDs[0] > 0 && w.takesShm(d.Fourcc) && d.Planes <= 2 {
		if b, err := w.imported(d, w.importMemfd); err == nil {
			return b, nil
		}
	}
	if !d.hasCPU() {
		return 0, fmt.Errorf("%w: %s frame the compositor cannot import, with no planes in memory", ErrFormat, fourccString(d.Fourcc))
	}
	return w.copyFrame(d)
}

func (w *waySurface) takesDMABuf(fourcc uint32, modifier uint64) bool {
	w.formatsMu.Lock()
	defer w.formatsMu.Unlock()
	for _, m := range w.modifiers[fourcc] {
		if m == modifier {
			return true
		}
	}
	return false
}

func (w *waySurface) takesShm(fourcc uint32) bool {
	w.formatsMu.Lock()
	defer w.formatsMu.Unlock()
	return w.shmFormats[shmCode(fourcc)]
}

func shmCode(fourcc uint32) uint32 {
	switch fourcc {
	case FourccARGB8888:
		return shmARGB8888
	case FourccXRGB8888:
		return shmXRGB8888
	}
	return fourcc
}

// imported returns d's cached buffer, making it with mk the first time
// d's serial is seen. A failed import is remembered, so frames of that
// pool go the copy's way at once.
func (w *waySurface) imported(d *DMABuf, mk func(*DMABuf) (wl.Proxy, error)) (wl.Proxy, error) {
	if b, ok := w.imports[d.Serial]; ok {
		if b.failed {
			return 0, ErrFormat
		}
		return b.buf, nil
	}
	if len(w.imports) >= 64 {
		for k, b := range w.imports {
			b.buf.Destroy(0)
			delete(w.imports, k)
		}
	}
	buf, err := mk(d)
	w.imports[d.Serial] = &importedBuffer{buf: buf, failed: err != nil}
	return buf, err
}

// importDMABuf makes a wl_buffer of d's DMA-BUFs and waits for the
// compositor to take or refuse it (create, not create_immed: a refusal of
// create_immed may end the connection, GTK's too).
func (w *waySurface) importDMABuf(d *DMABuf) (wl.Proxy, error) {
	params := w.dmabuf.Marshal(wl.DmabufCreateParams, wl.ZwpLinuxBufferParamsV1, w.dmabuf.Version(), 0)
	if params == 0 {
		return 0, errors.New("gpusurface: create_params failed")
	}
	type result struct {
		buf wl.Proxy
		ok  bool
	}
	done := make(chan result, 1)
	params.Listen(func(op uint32, e wl.Event) {
		switch op {
		case 0:
			done <- result{e.Proxy(0), true}
		case 1:
			done <- result{}
		}
	})
	for i := range d.Planes {
		params.Marshal(wl.ParamsAdd, nil, 0, uintptr(d.FDs[i]), uintptr(i), uintptr(d.Offsets[i]), uintptr(d.Pitches[i]),
			uintptr(d.Modifier>>32), uintptr(uint32(d.Modifier)))
	}
	params.Marshal(wl.ParamsCreate, nil, 0, wl.Int(d.Width), wl.Int(d.Height), uintptr(d.Fourcc), 0)
	w.q.Flush()
	var r result
	select {
	case r = <-done:
	case <-time.After(time.Second):
	}
	params.Destroy(0)
	if !r.ok {
		return 0, fmt.Errorf("%w: the compositor refused a %s DMA-BUF", ErrFormat, fourccString(d.Fourcc))
	}
	w.listenRelease(r.buf, nil)
	return r.buf, nil
}

// importMemfd makes a wl_shm buffer of d's memfd, which holds its planes
// as wl_shm lays them: the chroma right after the luma, at its pitch.
func (w *waySurface) importMemfd(d *DMABuf) (wl.Proxy, error) {
	rows := d.Height
	if d.Planes > 1 {
		if d.Pitches[1] != d.Pitches[0] || d.Offsets[1] != d.Offsets[0]+d.Pitches[0]*uint32(d.Height) {
			return 0, ErrFormat
		}
		rows += (d.Height + 1) / 2
	}
	size := int(d.Offsets[0]) + int(d.Pitches[0])*rows
	pool := w.shm.Marshal(wl.ShmCreatePool, wl.Core("wl_shm_pool"), 1, 0, uintptr(d.FDs[0]), wl.Int(size))
	buf := pool.Marshal(wl.ShmPoolCreateBuf, wl.Core("wl_buffer"), 1, 0, uintptr(d.Offsets[0]), wl.Int(d.Width), wl.Int(d.Height), uintptr(d.Pitches[0]), uintptr(shmCode(d.Fourcc)))
	pool.Destroy(wl.ShmPoolDestroy)
	if buf == 0 {
		return 0, ErrFormat
	}
	w.listenRelease(buf, nil)
	return buf, nil
}

func (w *waySurface) listenRelease(buf wl.Proxy, busy *atomic.Bool) {
	buf.Listen(func(op uint32, e wl.Event) {
		if op == 0 && busy != nil {
			busy.Store(false)
		}
	})
}

// copyFrame copies d's planes into a free buffer of the surface's own: as
// they are where the compositor takes the format in wl_shm, else
// converted to XRGB8888.
func (w *waySurface) copyFrame(d *DMABuf) (wl.Proxy, error) {
	format, stride := uint32(shmXRGB8888), uint32(d.Width*4)
	if (d.Fourcc == FourccNV12 || d.Fourcc == FourccP010) && w.takesShm(d.Fourcc) {
		format = d.Fourcc
		stride = uint32(d.Width)
		if d.Fourcc == FourccP010 {
			stride *= 2
		}
		stride = (stride + 3) &^ 3
	} else if d.Fourcc != FourccNV12 && d.Fourcc != FourccP010 && d.Fourcc != FourccXRGB8888 && d.Fourcc != FourccARGB8888 {
		return 0, fmt.Errorf("%w: %s", ErrFormat, fourccString(d.Fourcc))
	}
	b := w.freeShm(d.Width, d.Height, format, stride)
	if b == nil {
		return 0, errors.New("gpusurface: every buffer is still on screen")
	}
	h := d.Height
	switch {
	case format == FourccNV12 || format == FourccP010:
		rowBytes := int(stride)
		lp, cp := int(d.Pitches[0]), int(d.Pitches[1])
		luma, chroma := d.plane(0, h), d.plane(1, (h+1)/2)
		n := d.Width
		if format == FourccP010 {
			n *= 2
		}
		for r := range h {
			copy(b.mem[r*rowBytes:r*rowBytes+n], luma[r*lp:])
		}
		off := rowBytes * h
		for r := range (h + 1) / 2 {
			copy(b.mem[off+r*rowBytes:off+r*rowBytes+n], chroma[r*cp:])
		}
	case d.Fourcc == FourccXRGB8888 || d.Fourcc == FourccARGB8888:
		src := d.plane(0, h)
		for r := range h {
			copy(b.mem[r*int(stride):r*int(stride)+4*d.Width], src[r*int(d.Pitches[0]):])
		}
	default:
		yuvToXRGB(b.mem, int(stride), d)
	}
	b.busy.Store(true)
	return b.buf, nil
}

// freeShm is a buffer of the size and format the compositor released,
// made if fewer than four are.
func (w *waySurface) freeShm(width, height int, format, stride uint32) *shmBuffer {
	kept := w.shmBufs[:0]
	var free *shmBuffer
	for _, b := range w.shmBufs {
		if b.width != width || b.height != height || b.format != format {
			if !b.busy.Load() {
				b.destroy()
				continue
			}
		} else if free == nil && !b.busy.Load() {
			free = b
		}
		kept = append(kept, b)
	}
	w.shmBufs = kept
	if free != nil || len(w.shmBufs) >= 4 {
		return free
	}
	b, err := w.newShmBuffer(width, height, format, stride)
	if err != nil {
		return nil
	}
	w.shmBufs = append(w.shmBufs, b)
	return b
}

func (w *waySurface) newShmBuffer(width, height int, format, stride uint32) (*shmBuffer, error) {
	rows := height
	if format == FourccNV12 || format == FourccP010 {
		rows += (height + 1) / 2
	}
	size := int(stride) * rows
	fd, err := memfd("planck-video")
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err := syscall.Ftruncate(fd, int64(size)); err != nil {
		return nil, err
	}
	mem, err := syscall.Mmap(fd, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	pool := w.shm.Marshal(wl.ShmCreatePool, wl.Core("wl_shm_pool"), 1, 0, uintptr(fd), wl.Int(size))
	buf := pool.Marshal(wl.ShmPoolCreateBuf, wl.Core("wl_buffer"), 1, 0, 0, wl.Int(width), wl.Int(height), uintptr(stride), uintptr(format))
	pool.Destroy(wl.ShmPoolDestroy)
	if buf == 0 {
		syscall.Munmap(mem)
		return nil, errors.New("gpusurface: wl_shm_pool.create_buffer failed")
	}
	b := &shmBuffer{buf: buf, mem: mem, width: width, height: height, stride: stride, format: format}
	w.listenRelease(buf, &b.busy)
	return b, nil
}

func (b *shmBuffer) destroy() {
	b.buf.Destroy(0)
	syscall.Munmap(b.mem)
}

func (w *waySurface) close() {
	for _, p := range []wl.Proxy{w.videoView, w.videoSub, w.video, w.backView, w.backSub, w.back} {
		p.Destroy(0)
	}
	for _, b := range w.imports {
		b.buf.Destroy(0)
	}
	for _, b := range w.shmBufs {
		b.destroy()
	}
	if w.black != nil {
		w.black.destroy()
	}
	w.destroyGlobals()
	w.q.Close()
}

func (w *waySurface) destroyGlobals() {
	w.viewporter.Destroy(0)
	w.subcompositor.Destroy(0)
	w.dmabuf.Destroy(0)
	w.presentation.Destroy(0)
	// wl_compositor and wl_shm (before version 2) have no destructor.
	w.compositor.Free()
	w.shm.Free()
}

func memfd(name string) (int, error) {
	b := append([]byte(name), 0)
	fd, _, errno := syscall.Syscall(sysMemfdCreate, uintptr(unsafe.Pointer(&b[0])), 1 /* MFD_CLOEXEC */, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func fourccString(f uint32) string {
	return string([]byte{byte(f), byte(f >> 8), byte(f >> 16), byte(f >> 24)})
}
