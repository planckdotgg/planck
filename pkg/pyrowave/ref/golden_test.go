package ref

import (
	"math"
	"os"
	"testing"
)

// goldens were encoded by upstream's MIT Metal encoder at a32b05e0 from
// synth frames converted to 8-bit planes (planes8); PSNR is upstream's own
// decode against those planes. See testdata/README.md.
var goldens = []struct {
	Name         string
	W, H, Frame  int
	C420         bool
	Budget       int
	UpstreamPSNR float64
}{
	{"synth-640x360-420", 640, 360, 2, true, 92160, 32.678},
	{"synth-640x360-444", 640, 360, 5, false, 115200, 30.360},
	{"synth-300x170-420", 300, 170, 1, true, 25500, 33.868},
}

func planes8(img *Image) [3][]byte {
	var out [3][]byte
	for c := 0; c < 3; c++ {
		out[c] = make([]byte, len(img.Planes[c]))
		for i, v := range img.Planes[c] {
			out[c][i] = uint8(clamp01(v)*255 + 0.5)
		}
	}
	return out
}

func psnrPlanes(src [3][]byte, got [3][]float32) float64 {
	var se float64
	n := 0
	for c := 0; c < 3; c++ {
		for i := range src[c] {
			d := float64(src[c][i]) - math.Round(float64(clamp01(got[c][i]))*255)
			se += d * d
			n++
		}
	}
	return 10 * math.Log10(255*255*float64(n)/se)
}

// TestUpstreamGoldens decodes bitstreams made by upstream's encoder.
func TestUpstreamGoldens(t *testing.T) {
	for _, g := range goldens {
		bs, err := os.ReadFile("testdata/" + g.Name + ".pyro")
		if err != nil {
			t.Fatal(err)
		}
		_, img := synthImage(g.W, g.H, g.Frame, g.C420)
		dec, err := NewDecoder(g.W, g.H, g.C420, 1391)
		if err != nil {
			t.Fatal(err)
		}
		psnr := psnrPlanes(planes8(img), dec.DecodeRecords(bs).Planes)
		t.Logf("%s: %d bytes, %.3f dB (upstream decode %.3f dB)", g.Name, len(bs), psnr, g.UpstreamPSNR)
		if psnr < g.UpstreamPSNR-0.1 {
			t.Errorf("%s: %.3f dB, upstream decodes it to %.3f dB", g.Name, psnr, g.UpstreamPSNR)
		}
	}
}
