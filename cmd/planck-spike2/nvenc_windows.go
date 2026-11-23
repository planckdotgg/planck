//go:build windows

package main

import (
	"flag"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/planckdotgg/planck/internal/bench"
	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
)

var codecs = map[string]encode.Codec{"h264": encode.H264, "hevc": encode.HEVC, "av1": encode.AV1}
var chromas = map[string]encode.Chroma{"nv12": encode.NV12, "p010": encode.P010, "yuv444": encode.YUV444, "yuv444p10": encode.YUV444P10}

type encodeCase struct {
	codec, chroma, input string
	w, h, fps, kbps      int
	slices               int
	subframe             bool
	ir                   int
	split                uint32
}

type encodeResult struct {
	encodeCase
	encode, firstSlice bench.Summary
	kbpsOut            float64
	allocs             float64
	keyBytes           int
	err                error
}

type opener func(cfg encode.Config, c encodeCase) (encode.Encoder, error)

func openNVENC(d *device) opener {
	return func(cfg encode.Config, c encodeCase) (encode.Encoder, error) {
		return nvenc.Open(d.dev, d.info, cfg, nvenc.Options{SplitMode: c.split})
	}
}

func runEncodeCase(d *device, c encodeCase, frames int, pace bool, open opener) encodeResult {
	res := encodeResult{encodeCase: c}
	cfg := encode.Config{
		Codec: codecs[c.codec], Chroma: chromas[c.chroma], Source: inputFormats[c.input].capture, Width: c.w, Height: c.h, FPS: c.fps,
		BitrateKbps: c.kbps, Slices: c.slices, SubFrame: c.subframe, IntraRefreshPeriod: c.ir,
	}
	enc, err := open(cfg, c)
	if err != nil {
		res.err = err
		return res
	}
	defer enc.Close()
	textures, err := d.syntheticFrames(c.input, c.w, c.h, 8, 0)
	if err != nil {
		res.err = err
		return res
	}
	defer func() {
		for _, t := range textures {
			t.Release()
		}
	}()
	in := inputFormats[c.input]
	dst := make([]byte, 0, 4<<20)
	lat := bench.NewSamples(frames)
	first := bench.NewSamples(frames)
	const warmup = 30
	var bytes int
	var ms0, ms1 runtime.MemStats
	interval := time.Second / time.Duration(c.fps)
	next := clock.Now()
	for i := 0; i < warmup+frames; i++ {
		if i == warmup {
			runtime.ReadMemStats(&ms0)
		}
		if pace {
			for clock.Now() < next {
				if next-clock.Now() > 1500*time.Microsecond {
					time.Sleep(500 * time.Microsecond)
				}
			}
			next += interval
		}
		t := textures[i%len(textures)]
		frame := capture.Frame{Surface: uintptr(unsafePointer(t.Object)), Adapter: d.info.LUID, Format: in.capture, Width: c.w, Height: c.h}
		start := clock.Now()
		pkt, err := enc.Encode(frame, i == 0, dst)
		end := clock.Now()
		if err != nil {
			res.err = fmt.Errorf("frame %d: %w", i, err)
			return res
		}
		dst = pkt.Data
		if i == 0 {
			res.keyBytes = len(pkt.Data)
		}
		if i >= warmup {
			lat.Add(clock.Ms(start, end))
			bytes += len(pkt.Data)
			if sf, ok := enc.(interface{ FirstSlice() time.Duration }); ok && c.subframe {
				first.Add(float64(sf.FirstSlice()) / 1e6)
			}
		}
	}
	runtime.ReadMemStats(&ms1)
	res.encode = lat.Summarize()
	res.firstSlice = first.Summarize()
	res.kbpsOut = float64(bytes) * 8 / 1000 / (float64(frames) / float64(c.fps))
	res.allocs = float64(ms1.Mallocs-ms0.Mallocs) / float64(frames)
	return res
}

func runNVENC(args []string) error {
	fs := flag.NewFlagSet("nvenc", flag.ExitOnError)
	adapter := fs.String("adapter", "NVIDIA", "adapter name substring")
	codec := fs.String("codec", "hevc", "h264, hevc, or av1")
	chroma := fs.String("chroma", "nv12", "nv12, p010, yuv444, yuv444p10")
	input := fs.String("input", "bgra", "bgra, rgb10, nv12, p010")
	w := fs.Int("w", 1920, "width")
	h := fs.Int("h", 1080, "height")
	fps := fs.Int("fps", 120, "frame rate")
	kbps := fs.Int("kbps", 50000, "bit rate")
	frames := fs.Int("frames", 600, "measured frames")
	slices := fs.Int("slices", 0, "slices per picture")
	subframe := fs.Bool("subframe", false, "sub-frame readback")
	ir := fs.Int("ir", 0, "intra refresh period in frames")
	split := fs.Uint("split", 0, "split encode mode: 0 auto, 2 two engines, 15 off")
	pace := fs.Bool("pace", true, "submit at the frame rate instead of back to back")
	matrix := fs.Bool("matrix", false, "run the Phase 0 matrix")
	fs.Parse(args)

	d, err := openDevice(*adapter)
	if err != nil {
		return err
	}
	defer d.Close()
	maj, min, _ := nvenc.MaxAPIVersion()
	caps, err := nvenc.Probe(d.dev, d.info)
	if err != nil {
		return err
	}
	fmt.Printf("# NVENC on %s (luid 0x%x), driver API %d.%d\n", d.info.Name, uint64(d.info.LUID), maj, min)
	fmt.Printf("# caps: codecs=%v chroma=%v max=%dx%d intraRefresh=%v subFrame=%v engines=%d\n\n",
		caps.Codecs, caps.Chroma, caps.MaxWidth, caps.MaxHeight, caps.IntraRefresh, caps.SubFrame, caps.Engines)

	var cases []encodeCase
	if *matrix {
		cases = nvencMatrix()
	} else {
		cases = []encodeCase{{*codec, *chroma, *input, *w, *h, *fps, *kbps, *slices, *subframe, *ir, uint32(*split)}}
	}
	printEncodeHeader()
	for _, c := range cases {
		printEncodeRow(c, runEncodeCase(d, c, *frames, *pace, openNVENC(d)))
	}
	return nil
}

func printEncodeHeader() {
	fmt.Println("| codec | chroma | input | res | fps | slices | subframe | IR | split | encode p50 | p95 | p99 | max | first slice p50/p99 | kbps out | IDR bytes | allocs/frame |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
}

func printEncodeRow(c encodeCase, r encodeResult) {
	{
		head := fmt.Sprintf("| %s | %s | %s | %dx%d | %d | %d | %v | %d | %d |", c.codec, c.chroma, c.input, c.w, c.h, c.fps, c.slices, c.subframe, c.ir, c.split)
		if r.err != nil {
			fmt.Printf("%s %s |\n", head, strings.ReplaceAll(r.err.Error(), "|", "/"))
			return
		}
		fs := "-"
		if c.subframe {
			fs = fmt.Sprintf("%.3f / %.3f", r.firstSlice.P50, r.firstSlice.P99)
		}
		fmt.Printf("%s %.3f | %.3f | %.3f | %.3f | %s | %.0f | %d | %.1f |\n", head,
			r.encode.P50, r.encode.P95, r.encode.P99, r.encode.Max, fs, r.kbpsOut, r.keyBytes, r.allocs)
	}
}

func nvencMatrix() []encodeCase {
	var out []encodeCase
	type size struct{ w, h, kbps int }
	sizes := []size{{1920, 1080, 50000}, {2560, 1440, 80000}}
	for _, s := range sizes {
		for _, fps := range []int{120, 240} {
			kbps := s.kbps * fps / 120
			for _, codec := range []string{"hevc", "av1"} {
				out = append(out,
					encodeCase{codec, "nv12", "bgra", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "nv12", "nv12", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "p010", "bgra", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "p010", "p010", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "p010", "rgb10", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "yuv444", "bgra", s.w, s.h, fps, kbps, 0, false, 0, 0},
					encodeCase{codec, "yuv444p10", "rgb10", s.w, s.h, fps, kbps, 0, false, 0, 0},
				)
			}
			out = append(out,
				encodeCase{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 0, false, 0, 15},
				encodeCase{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 0, false, fps, 0},
				encodeCase{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 4, false, 0, 0},
				encodeCase{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 4, true, 0, 0},
				encodeCase{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 8, true, 0, 15},
				encodeCase{"av1", "nv12", "bgra", s.w, s.h, fps, kbps, 4, false, 0, 0},
			)
		}
	}
	return out
}
