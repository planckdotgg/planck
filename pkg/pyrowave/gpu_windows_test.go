//go:build windows

package pyrowave_test

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/pyrowave"
	"github.com/planckdotgg/planck/pkg/pyrowave/ref"
)

// TestMain holds the NVIDIA high-performance policy for live runs, as the
// host does, before any device opens.
func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("PLANCK_LIVE_PYRO_TEST") == "1" {
			if a, err := dxgi.FindAdapter("NVIDIA"); err == nil {
				guard := gpu.AcquireHighPerf(a)
				fmt.Printf("power guard on %s: %s (%v)\n", a.Name, guard.Mode, guard.Reason)
				defer guard.Close()
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}

func liveAdapters(t *testing.T) []string {
	if os.Getenv("PLANCK_LIVE_PYRO_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_PYRO_TEST=1 to run on the GPUs")
	}
	if a := os.Getenv("PLANCK_LIVE_ADAPTER"); a != "" {
		return strings.Split(a, ",")
	}
	return []string{"NVIDIA", "Radeon", "UHD"}
}

type liveCase struct {
	name   string
	w, h   int
	chroma encode.Chroma
	src    capture.Format
	bpp    float64
}

// sourceImage is frame i as the encoder sees it, through ref's colour
// conversion, and its BGRA8 rendering.
func sourceImage(c liveCase, i int, matrix ref.Matrix) (*ref.Image, []byte) {
	c420 := !c.chroma.Full()
	px := make([]byte, c.w*c.h*4)
	var img *ref.Image
	if c.src == capture.FormatRGB10A2 {
		synth.RGB10A2(px, c.w, c.h, i)
		img = ref.FromRGB(c.w, c.h, c420, matrix, func(x, y int) (r, g, b float32) {
			p := px[(y*c.w+x)*4:]
			v := uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
			return float32(v&1023) / 1023, float32(v>>10&1023) / 1023, float32(v>>20&1023) / 1023
		})
	} else {
		synth.BGRA(px, c.w, c.h, i)
		img = ref.FromBGRA8(px, c.w, c.h, c420, matrix)
	}
	return img, img.BGRA8(matrix)
}

// rgb10ToBGRA8 converts R10G10B10A2 pixels to BGRA8 for PSNR.
func rgb10ToBGRA8(px []byte) []byte {
	out := make([]byte, len(px))
	for i := 0; i+3 < len(px); i += 4 {
		v := uint32(px[i]) | uint32(px[i+1])<<8 | uint32(px[i+2])<<16 | uint32(px[i+3])<<24
		q := func(c uint32) byte { return byte((c*255 + 511) / 1023) }
		out[i], out[i+1], out[i+2], out[i+3] = q(v>>20&1023), q(v>>10&1023), q(v&1023), 255
	}
	return out
}

func pct(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

// TestLiveD3D11 encodes synthetic 1080p frames on each GPU, checks the
// bitstream against the CPU reference, decodes it on the same GPU and
// compares that with the reference decode, and times both.
func TestLiveD3D11(t *testing.T) {
	cases := []liveCase{
		{"1080p-420-8bit-1.6bpp", 1920, 1080, encode.NV12, capture.FormatBGRA8, 1.6},
		{"1080p-420-8bit-4bpp", 1920, 1080, encode.NV12, capture.FormatBGRA8, 4},
		{"1080p-444-10bit-4bpp", 1920, 1080, encode.YUV444P10, capture.FormatRGB10A2, 4},
	}
	const fps, timed = 120, 240
	for _, match := range liveAdapters(t) {
		t.Run(match, func(t *testing.T) {
			dev := encodetest.Device(t, match)
			g := encodetest.GPU(dev)
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					if revert, err := win.BoostThread("Games"); err == nil {
						defer revert()
					}
					frames := encodetest.Frames(t, dev, c.src, c.w, c.h, 4)
					cfg := encode.Config{Codec: encode.PyroWave, Chroma: c.chroma, Source: c.src, Width: c.w, Height: c.h,
						FPS: fps, BitrateKbps: int(c.bpp * float64(c.w*c.h) * fps / 1000), ChunkLen: 1391}
					e, err := encode.Open(cfg, g)
					if err != nil {
						t.Fatal(err)
					}
					defer e.Close()
					enc := e.(*pyrowave.Encoder)
					dec, err := pyrowave.OpenD3D11Decoder(g, decode.Config{Codec: encode.PyroWave, Width: c.w, Height: c.h, Chroma: c.chroma, ChunkLen: 1391})
					if err != nil {
						t.Fatal(err)
					}
					defer dec.Close()
					dst := make([]byte, 0, 16<<20)
					pkt, err := enc.Encode(frames[0], true, dst)
					if err != nil {
						t.Fatal(err)
					}
					matrix := ref.BT709
					if c.src == capture.FormatRGB10A2 {
						matrix = ref.BT2020
					}
					img, src := sourceImage(c, 0, matrix)
					rdec, _ := ref.NewDecoder(c.w, c.h, !c.chroma.Full(), 1391)
					rimg, err := rdec.Decode(pkt.Data)
					if err != nil {
						t.Fatal(err)
					}
					if rdec.Parser().Rejected != 0 {
						t.Errorf("reference decoder rejected %d GPU records", rdec.Parser().Rejected)
					}
					refOut := rimg.BGRA8(matrix)
					gpuPSNR := ref.PSNR(src, refOut)
					renc, _ := ref.NewEncoder(c.w, c.h, !c.chroma.Full())
					renc.Matrix = matrix
					rrecs := renc.Encode(img, enc.BudgetBytes())
					rdec2, _ := ref.NewDecoder(c.w, c.h, !c.chroma.Full(), 1391)
					refPSNR := ref.PSNR(src, rdec2.DecodeRecords(rrecs).BGRA8(matrix))
					t.Logf("%s on %s: %d bytes in %d slices (%d critical); GPU encode %.2f dB, ref encode %.2f dB at %d budget bytes",
						c.name, dev.Info.Name, len(pkt.Data), len(pkt.SliceEnds), pkt.Critical, gpuPSNR, refPSNR, enc.BudgetBytes())
					if gpuPSNR < refPSNR-0.5 {
						t.Errorf("GPU encode %.2f dB, ref %.2f dB", gpuPSNR, refPSNR)
					}

					fr, err := dec.Decode(pkt.Data, 0)
					if err != nil {
						t.Fatal(err)
					}
					px, err := dec.ReadTexture(fr.Surface)
					if err != nil {
						t.Fatal(err)
					}
					if c.chroma.TenBit() {
						px = rgb10ToBGRA8(px)
					}
					match := ref.PSNR(refOut, px)
					t.Logf("GPU decode vs ref decode: %.2f dB; GPU decode vs source %.2f dB", match, ref.PSNR(src, px))
					if match < 50 {
						t.Errorf("GPU decode differs from the reference: %.2f dB", match)
					}

					var encT, decT []time.Duration
					for i := 0; i < timed+20; i++ {
						t0 := clock.Now()
						p, err := enc.Encode(frames[i%len(frames)], false, dst)
						t1 := clock.Now()
						if err != nil {
							t.Fatal(err)
						}
						if _, err := dec.Decode(p.Data, int64(i)); err != nil {
							t.Fatal(err)
						}
						dec.Wait()
						t2 := clock.Now()
						if i >= 20 {
							encT = append(encT, t1-t0)
							decT = append(decT, t2-t1)
						}
					}
					t.Logf("timing over %d frames: encode p50 %v p99 %v max %v; decode+wait p50 %v p99 %v max %v",
						timed, pct(encT, .5), pct(encT, .99), pct(encT, 1), pct(decT, .5), pct(decT, .99), pct(decT, 1))
					i := 0
					allocs := testing.AllocsPerRun(50, func() {
						p, _ := enc.Encode(frames[i%len(frames)], false, dst)
						dec.Decode(p.Data, int64(i))
						i++
					})
					if allocs != 0 {
						t.Errorf("%.1f allocations per encode+decode", allocs)
					}
					logGPUTimes(t, enc, dec, frames, dst)
				})
			}
		})
	}
}

// logGPUTimes reports the median GPU time of each pass over 60 frames.
func logGPUTimes(t *testing.T, enc *pyrowave.Encoder, dec *pyrowave.D3D11Decoder, frames []capture.Frame, dst []byte) {
	if err := enc.Profile(); err != nil {
		t.Fatal(err)
	}
	if err := dec.Profile(); err != nil {
		t.Fatal(err)
	}
	encMarks, decMarks := map[string][]float64{}, map[string][]float64{}
	for i := 0; i < 60; i++ {
		p, _ := enc.Encode(frames[i%len(frames)], false, dst)
		for k, v := range enc.GPUTimes() {
			encMarks[k] = append(encMarks[k], v)
		}
		dec.Decode(p.Data, 0)
		dec.Wait()
		for k, v := range dec.GPUTimes() {
			decMarks[k] = append(decMarks[k], v)
		}
	}
	med := func(m map[string][]float64, order ...string) string {
		var b strings.Builder
		for _, k := range order {
			v := m[k]
			sort.Float64s(v)
			fmt.Fprintf(&b, " %s %.3f", k, v[len(v)/2])
		}
		return b.String()
	}
	t.Logf("GPU ms since frame start (median): encode%s", med(encMarks, "convert", "dwt", "analyze", "finalize", "pack", "readback"))
	t.Logf("GPU ms since frame start (median): decode%s", med(decMarks, "upload", "clear", "dequant", "idwt", "yuv2rgb"))
}
