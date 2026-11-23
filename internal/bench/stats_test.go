package bench

import "testing"

func TestSummarizeNearestRank(t *testing.T) {
	s := NewSamples(100)
	for i := 100; i >= 1; i-- {
		s.Add(float64(i))
	}
	got := s.Summarize()
	want := Summary{N: 100, Mean: 50.5, P50: 50, P95: 95, P99: 99, Max: 100}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSummarizeSmall(t *testing.T) {
	s := NewSamples(1)
	s.Add(3)
	if got := s.Summarize(); got.P50 != 3 || got.P99 != 3 || got.N != 1 {
		t.Fatalf("got %+v", got)
	}
	if got := NewSamples(0).Summarize(); got.N != 0 {
		t.Fatalf("empty: got %+v", got)
	}
}
