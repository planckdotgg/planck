//go:build windows

package player

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/egoist/mygo/gpusurface"

	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/decode"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
	"github.com/watzon/planck/pkg/encode/nvenc"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

const (
	wsPopup        = 0x80000000
	wsVisible      = 0x10000000
	wsExToolWindow = 0x80
	wsExNoActivate = 0x08000000
	swpNoMove      = 0x2
	swpNoZOrder    = 0x4
	swpNoActivate  = 0x10
	smCXScreen     = 0
	smCYScreen     = 1
	pmRemove       = 1
)

// window opens a small window in the bottom-left corner of the primary
// display that never takes focus. The display layout is not touched.
func window(t *testing.T, w, h int) uintptr {
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	title, _ := syscall.UTF16PtrFromString("planck player test")
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	hwnd, _, err := procCreateWindowExW.Call(wsExToolWindow|wsExNoActivate, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsPopup|wsVisible, 40, sh-uintptr(h)-80, uintptr(w), uintptr(h), 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW: %v", err)
	}
	t.Cleanup(func() { procDestroyWindow.Call(hwnd) })
	return hwnd
}

func pump() {
	var msg [48]byte
	for {
		if r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, pmRemove); r == 0 {
			return
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}

func stream(t *testing.T, chroma encode.Chroma, src capture.Format, n int) [][]byte {
	nv := encodetest.Device(t, "NVIDIA")
	cfg := encode.Config{Codec: encode.HEVC, Chroma: chroma, Source: src, Width: 1920, Height: 1080, FPS: 120, BitrateKbps: 60000}
	enc, err := nvenc.Open(nv.D3D, nv.Info, cfg, nvenc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	frames := encodetest.Frames(t, nv, src, 1920, 1080, 8)
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		p, err := enc.Encode(frames[i%len(frames)], i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Data)
	}
	return out
}

func TestPlayer(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 8-bit, then the host switches to 10-bit with a new sequence.
	packets := append(stream(t, encode.NV12, capture.FormatNV12, 120), stream(t, encode.P010, capture.FormatP010, 120)...)

	hwnd := window(t, 640, 360)
	surf, err := gpusurface.New(gpusurface.Options{Window: hwnd})
	if err != nil {
		t.Fatal(err)
	}
	defer surf.Close()
	p, err := Open(surf, decode.Config{Codec: encode.HEVC, Width: 1920, Height: 1080, FPS: 120}, 512)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	formats := map[capture.Format]int{}
	submit := func(from, to int) {
		for i := from; i < to; i++ {
			_, err := p.Submit(packets[i], int64(i))
			if errors.Is(err, decode.ErrNoFrame) {
				continue
			}
			if err != nil {
				t.Fatalf("access unit %d: %v", i, err)
			}
			f, w, h := p.Format()
			formats[f]++
			if w != 1920 || h != 1080 {
				t.Fatalf("access unit %d shown at %dx%d", i, w, h)
			}
			pump()
		}
	}
	submit(0, 180)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 960, 540, swpNoMove|swpNoZOrder|swpNoActivate)
	pump()
	if err := p.Resize(0, 0); err != nil {
		t.Fatal(err)
	}
	if w, h := surf.Size(); w != 960 || h != 540 {
		t.Fatalf("surface %dx%d after resize, want 960x540", w, h)
	}
	submit(180, len(packets))

	st := p.Stats()
	dec, pres := bench.NewSamples(len(packets)), bench.NewSamples(len(packets))
	for _, tm := range p.Timings()[10:] {
		dec.Add(float64(tm.Decode) / 1e6)
		pres.Add(float64(tm.Present) / 1e6)
	}
	t.Logf("on %s: %+v; NV12 %d, P010 %d pictures; decode %s; present %s", surf.AdapterName(), st, formats[capture.FormatNV12], formats[capture.FormatP010],
		dec.Summarize(), pres.Summarize())
	if st.Pictures+st.NoPicture != len(packets) || st.NoPicture > len(packets)/100 {
		t.Fatalf("%d pictures, %d access units without one, of %d", st.Pictures, st.NoPicture, len(packets))
	}
	if st.FormatChanges != 2 || formats[capture.FormatP010] < 110 {
		t.Fatalf("format changes %d, P010 pictures %d: the 8-bit to 10-bit switch was not tracked", st.FormatChanges, formats[capture.FormatP010])
	}
}
