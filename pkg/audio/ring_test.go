package audio

import (
	"encoding/binary"
	"testing"
)

func readSamples(q *pcmRing, n int) []int16 {
	b := make([]byte, 2*n)
	got := q.read(b)
	out := make([]int16, got/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

func equal(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRingPassesSamplesThroughAcrossWrap(t *testing.T) {
	q := newPCMRing(8)
	q.write([]int16{1, 2, 3, 4, 5, 6})
	if got := readSamples(q, 4); !equal(got, []int16{1, 2, 3, 4}) {
		t.Fatalf("got %v", got)
	}
	q.write([]int16{-7, 8, -32768, 32767})
	if got := readSamples(q, 6); !equal(got, []int16{5, 6, -7, 8, -32768, 32767}) {
		t.Fatalf("got %v", got)
	}
	if q.dropped != 0 {
		t.Fatalf("dropped %d, want 0", q.dropped)
	}
}

func TestRingShortReadsWithoutPadding(t *testing.T) {
	q := newPCMRing(8)
	if got := readSamples(q, 4); len(got) != 0 {
		t.Fatalf("empty ring: got %v, want nothing", got)
	}
	q.write([]int16{9, 9})
	if got := readSamples(q, 4); !equal(got, []int16{9, 9}) {
		t.Fatalf("got %v, want the two queued samples only", got)
	}
}

func TestRingDropsOldestPastMax(t *testing.T) {
	q := newPCMRing(4)
	q.write([]int16{1, 2, 3})
	q.write([]int16{4, 5, 6})
	if got := readSamples(q, 4); !equal(got, []int16{3, 4, 5, 6}) {
		t.Fatalf("got %v", got)
	}
	q.write([]int16{1, 2, 3, 4, 5, 6})
	if got := readSamples(q, 4); !equal(got, []int16{3, 4, 5, 6}) {
		t.Fatalf("got %v", got)
	}
	if q.dropped != 4 {
		t.Fatalf("dropped %d, want 4", q.dropped)
	}
}
