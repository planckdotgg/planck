//go:build darwin || windows

package main

import (
	"testing"

	"github.com/egoist/mygo"
)

// TestOverWindow: a stream window opens centred over the launcher, its
// corner kept on the launcher so that it opens on the launcher's display.
func TestOverWindow(t *testing.T) {
	for _, c := range []struct {
		r            mygo.Rectangle
		wantX, wantY int
	}{
		{mygo.Rectangle{X: 100, Y: 50, Width: 1600, Height: 1000}, 260, 150},
		{mygo.Rectangle{X: -1920, Y: 0, Width: 960, Height: 600}, -1920, 0},
	} {
		x, y := overWindow(c.r, streamWidth, streamHeight)
		if x != c.wantX || y != c.wantY {
			t.Errorf("over %+v: %d,%d, want %d,%d", c.r, x, y, c.wantX, c.wantY)
		}
	}
}
