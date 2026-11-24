package nvenc

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

func nal(hdr byte, n int) []byte {
	b := append([]byte{0, 0, 0, 1, hdr, 1}, make([]byte, n)...)
	for i := 6; i < len(b); i++ {
		b[i] = byte(i%250) + 3
	}
	return b
}

// keyframe is an HEVC picture of four slices: VPS, SPS and PPS before an
// IDR slice, then three more IDR slices.
func keyframe() ([]byte, []int) {
	var b []byte
	for _, h := range []byte{32 << 1, 33 << 1, 34 << 1} {
		b = append(b, nal(h, 20)...)
	}
	var ends []int
	for _, n := range []int{300, 120, 410, 90} {
		b = append(b, nal(19<<1, n)...)
		ends = append(ends, len(b))
	}
	return b, ends
}

// TestSliceEndsIgnoreStaleOffsets replays what NVENC reported on bigboi:
// non-blocking locks whose slice count and size had moved on while the
// slice offsets still held the previous picture's values.
func TestSliceEndsIgnoreStaleOffsets(t *testing.T) {
	pic, want := keyframe()
	stale := []uint32{0, 7, 9, 11}
	var ends []int
	var ok bool
	for _, poll := range []struct{ n, size int }{{1, want[0]}, {3, want[2]}, {4, want[3]}} {
		ends, ok = appendSliceEnds(ends, pic[:poll.size], poll.n, encode.HEVC, stale, false)
		if !ok || !slices.Equal(ends, want[:poll.n]) {
			t.Fatalf("after %d slices: ends %v ok %v, want %v", poll.n, ends, ok, want[:poll.n])
		}
	}
}

func TestSliceEndsAV1WaitsForTheFinishedPicture(t *testing.T) {
	data := make([]byte, 1000)
	offsets := []uint32{0, 300, 700}
	ends, ok := appendSliceEnds(nil, data[:300], 1, encode.AV1, offsets, false)
	if !ok || !slices.Equal(ends, []int{300}) {
		t.Fatalf("one new slice: %v %v", ends, ok)
	}
	if got, ok := appendSliceEnds(ends, data, 3, encode.AV1, offsets, false); ok || !slices.Equal(got, []int{300}) {
		t.Fatalf("two new AV1 slices before the picture is done: %v %v, want no boundary yet", got, ok)
	}
	if got, ok := appendSliceEnds(ends, data, 3, encode.AV1, offsets, true); !ok || !slices.Equal(got, []int{300, 700, 1000}) {
		t.Fatalf("finished picture: %v %v", got, ok)
	}
}
