//go:build windows

package dxgi

import (
	"encoding/binary"
	"testing"
)

// TestParseDevMode: a display's place, size and rate come from the
// DEVMODEW fields GDI fills, a display left of the primary at negative
// coordinates included.
func TestParseDevMode(t *testing.T) {
	var dm [220]byte
	put := func(off int, v int32) { binary.LittleEndian.PutUint32(dm[off:], uint32(v)) }
	put(76, -3840)
	put(80, -720)
	put(172, 3840)
	put(176, 2160)
	put(184, 120)
	m, ok := parseDevMode(&dm)
	if !ok || m != (displayMode{x: -3840, y: -720, w: 3840, h: 2160, hz: 120}) {
		t.Fatalf("got %+v %v", m, ok)
	}
	if _, ok := parseDevMode(&[220]byte{}); ok {
		t.Fatal("an empty mode parsed")
	}
}
