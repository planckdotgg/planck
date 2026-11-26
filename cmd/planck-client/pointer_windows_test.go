package main

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
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

// TestRawReportMotionBeforeSideButton: a RAWMOUSE carrying motion and a
// side button's press passes the motion on first, so that the frame the
// press asks for sends it after the move.
func TestRawReportMotionBeforeSideButton(t *testing.T) {
	var log []string
	r := rawMouse{
		motion:  func(dx, dy float64) { log = append(log, "move") },
		buttons: func(b input.Button, down bool) { log = append(log, "side") },
	}
	m := make([]byte, 24)
	le := binary.LittleEndian
	le.PutUint16(m[4:], 0x0040) // RI_MOUSE_BUTTON_4_DOWN
	le.PutUint32(m[12:], 5)
	r.report(m, true)
	if want := []string{"move", "side"}; !slices.Equal(log, want) {
		t.Fatalf("passed on %v, want %v", log, want)
	}
	log = nil
	r.report(m, false)
	if want := []string{"side"}; !slices.Equal(log, want) {
		t.Fatalf("not relative: passed on %v, want %v", log, want)
	}
}
