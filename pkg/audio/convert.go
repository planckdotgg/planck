package audio

func floatToInt16(v float32) int16 {
	switch {
	case v >= 1:
		return 32767
	case v <= -1:
		return -32768
	case v != v:
		return 0
	}
	return int16(v * 32767)
}

// interleaveFloat writes left and right float32 planes to interleaved stereo
// int16 in dst, which must hold 2*len(left) values.
func interleaveFloat(dst []int16, left, right []float32) {
	for i := range left {
		dst[2*i] = floatToInt16(left[i])
		dst[2*i+1] = floatToInt16(right[i])
	}
}

// stereoFromInterleavedFloat keeps the first two of channels interleaved
// float32 channels (or duplicates a mono one) as stereo int16.
func stereoFromInterleavedFloat(dst []int16, src []float32, channels int) {
	frames := len(src) / channels
	for i := 0; i < frames; i++ {
		l := src[i*channels]
		r := l
		if channels > 1 {
			r = src[i*channels+1]
		}
		dst[2*i] = floatToInt16(l)
		dst[2*i+1] = floatToInt16(r)
	}
}
