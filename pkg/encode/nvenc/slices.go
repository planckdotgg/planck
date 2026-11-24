package nvenc

import (
	"bytes"

	"github.com/planckdotgg/planck/pkg/encode"
)

// appendSliceEnds extends ends, the byte ends of the slices already found
// in data, to the first n slices of a picture whose finished slices fill
// data. It reports false when a boundary cannot be placed yet.
//
// Before the picture is done, NVENC updates the slice count and size of a
// non-blocking lock before the slice offsets, which can still hold the
// previous picture's values. So a slice that is the only new one ends at
// len(data), several new HEVC or H.264 slices are split at their slice NAL
// start codes, and the offsets are trusted only once the picture is done.
func appendSliceEnds(ends []int, data []byte, n int, codec encode.Codec, offsets []uint32, done bool) ([]int, bool) {
	start := 0
	if len(ends) > 0 {
		start = ends[len(ends)-1]
	}
	base := len(ends)
	for i := len(ends); i < n-1; i++ {
		end, ok := nextSlice(data, start, codec)
		if !ok && done && i+1 < len(offsets) {
			end, ok = int(offsets[i+1]), true
		}
		if !ok || end <= start || end >= len(data) {
			return ends[:base], false
		}
		ends = append(ends, end)
		start = end
	}
	if n > len(ends) {
		ends = append(ends, len(data))
	}
	return ends, true
}

// nextSlice returns where the slice NAL after the first one at or past
// from begins: the end of the slice that from starts, with any parameter
// sets before its slice NAL.
func nextSlice(data []byte, from int, codec encode.Codec) (int, bool) {
	if codec != encode.HEVC && codec != encode.H264 {
		return 0, false
	}
	seen := false
	for i := from; i < len(data); {
		j := bytes.Index(data[i:], []byte{0, 0, 1})
		if j < 0 || i+j+3 >= len(data) {
			return 0, false
		}
		at, hdr := i+j, data[i+j+3]
		if isSliceNAL(hdr, codec) {
			if seen {
				if at > from && data[at-1] == 0 {
					at--
				}
				return at, true
			}
			seen = true
		}
		i = at + 3
	}
	return 0, false
}

func isSliceNAL(hdr byte, codec encode.Codec) bool {
	if codec == encode.HEVC {
		return (hdr>>1)&0x3f < 32
	}
	t := hdr & 0x1f
	return t == 1 || t == 5
}
