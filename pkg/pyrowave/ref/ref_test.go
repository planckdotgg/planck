package ref

import (
	"math"
	"math/rand"
	"testing"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/pyrowave"
)

func TestDWTRoundTrip(t *testing.T) {
	const w, h = 64, 32
	src := make([]float32, w*h)
	rng := rand.New(rand.NewSource(1))
	for i := range src {
		src[i] = rng.Float32() - 0.5
	}
	var bands [4][]float32
	forward2D(src, w, h, &bands)
	got := inverse2D(&bands, w, h)
	for i := range src {
		if d := math.Abs(float64(got[i] - src[i])); d > 1e-5 {
			t.Fatalf("sample %d: got %v want %v", i, got[i], src[i])
		}
	}
}

func synthImage(w, h, i int, chroma420 bool) ([]byte, *Image) {
	px := make([]byte, w*h*4)
	synth.BGRA(px, w, h, i)
	return px, FromBGRA8(px, w, h, chroma420, BT709)
}

func TestRoundTripPSNR(t *testing.T) {
	for _, tc := range []struct {
		w, h      int
		chroma420 bool
		bpp       float64
		min       float64
	}{
		{1920, 1080, true, 4, 35},
		{1280, 720, false, 8, 35},
		{200, 100, true, 4, 30},
		{1280, 720, true, 24, 45},
	} {
		_, img := synthImage(tc.w, tc.h, 3, tc.chroma420)
		px := img.BGRA8(BT709)
		enc, err := NewEncoder(tc.w, tc.h, tc.chroma420)
		if err != nil {
			t.Fatal(err)
		}
		budget := int(tc.bpp * float64(tc.w*tc.h) / 8)
		recs := enc.Encode(img, budget)
		dec, _ := NewDecoder(tc.w, tc.h, tc.chroma420, 1391)
		out := dec.DecodeRecords(recs).BGRA8(BT709)
		psnr := PSNR(px, out)
		t.Logf("%dx%d 420=%v %.1f bpp: %d of %d bytes, PSNR %.2f dB", tc.w, tc.h, tc.chroma420, tc.bpp, len(recs), budget, psnr)
		if len(recs) > budget {
			t.Errorf("records %d bytes over budget %d", len(recs), budget)
		}
		if psnr < tc.min {
			t.Errorf("PSNR %.2f below %.0f", psnr, tc.min)
		}
	}
}

func TestChunkedMatchesRecords(t *testing.T) {
	const w, h = 640, 360
	_, img := synthImage(w, h, 1, true)
	enc, _ := NewEncoder(w, h, true)
	recs := enc.Encode(img, w*h*3)
	p, err := pyrowave.NewPacker(enc.L, 1391)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := p.Pack(nil, enc.Header, recs)
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := NewDecoder(w, h, true, 1391)
	a := dec.DecodeRecords(recs).BGRA8(BT709)
	img2, err := dec.Decode(pk.Data)
	if err != nil {
		t.Fatal(err)
	}
	if psnr := PSNR(a, img2.BGRA8(BT709)); !math.IsInf(psnr, 1) {
		t.Fatalf("chunked decode differs from record decode: %.2f dB", psnr)
	}
}
