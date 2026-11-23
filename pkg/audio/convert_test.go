package audio

import (
	"math"
	"testing"
)

func TestFloatToInt16ClampsAndScales(t *testing.T) {
	cases := []struct {
		in   float32
		want int16
	}{
		{0, 0}, {1, 32767}, {-1, -32768}, {2.5, 32767}, {-7, -32768},
		{0.5, 16383}, {-0.5, -16383}, {float32(math.NaN()), 0},
		{float32(math.Inf(1)), 32767}, {float32(math.Inf(-1)), -32768},
	}
	for _, c := range cases {
		if got := floatToInt16(c.in); got != c.want {
			t.Errorf("floatToInt16(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestInterleaveFloatPlanes(t *testing.T) {
	dst := make([]int16, 4)
	interleaveFloat(dst, []float32{1, 0}, []float32{-1, 0.5})
	if want := []int16{32767, -32768, 0, 16383}; !equal(dst, want) {
		t.Fatalf("got %v, want %v", dst, want)
	}
}

func TestStereoFromInterleavedFloat(t *testing.T) {
	dst := make([]int16, 4)
	stereoFromInterleavedFloat(dst, []float32{1, -1, 0.5, 0, 0, 0}, 3)
	if want := []int16{32767, -32768, 0, 0}; !equal(dst, want) {
		t.Fatalf("3ch: got %v, want %v", dst, want)
	}
	stereoFromInterleavedFloat(dst, []float32{0.5, -1}, 1)
	if want := []int16{16383, 16383, -32768, -32768}; !equal(dst, want) {
		t.Fatalf("mono: got %v, want %v", dst, want)
	}
}
