package input

import (
	"math"
	"testing"
)

// TestVirtualDeskAbs: a point normalized across the display a stream
// shows lands, through MOUSEEVENTF_VIRTUALDESK's normalization across the
// whole desktop, on the pixel it names, within a pixel, whether Windows
// truncates the coordinate back to a pixel or rounds it.
func TestVirtualDeskAbs(t *testing.T) {
	primary := Rect{0, 0, 1920, 1080}
	for _, c := range []struct {
		name             string
		display, desktop Rect
	}{
		{"primary alone", primary, primary},
		{"primary, a virtual display to its right", primary, Rect{0, 0, 1920 + 5120, 1440}},
		{"virtual display right of the primary", Rect{1920, 0, 5120, 1440}, Rect{0, 0, 1920 + 5120, 1440}},
		{"virtual display left of and above the primary", Rect{-2560, -360, 2560, 1440}, Rect{-2560, -360, 2560 + 1920, 1440}},
		{"virtual display after a resize", Rect{1920, 0, 3456, 2234}, Rect{0, 0, 1920 + 3456, 2234}},
		{"virtual display below the primary", Rect{0, 1080, 3840, 2160}, Rect{0, 0, 3840, 1080 + 2160}},
	} {
		d, v := c.display, c.desktop
		for _, p := range [][2]uint16{{0, 0}, {65535, 0}, {0, 65535}, {65535, 65535}, {32768, 32768}, {12345, 54321}} {
			wantX := float64(d.X) + float64(p[0])*float64(d.W-1)/65535
			wantY := float64(d.Y) + float64(p[1])*float64(d.H-1)/65535
			nx := virtualDeskAbs(p[0], d.X, d.W, v.X, v.W)
			ny := virtualDeskAbs(p[1], d.Y, d.H, v.Y, v.H)
			if nx < 0 || nx > 65535 || ny < 0 || ny > 65535 {
				t.Fatalf("%s: %v gives %d, %d, outside 0..65535", c.name, p, nx, ny)
			}
			for _, back := range []struct {
				how string
				f   func(n int32, origin, size int) float64
			}{
				{"truncated", func(n int32, origin, size int) float64 {
					return float64(origin) + math.Floor(float64(n)*float64(size)/65536)
				}},
				{"rounded", func(n int32, origin, size int) float64 {
					return float64(origin) + math.Round(float64(n)*float64(size-1)/65535)
				}},
			} {
				x, y := back.f(nx, v.X, v.W), back.f(ny, v.Y, v.H)
				if math.Abs(x-wantX) > 1 || math.Abs(y-wantY) > 1 {
					t.Errorf("%s, %s: %v lands at %v, %v, want %.1f, %.1f", c.name, back.how, p, x, y, wantX, wantY)
				}
				if x < float64(d.X) || x > float64(d.X+d.W-1) || y < float64(d.Y) || y > float64(d.Y+d.H-1) {
					t.Errorf("%s, %s: %v lands at %v, %v, off the display %+v", c.name, back.how, p, x, y, d)
				}
			}
		}
	}
}
