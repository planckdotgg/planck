package ui

import (
	"testing"
	"time"
)

// TestHostingTooltipShowsOnce: the hosting button's status card is its
// only tooltip; the rail's plain name tooltip drew under it too.
func TestHostingTooltipShowsOnce(t *testing.T) {
	f := newFake()
	f.hosting = HostingState{Encoder: "Apple GPU VideoToolbox", Err: "Another app is using UDP port 41000."}
	a, tt := newTester(t, f)
	name := "Host this " + machineWord(a.goos)
	r, ok := tt.Find(name)
	if !ok {
		t.Fatalf("no %q button", name)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	snapshot(t, tt, "hosting-tooltip")
	n := 0
	for _, s := range tt.Texts() {
		if s == name {
			n++
		}
	}
	// The button's own label, and the card's title.
	if n != 2 {
		t.Fatalf("%q shows %d times, want 2 (button and one tooltip): %q", name, n, tt.Texts())
	}
	for _, s := range []string{"off", "Apple GPU VideoToolbox", "Another app is using UDP port 41000."} {
		if !tt.HasText(s) {
			t.Errorf("tooltip misses %q", s)
		}
	}
}
