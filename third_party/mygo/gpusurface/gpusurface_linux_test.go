//go:build linux && !android && (amd64 || arm64)

package gpusurface

import (
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// nv12 is a w x h NV12 frame in memory of one colour.
func nv12(w, h int, y, u, v byte) (*DMABuf, []byte) {
	pitch := (w + 63) &^ 63
	pix := make([]byte, pitch*h+pitch*((h+1)/2))
	for i := range pitch * h {
		pix[i] = y
	}
	for i := pitch * h; i+1 < len(pix); i += 2 {
		pix[i], pix[i+1] = u, v
	}
	d := &DMABuf{Fourcc: FourccNV12, Width: w, Height: h, Planes: 2, Serial: 1}
	d.Pitches = [4]uint32{uint32(pitch), uint32(pitch)}
	d.CPU[0] = unsafe.Pointer(&pix[0])
	d.CPU[1] = unsafe.Pointer(&pix[pitch*h])
	return d, pix
}

func TestBT709(t *testing.T) {
	for _, c := range []struct {
		y, u, v byte
		want    uint32
	}{
		{16, 128, 128, 0xff000000},
		{235, 128, 128, 0xffffffff},
		{63, 102, 240, 0xffff0000}, // BT.709 video-range red
	} {
		got := bt709(int(c.y), int(c.u), int(c.v))
		if d := diff(got, c.want); d > 2 {
			t.Errorf("Y'CbCr %d,%d,%d: %#08x, want %#08x", c.y, c.u, c.v, got, c.want)
		}
	}
}

func diff(a, b uint32) int {
	m := 0
	for s := 0; s < 24; s += 8 {
		d := int(a>>s&0xff) - int(b>>s&0xff)
		m = max(m, d, -d)
	}
	return m
}

func TestYUVToXRGBPadded(t *testing.T) {
	d, pix := nv12(5, 3, 235, 128, 128)
	stride := 5*4 + 12
	dst := make([]byte, stride*3)
	yuvToXRGB(dst, stride, d)
	runtime.KeepAlive(pix)
	for r := range 3 {
		for x := range 5 {
			if got := uint32(dst[r*stride+4*x]) | uint32(dst[r*stride+4*x+1])<<8 | uint32(dst[r*stride+4*x+2])<<16 | uint32(dst[r*stride+4*x+3])<<24; got != 0xffffffff {
				t.Fatalf("pixel %d,%d %#08x", x, r, got)
			}
		}
		for _, b := range dst[r*stride+20 : (r+1)*stride] {
			if b != 0 {
				t.Fatalf("row %d: the padding was written", r)
			}
		}
	}
}

func TestPictureModeCopiesP010HighBits(t *testing.T) {
	w, h := 4, 2
	pitch := w * 2
	pix := make([]byte, pitch*h+pitch)
	for i := 0; i < len(pix); i += 2 {
		pix[i], pix[i+1] = 0xc0, byte(i) // 10 bits high: the high byte is the 8-bit sample
	}
	d := &DMABuf{Fourcc: FourccP010, Width: w, Height: h, Planes: 2, Pitches: [4]uint32{uint32(pitch), uint32(pitch)}}
	d.CPU[0], d.CPU[1] = unsafe.Pointer(&pix[0]), unsafe.Pointer(&pix[pitch*h])
	p := newPictureMode(nil)
	if err := p.present(d); err != nil {
		t.Fatal(err)
	}
	b := p.bufs[p.middle.Load()&^overlayFresh]
	for i, got := range b.pix {
		if want := byte(2 * i); got != want {
			t.Fatalf("sample %d: %d, want %d", i, got, want)
		}
	}
	if !p.refresh() || p.refresh() {
		t.Fatal("refresh should show the frame once")
	}
}

// gtkWindow opens a GTK 3 window of w x h on whatever display GDK picks,
// shown and realized, on the calling (locked) thread; iterate runs GTK's
// main loop a while.
func gtkWindow(t *testing.T, w, h int) (win uintptr, iterate func(time.Duration)) {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		t.Skip("no display")
	}
	g, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		t.Skip("no GTK 3:", err)
	}
	var (
		initCheck     func(argc, argv uintptr) bool
		windowNew     func(kind int32) uintptr
		setDefault    func(win uintptr, w, h int32)
		showAll       func(win uintptr)
		iterationDo   func(block bool) bool
		widgetDestroy func(win uintptr)
	)
	purego.RegisterLibFunc(&initCheck, g, "gtk_init_check")
	purego.RegisterLibFunc(&windowNew, g, "gtk_window_new")
	purego.RegisterLibFunc(&setDefault, g, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&showAll, g, "gtk_widget_show_all")
	purego.RegisterLibFunc(&iterationDo, g, "gtk_main_iteration_do")
	purego.RegisterLibFunc(&widgetDestroy, g, "gtk_widget_destroy")
	if !initCheck(0, 0) {
		t.Skip("GTK found no display")
	}
	win = windowNew(0)
	setDefault(win, int32(w), int32(h))
	showAll(win)
	iterate = func(d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); {
			if !iterationDo(false) {
				time.Sleep(time.Millisecond)
			}
		}
	}
	iterate(300 * time.Millisecond)
	t.Cleanup(func() { widgetDestroy(win); iterate(50 * time.Millisecond) })
	return win, iterate
}

// TestPresentLive shows NV12 frames in memory in a GTK window: in a
// subsurface on Wayland, where each must reach the screen (wp_presentation)
// or be replaced, and in picture mode on X11.
func TestPresentLive(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	win, iterate := gtkWindow(t, 640, 360)
	s, err := New(Options{Window: win, Timings: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wayland subsurface: %v; content %dx%d at scale %v", s.Wayland(), s.native.AreaW, s.native.AreaH, s.Scale())
	if s.Wayland() {
		t.Logf("shm formats %v, dmabuf formats %d, presentation clock %d", s.way.shmFormats, len(s.way.modifiers), s.way.clock.Load())
	}
	presented := make(chan int64, 64)
	s.OnPresented(func(tag, submitted, at int64) { presented <- at })
	frame, pix := nv12(1280, 720, 63, 102, 240)
	for i := range 30 {
		frame.Serial = uint64(i % 3)
		if err := s.PresentTagged(frame.handle(), int64(i)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		iterate(16 * time.Millisecond)
	}
	iterate(200 * time.Millisecond)
	runtime.KeepAlive(pix)
	s.Close()
	got, onScreen := len(presented), 0
	for range got {
		if <-presented != 0 {
			onScreen++
		}
	}
	tms := s.Timings()
	if len(tms) != 30 {
		t.Fatalf("%d timings", len(tms))
	}
	if s.way != nil && s.way.presentation != 0 {
		if onScreen == 0 {
			t.Fatalf("none of %d presentation reports had a frame on screen", got)
		}
		last := tms[0]
		for _, tm := range tms {
			if tm.Presented != 0 {
				last = tm
			}
		}
		glass := float64(last.Presented-last.Submitted) / 1e6
		t.Logf("%d of 30 frames reported, %d on screen; last present to screen %.2f ms", got, onScreen, glass)
		if glass < 0 || glass > 1000 {
			t.Errorf("present to screen %.2f ms: the clocks disagree", glass)
		}
	}
}

func (d *DMABuf) handle() uintptr { return uintptr(unsafe.Pointer(d)) }
