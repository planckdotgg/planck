//go:build darwin && arm64

package pyrowave_test

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/pyrowave"
	"github.com/planckdotgg/planck/pkg/pyrowave/ref"
)

func durPct(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

// TestLiveMetal decodes reference-encoded synthetic frames with the Metal
// decoder, compares the pictures with the reference decoder's, and times
// decoding.
func TestLiveMetal(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_PYRO_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_PYRO_TEST=1 to run on the GPU")
	}
	for _, c := range []struct {
		name   string
		w, h   int
		chroma encode.Chroma
		bpp    float64
	}{
		{"1080p-420-1.6bpp", 1920, 1080, encode.NV12, 1.6},
		{"1080p-420-4bpp", 1920, 1080, encode.NV12, 4},
		{"1080p-444-4bpp", 1920, 1080, encode.YUV444, 4},
		{"1440p-420-1.6bpp", 2560, 1440, encode.NV12, 1.6},
		{"3024x1964-420-1.6bpp", 3024, 1964, encode.NV12, 1.6},
	} {
		t.Run(c.name, func(t *testing.T) {
			c420 := !c.chroma.Full()
			var aus [][]byte
			var want [][]byte
			for i := 0; i < 2; i++ {
				px := make([]byte, c.w*c.h*4)
				synth.BGRA(px, c.w, c.h, i)
				enc, _ := ref.NewEncoder(c.w, c.h, c420)
				enc.Header.Sequence = uint8(i)
				recs := enc.Encode(ref.FromBGRA8(px, c.w, c.h, c420, ref.BT709), int(c.bpp*float64(c.w*c.h)/8))
				p, _ := pyrowave.NewPacker(enc.L, 1391)
				pk, err := p.Pack(nil, enc.Header, recs)
				if err != nil {
					t.Fatal(err)
				}
				aus = append(aus, append([]byte(nil), pk.Data...))
				rd, _ := ref.NewDecoder(c.w, c.h, c420, 1391)
				img, err := rd.Decode(pk.Data)
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, img.BGRA8(ref.BT709))
			}
			d, err := decode.Open(decode.Target{}, decode.Config{Codec: encode.PyroWave, Width: c.w, Height: c.h, Chroma: c.chroma, ChunkLen: 1391})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			dec := d.(*pyrowave.MetalDecoder)
			for i, au := range aus {
				f, err := dec.Decode(au, int64(i))
				if err != nil {
					t.Fatal(err)
				}
				dec.Wait()
				got := pyrowave.ReadSurface(f.Surface, c.w, c.h)
				if psnr := ref.PSNR(want[i], got); psnr < 50 {
					t.Errorf("frame %d: Metal decode vs reference %.2f dB", i, psnr)
				} else if i == 0 {
					t.Logf("%s: %d bytes, Metal decode vs reference decode %.2f dB", c.name, len(au), psnr)
				}
			}
			const n = 240
			var submit, wall []time.Duration
			var gpu []float64
			for i := 0; i < n+20; i++ {
				t0 := time.Now()
				if _, err := dec.Decode(aus[i%2], int64(i)); err != nil {
					t.Fatal(err)
				}
				t1 := time.Now()
				g := dec.Wait()
				t2 := time.Now()
				if i >= 20 {
					submit = append(submit, t1.Sub(t0))
					wall = append(wall, t2.Sub(t0))
					gpu = append(gpu, g*1e3)
				}
			}
			sort.Float64s(gpu)
			t.Logf("%s over %d frames: GPU p50 %.3f ms p99 %.3f ms; Decode call (CPU parse+submit) p50 %v; call to GPU done p50 %v p99 %v",
				c.name, n, gpu[n/2], gpu[n*99/100], durPct(submit, .5), durPct(wall, .5), durPct(wall, .99))
			i := 0
			if a := testing.AllocsPerRun(30, func() { dec.Decode(aus[i%2], 0); i++ }); a != 0 {
				t.Errorf("%.1f allocations per Decode", a)
			}
		})
	}
}
