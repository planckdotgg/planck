package input

import (
	"testing"
	"time"
)

func TestScaleAbs(t *testing.T) {
	cases := []struct {
		v            uint16
		origin, size float64
		want         float64
	}{
		{0, 0, 1920, 0},
		{65535, 0, 1920, 1919},
		{32768, 0, 1921, 960.0146},
		{65535, -1440, 1440, -1},
		{100, 50, 1, 50},
	}
	for _, c := range cases {
		if got := scaleAbs(c.v, c.origin, c.size); got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("scaleAbs(%d, %v, %v) = %v, want %v", c.v, c.origin, c.size, got, c.want)
		}
	}
	if clamp(-5, 0, 10) != 0 || clamp(15, 0, 10) != 10 || clamp(5, 0, 10) != 5 {
		t.Error("clamp")
	}
}

func TestWheelAccumCarriesRemainder(t *testing.T) {
	var w wheelAccum
	steps := []struct {
		in   int16
		want int32
	}{{WheelNotch, 1}, {-2 * WheelNotch, -2}, {60, 0}, {60, 1}, {-30, 0}, {-90, -1}, {32767, 273}}
	for i, s := range steps {
		if got := w.lines(s.in); got != s.want {
			t.Fatalf("step %d: lines(%d) = %d, want %d", i, s.in, got, s.want)
		}
	}
}

func TestClickTrackerCountsMultiClicks(t *testing.T) {
	var c clickTracker
	t0 := time.Unix(100, 0)
	steps := []struct {
		b    Button
		dt   time.Duration
		x, y float64
		want int64
	}{
		{ButtonLeft, 0, 10, 10, 1},
		{ButtonLeft, 200 * time.Millisecond, 12, 9, 2},
		{ButtonLeft, 400 * time.Millisecond, 12, 9, 3},
		{ButtonLeft, 600 * time.Millisecond, 12, 9, 1},
		{ButtonRight, 100 * time.Millisecond, 12, 9, 1},
		{ButtonRight, 100 * time.Millisecond, 30, 9, 1},
	}
	now := t0
	for i, s := range steps {
		now = now.Add(s.dt)
		if got := c.press(s.b, now, s.x, s.y); got != s.want {
			t.Fatalf("press %d = %d, want %d", i, got, s.want)
		}
	}
}
