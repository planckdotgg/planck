package main

import (
	"encoding/binary"
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/input"
)

// TestRawSideButtons: a RAWMOUSE's button flags give the side buttons'
// presses and releases, several in one report, and nothing of the others.
func TestRawSideButtons(t *testing.T) {
	m := make([]byte, 24)
	var got []string
	to := func(b input.Button, down bool) {
		got = append(got, buttonLog([]input.Event{{Type: input.TypeMouseButton, Button: input.MouseButton{Button: b, Pressed: down}}})...)
	}
	for _, flags := range []uint16{
		riMouseButton4Down,
		0x0001 | 0x0004, // left and right down
		riMouseButton5Down | 0x0002,
		riMouseButton4Up | riMouseButton5Up,
	} {
		binary.LittleEndian.PutUint16(m[4:], flags)
		rawSideButtons(m, to)
	}
	if want := []string{"X1+", "X2+", "X1-", "X2-"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
