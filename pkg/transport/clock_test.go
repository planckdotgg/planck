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

func TestPacerRate(t *testing.T) {
	const pkt, n = 1400, 2000
	p := NewPacer(400e6 / 8)
	start := Mono()
	for i := 0; i < n; i++ {
		p.Wait(pkt)
	}
	got := float64(pkt*(n-1)*8) / (float64(Mono()-start) / 1e9) / 1e6
	if got < 360 || got > 440 {
		t.Fatalf("paced at %.0f Mbps, want 400±10%%", got)
	}
}
