//go:build windows

package cursor

import (
	"testing"
	"unsafe"
)

func TestCursorInfoMatchesWin32Layout(t *testing.T) {
	var ci cursorInfo
	if s := unsafe.Sizeof(ci); s != 24 {
		t.Errorf("sizeof(CURSORINFO) = %d, want 24", s)
	}
	if o := unsafe.Offsetof(ci.pos); o != 16 {
		t.Errorf("offsetof(CURSORINFO.ptScreenPos) = %d, want 16", o)
	}
}

func TestShowing(t *testing.T) {
	for _, c := range []struct {
		flags uint32
		h     uintptr
		want  bool
	}{
		{cursorShowing, 0x10003, true},
		{0, 0x10003, false},
		{cursorShowing, 0, false},
		{cursorShowing | cursorSuppressed, 0x10003, false},
	} {
		if got := showing(c.flags, c.h); got != c.want {
			t.Errorf("showing(%#x, %#x) = %v, want %v", c.flags, c.h, got, c.want)
		}
	}
}

// TestRead reads the real cursor, which it leaves where it is.
func TestRead(t *testing.T) {
	if _, err := Read(); err != nil {
		t.Fatal(err)
	}
}
