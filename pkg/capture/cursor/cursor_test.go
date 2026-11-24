package cursor

import "testing"

func TestClassify(t *testing.T) {
	desk := Rect{-1920, 0, 7040, 1440}
	for _, c := range []struct {
		name    string
		showing bool
		clip    Rect
		want    State
	}{
		{"desktop, free", true, desk, State{Visible: true}},
		{"hidden, free: a page's cursor: none, a video player, typing", false, desk, State{}},
		{"shown, confined to a window: an RTS", true, Rect{0, 0, 1920, 1080}, State{Visible: true, Captured: true}},
		{"hidden and confined: mouselook", false, Rect{100, 100, 1, 1}, State{Captured: true}},
		{"hidden, no clip reported", false, Rect{}, State{}},
		{"confined on one edge only", true, Rect{-1920, 0, 7040, 1439}, State{Visible: true, Captured: true}},
		{"no clip reported", true, Rect{}, State{Visible: true}},
	} {
		if got := classify(c.showing, c.clip, desk); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := classify(true, Rect{0, 0, 100, 100}, Rect{}); got.Captured {
		t.Error("an unknown desktop counted as a confinement")
	}
}

func TestNormalize(t *testing.T) {
	d := Rect{1920, 0, 2560, 1440}
	for _, c := range []struct {
		x, y   int
		nx, ny uint16
		ok     bool
	}{
		{1920, 0, 0, 0, true},
		{1920 + 2559, 1439, 65535, 65535, true},
		{1920 + 1280, 720, 32780, 32790, true},
		{1919, 10, 0, 0, false},
		{1920 + 2560, 10, 0, 0, false},
		{2000, -1, 0, 0, false},
	} {
		nx, ny, ok := Normalize(c.x, c.y, d)
		if ok != c.ok || nx != c.nx || ny != c.ny {
			t.Errorf("Normalize(%d, %d) = %d, %d, %v; want %d, %d, %v", c.x, c.y, nx, ny, ok, c.nx, c.ny, c.ok)
		}
	}
	if _, _, ok := Normalize(0, 0, Rect{}); ok {
		t.Error("an unknown display took a point")
	}
}
