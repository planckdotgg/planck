package main

import (
	"encoding/binary"
	"testing"
)

// TestRawMotion reads RAWMOUSE's relative motion, and leaves a tablet's
// absolute positions alone.
func TestRawMotion(t *testing.T) {
	m := make([]byte, 24)
	le := binary.LittleEndian
	dx := int32(-7)
	le.PutUint32(m[12:], uint32(dx))
	le.PutUint32(m[16:], 3)
	if dx, dy, ok := rawMotion(m); !ok || dx != -7 || dy != 3 {
		t.Fatalf("relative: %v, %v, %v", dx, dy, ok)
	}
	le.PutUint16(m[0:], mouseMoveAbs)
	if _, _, ok := rawMotion(m); ok {
		t.Fatal("an absolute position read as motion")
	}
	clear(m)
	if _, _, ok := rawMotion(m); ok {
		t.Fatal("no motion read as motion")
	}
}
