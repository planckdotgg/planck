//go:build windows

package cursor

import (
	"syscall"
	"testing"
	"unsafe"
)

func TestShapeStructsMatchWin32Layout(t *testing.T) {
	if s := unsafe.Sizeof(iconInfo{}); s != 32 {
		t.Errorf("sizeof(ICONINFO) = %d, want 32", s)
	}
	if s := unsafe.Sizeof(bitmapObj{}); s != 32 {
		t.Errorf("sizeof(BITMAP) = %d, want 32", s)
	}
	if o := unsafe.Offsetof(bitmapInfo{}.colors); o != 40 {
		t.Errorf("sizeof(BITMAPINFOHEADER) = %d, want 40", o)
	}
}

var (
	procLoadCursorW     = user32.NewProc("LoadCursorW")
	procGetGuiResources = user32.NewProc("GetGuiResources")
	procGetCurrentProc  = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
)

func gdiObjects() uintptr {
	p, _, _ := procGetCurrentProc.Call()
	n, _, _ := procGetGuiResources.Call(p, 0) // GR_GDIOBJECTS
	return n
}

// TestReadSystemShapes reads the system's arrow and I-beam: each draws
// something, and reading them again and again leaks no GDI objects.
func TestReadSystemShapes(t *testing.T) {
	for _, c := range []struct {
		name string
		id   uintptr
	}{{"arrow", 32512}, {"I-beam", 32513}, {"hand", 32649}, {"size all", 32646}} {
		h, _, _ := procLoadCursorW.Call(0, c.id)
		s, err := ReadShape(State{Handle: h})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		opaque := 0
		for i := 3; i < len(s.RGBA); i += 4 {
			if s.RGBA[i] == 255 {
				opaque++
			}
		}
		if s.W == 0 || s.H == 0 || len(s.RGBA) != s.W*s.H*4 || opaque == 0 || s.HotX >= s.W || s.HotY >= s.H || s.DPI < 96 {
			t.Errorf("%s: %dx%d hot %d,%d dpi %d, %d opaque pixels", c.name, s.W, s.H, s.HotX, s.HotY, s.DPI, opaque)
		}
		t.Logf("%s: %dx%d hot %d,%d dpi %d, %d opaque pixels", c.name, s.W, s.H, s.HotX, s.HotY, s.DPI, opaque)
	}
	h, _, _ := procLoadCursorW.Call(0, 32513)
	ReadShape(State{Handle: h})
	before := gdiObjects()
	for range 2000 {
		if _, err := ReadShape(State{Handle: h}); err != nil {
			t.Fatal(err)
		}
	}
	if after := gdiObjects(); after > before {
		t.Errorf("GDI objects went from %d to %d over 2000 reads", before, after)
	}
	if _, err := ReadShape(State{}); err == nil {
		t.Error("read a shape of no cursor")
	}
}
