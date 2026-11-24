//go:build darwin || windows

package main

import (
	"testing"
	"time"
)

// TestDirectModeFollowsOverlay: direct mode needs full screen, a profile
// that allows it and nothing of the overlay drawn, and the UI's overlay
// callback moves it.
func TestDirectModeFollowsOverlay(t *testing.T) {
	v := &videoView{logf: t.Logf}
	v.mode.allowed.Store(true)
	want := func(step string, fullScreen, direct bool) {
		t.Helper()
		if got := directWanted(v.mode.allowed.Load(), fullScreen, v.mode.overlay.Load()); got != direct {
			t.Errorf("%s: direct %v, want %v", step, got, direct)
		}
	}
	want("overlay hidden, windowed", false, false)
	want("overlay hidden, full screen", true, true)
	v.overlayChanged(true)
	want("overlay shown", true, false)
	v.overlayChanged(false)
	want("overlay hidden again", true, true)
	want("overlay hidden, windowed", false, false)
	v.mode.allowed.Store(false)
	want("profile forbids it", true, false)
	if v.mode.gen != 2 {
		t.Fatalf("two changes scheduled %d swaps", v.mode.gen)
	}
}

// TestDirectDelay: the overlay coming back swaps at once, going direct
// waits 250 ms, and no swap comes within 250 ms of the last.
func TestDirectDelay(t *testing.T) {
	now := time.Now()
	long := now.Add(-time.Hour)
	for _, tc := range []struct {
		name     string
		toDirect bool
		last     time.Time
		want     time.Duration
	}{
		{"overlay back", false, long, 0},
		{"overlay back 100 ms after a swap", false, now.Add(-100 * time.Millisecond), 150 * time.Millisecond},
		{"going direct", true, long, directSettle},
		{"going direct right after a swap", true, now, directSettle},
	} {
		if got := directDelay(tc.toDirect, now, tc.last); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
