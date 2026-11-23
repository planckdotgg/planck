//go:build darwin

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/internal/synth"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/vt"
)

func init() {
	commands["vt"] = runVT
	commands["sck"] = runSCK
}

type vtCase struct {
	chroma, input string
	w, h, fps     int
	kbps          int
}

var vtInputs = map[string]struct {
	cv      uint32
	capture capture.Format
}{
	"bgra": {vt.PixelBGRA, capture.FormatBGRA8},
	"nv12": {vt.Pixel420v, capture.FormatNV12},
	"p010": {vt.PixelX420, capture.FormatP010},
}

// syntheticBuffers fills n IOSurface pixel buffers with the shared test pattern.
func syntheticBuffers(input string, w, h, n int) ([]uintptr, error) {
	var out []uintptr
	for i := 0; i < n; i++ {
		pb, err := vt.NewPixelBuffer(w, h, vtInputs[input].cv)
		if err != nil {
			return nil, err
		}
		switch input {
		case "bgra":
			b := make([]byte, w*h*4)
			synth.BGRA(b, w, h, i)
			vt.FillPixelBuffer(pb, [][]byte{b}, []int{w * 4})
		case "nv12":
			b := make([]byte, w*h*3/2)
			synth.NV12(b, w, h, i)
			vt.FillPixelBuffer(pb, [][]byte{b[:w*h], b[w*h:]}, []int{w, w})
		case "p010":
			b := make([]byte, w*h*3)
			synth.P010(b, w, h, i)
			vt.FillPixelBuffer(pb, [][]byte{b[:w*h*2], b[w*h*2:]}, []int{w * 2, w * 2})
		}
		out = append(out, pb)
	}
	return out, nil
}

func rssMB() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Maxrss) / (1 << 20)
}

func runVTCase(c vtCase, frames int, pace bool) string {
	head := fmt.Sprintf("| %s | %s | %dx%d | %d |", c.chroma, c.input, c.w, c.h, c.fps)
	cfg := encode.Config{Codec: encode.HEVC, Chroma: chromaByName[c.chroma], Source: vtInputs[c.input].capture,
		Width: c.w, Height: c.h, FPS: c.fps, BitrateKbps: c.kbps}
	enc, err := vt.Open(cfg)
	if err != nil {
		return fmt.Sprintf("%s %v |", head, err)
	}
	defer enc.Close()
	enc.Keep = true
	bufs, err := syntheticBuffers(c.input, c.w, c.h, 8)
	if err != nil {
		return fmt.Sprintf("%s %v |", head, err)
	}
	defer func() {
		for _, b := range bufs {
			apple.CFRelease(b)
		}
	}()
	var dec *vt.Decoder
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()
	encMs, decMs := bench.NewSamples(frames), bench.NewSamples(frames)
	dst := make([]byte, 0, 4<<20)
	var bytes int
	var m0, m1 runtime.MemStats
	const warmup = 30
	interval := time.Second / time.Duration(c.fps)
	next := time.Now()
	var dropped int
	for i := 0; i < warmup+frames; i++ {
		if i == warmup {
			runtime.ReadMemStats(&m0)
		}
		if pace {
			for time.Now().Before(next) {
				if time.Until(next) > 1500*time.Microsecond {
					time.Sleep(500 * time.Microsecond)
				}
			}
			next = next.Add(interval)
		}
		f := capture.Frame{Surface: bufs[i%len(bufs)], Format: vtInputs[c.input].capture, Width: c.w, Height: c.h}
		t0 := apple.Now()
		pkt, err := enc.Encode(f, false, dst)
		t1 := apple.Now()
		if errors.Is(err, encode.ErrDropped) {
			dropped++
			continue
		}
		if err != nil {
			return fmt.Sprintf("%s frame %d: %v |", head, i, err)
		}
		dst = pkt.Data
		sample := enc.LastSample
		if dec == nil {
			if dec, err = vt.NewDecoder(sample); err != nil {
				apple.CFRelease(sample)
				return fmt.Sprintf("%s %v |", head, err)
			}
		}
		d0 := apple.Now()
		d1, err := dec.Decode(sample)
		apple.CFRelease(sample)
		if err != nil {
			return fmt.Sprintf("%s decode frame %d: %v |", head, i, err)
		}
		if i >= warmup {
			encMs.Add(float64(t1-t0) / 1e6)
			decMs.Add(float64(d1-d0) / 1e6)
			bytes += len(pkt.Data)
		}
	}
	runtime.ReadMemStats(&m1)
	e, d := encMs.Summarize(), decMs.Summarize()
	return fmt.Sprintf("%s %v | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.3f | %.0f | %d | %.1f |", head, enc.LowLatency,
		e.P50, e.P95, e.P99, e.Max, d.P50, d.P95, d.P99,
		float64(bytes)*8/1000/(float64(frames)/float64(c.fps)), dropped, float64(m1.Mallocs-m0.Mallocs)/float64(frames))
}

var chromaByName = map[string]encode.Chroma{"nv12": encode.NV12, "p010": encode.P010, "yuv444": encode.YUV444}

func runVT(args []string) error {
	fs := flag.NewFlagSet("vt", flag.ExitOnError)
	frames := fs.Int("frames", 1200, "measured frames per case")
	pace := fs.Bool("pace", true, "submit at the frame rate")
	soak := fs.Duration("soak", 0, "run 1080p120 HEVC encode+decode this long and report RSS")
	fs.Parse(args)
	fmt.Printf("# VideoToolbox HEVC, CGO_ENABLED=0 (purego). hardware decode=%v, pid %d\n\n", vt.HardwareDecode(), os.Getpid())
	if *soak > 0 {
		n := int(soak.Seconds() * 120)
		fmt.Printf("soak: %d frames, RSS before %.1f MiB\n", n, rssMB())
		fmt.Println(runVTCase(vtCase{"nv12", "nv12", 1920, 1080, 120, 50000}, n, true))
		runtime.GC()
		fmt.Printf("soak: max RSS after %.1f MiB, goroutines %d\n", rssMB(), runtime.NumGoroutine())
		return nil
	}
	fmt.Println("| chroma | input | res | fps | low-latency RC | encode p50 | p95 | p99 | max | decode p50 | p95 | p99 | kbps out | dropped | allocs/frame |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range []struct{ w, h, kbps int }{{1920, 1080, 50000}, {2560, 1440, 80000}} {
		for _, fps := range []int{120, 240} {
			kbps := s.kbps * fps / 120
			for _, in := range []struct{ chroma, input string }{{"nv12", "bgra"}, {"nv12", "nv12"}, {"p010", "p010"}, {"p010", "bgra"}, {"yuv444", "bgra"}} {
				fmt.Println(strings.TrimSpace(runVTCase(vtCase{in.chroma, in.input, s.w, s.h, fps, kbps}, *frames, *pace)))
			}
		}
	}
	return nil
}
