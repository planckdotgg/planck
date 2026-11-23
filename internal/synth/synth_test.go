package synth

import "testing"

func TestYUVReferenceColors(t *testing.T) {
	cases := []struct {
		r, g, b uint8
		y, u, v int
	}{
		{0, 0, 0, 16, 128, 128},
		{255, 255, 255, 234, 128, 128},
		{255, 0, 0, 62, 102, 239},
	}
	for _, c := range cases {
		y, u, v := yuv(c.r, c.g, c.b)
		if abs(y-c.y) > 1 || abs(u-c.u) > 1 || abs(v-c.v) > 1 {
			t.Errorf("yuv(%d,%d,%d) = %d,%d,%d want %d,%d,%d", c.r, c.g, c.b, y, u, v, c.y, c.u, c.v)
		}
	}
}

func TestFramesDiffer(t *testing.T) {
	const w, h = 64, 32
	a, b := make([]byte, w*h*4), make([]byte, w*h*4)
	BGRA(a, w, h, 0)
	BGRA(b, w, h, 1)
	if string(a) == string(b) {
		t.Fatal("consecutive frames are identical; encoders would see no motion")
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestPlanarFillsExactBuffers(t *testing.T) {
	const w, h = 64, 32
	nv12 := make([]byte, w*h*3/2)
	NV12(nv12, w, h, 0)
	p010 := make([]byte, w*h*3)
	P010(p010, w, h, 0)
	if nv12[len(nv12)-1] == 0 || p010[len(p010)-1] == 0 {
		t.Fatal("last chroma sample was never written")
	}
}
