package transport

import (
	"testing"
	"time"
)

func TestMonoResolutionAndAllocs(t *testing.T) {
	var smallest int64 = 1 << 62
	prev := Mono()
	for i := 0; i < 100000; i++ {
		n := Mono()
		if n < prev {
			t.Fatalf("Mono went backwards: %d -> %d", prev, n)
		}
		if d := n - prev; d > 0 && d < smallest {
			smallest = d
		}
		prev = n
	}
	if smallest > int64(10*time.Microsecond) {
		t.Fatalf("Mono resolution %v, want under 10µs", time.Duration(smallest))
	}
	if a := testing.AllocsPerRun(1000, func() { Mono() }); a != 0 {
		t.Fatalf("Mono allocates %.1f", a)
	}
}

// TestPacerRate is a smoke test of the pacer on the real clock; the
// pacer's deadlines are checked exactly in TestPacerDeadlines. It measures
// the rate between scheduler stalls: a pacer that
// woke late starts again from now, so a stall on a busy runner costs wall
// time the pacer never had. Without a burst it books one 28 µs packet
// ahead, so a gap of ten is the OS, not the pacer.
func TestPacerRate(t *testing.T) {
	const pkt, n, rate = 1400, 2000, 400e6 / 8
	stall := 10 * int64(pkt*1e9/rate)
	p := NewPacer(rate)
	start := Mono()
	prev, stalls, stalled := start, 0, int64(0)
	for i := 0; i < n; i++ {
		p.Wait(pkt)
		now := Mono()
		if g := now - prev; i > 0 && g > stall {
			stalls, stalled = stalls+1, stalled+g
		}
		prev = now
	}
	if stalls > n/10 {
		t.Fatalf("%d of %d packets waited over %v", stalls, n, time.Duration(stall))
	}
	got := float64(pkt*(n-1-stalls)*8) / (float64(prev-start-stalled) / 1e9) / 1e6
	if got < 360 || got > 440 {
		t.Fatalf("paced at %.0f Mbps, want 400±10%% (%d stalls, %v)", got, stalls, time.Duration(stalled))
	}
}
