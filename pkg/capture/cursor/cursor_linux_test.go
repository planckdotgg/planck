//go:build linux && !android && (amd64 || arm64)

package cursor

import (
	"bytes"
	"testing"
)

func TestStraightRGBA(t *testing.T) {
	// Opaque red, half-transparent premultiplied white, clear.
	got := straightRGBA([]uint32{0xffff0000, 0x80808080, 0})
	want := []byte{255, 0, 0, 255, 255, 255, 255, 128, 0, 0, 0, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if anyAlpha([]uint32{0x00ffffff, 0}) || !anyAlpha([]uint32{0, 0x01000000}) {
		t.Fatal("anyAlpha looks at more than alpha")
	}
}
