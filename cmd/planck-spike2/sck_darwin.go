//go:build darwin

package main

import (
	"flag"
	"fmt"
	"runtime"
	"time"

	"github.com/watzon/planck/internal/apple"
	"github.com/watzon/planck/internal/bench"
	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/sck"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/vt"
)

// The main goroutine must stay on the main thread for AppKit.
func init() { runtime.LockOSThread() }

type sckCase struct {
	w, h, fps int
	format    uint32
	name      string
}

func runSCKCase(c sckCase, frames int) string {
	head := fmt.Sprintf("| %dx%d | %d | %s |", c.w, c.h, c.fps, c.name)
	src, err := sck.Open(c.w, c.h, c.fps, c.format)
	if err != nil {
		return fmt.Sprintf("%s %v |", head, err)
	}
	defer src.Close()
	source := capture.FormatNV12
	if c.format == vt.PixelBGRA {
		source = capture.FormatBGRA8
	}
	enc, err := vt.Open(encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: source,
		Width: c.w, Height: c.h, FPS: c.fps, BitrateKbps: 50000 * c.w * c.h / (1920 * 1080)})
	if err != nil {
		return fmt.Sprintf("%s %v |", head, err)
	}
	defer enc.Close()
	age, interval, encMs, total := bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames)
	dst := make([]byte, 0, 4<<20)
	var prev int64
	var m0, m1 runtime.MemStats
	const warmup = 30
	for i := 0; i < warmup+frames; i++ {
		if i == warmup {
			runtime.ReadMemStats(&m0)
		}
		f, err := src.Next(2 * time.Second)
		if err != nil {
			return fmt.Sprintf("%s frame %d: %v (stats %+v) |", head, i, err, src.Stats())
		}
		t0 := apple.Now()
		pkt, err := enc.Encode(f, false, dst)
		if err != nil {
			return fmt.Sprintf("%s encode: %v |", head, err)
		}
		dst = pkt.Data
		if i >= warmup {
			if f.Presented != 0 {
				age.Add(float64(f.Acquired-f.Presented) / 1e6)
				total.Add(float64(enc.LastDone-int64(f.Presented)) / 1e6)
			}
			if prev != 0 {
				interval.Add(float64(int64(f.Acquired)-prev) / 1e6)
			}
			encMs.Add(float64(apple.Now()-t0) / 1e6)
		}
		prev = int64(f.Acquired)
	}
	runtime.ReadMemStats(&m1)
	a, iv, e, t := age.Summarize(), interval.Summarize(), encMs.Summarize(), total.Summarize()
	st := src.Stats()
	return fmt.Sprintf("%s %.3f / %.3f / %.3f | %.3f / %.3f | %.3f / %.3f / %.3f | %.3f / %.3f / %.3f | %d/%d/%d/%d | %.1f |", head,
		a.P50, a.P95, a.P99, iv.P50, iv.P99, e.P50, e.P95, e.P99, t.P50, t.P95, t.P99,
		st.Complete, st.Idle, st.Other, st.Dropped, float64(m1.Mallocs-m0.Mallocs)/float64(frames))
}

func runSCK(args []string) error {
	fs := flag.NewFlagSet("sck", flag.ExitOnError)
	frames := fs.Int("frames", 1200, "measured frames per case")
	soak := fs.Duration("soak", 0, "capture+encode 1080p120 this long and report RSS")
	animate := fs.Bool("animate", true, "drive screen motion with a small window")
	fs.Parse(args)
	fmt.Printf("# ScreenCaptureKit + VideoToolbox, CGO_ENABLED=0 (purego). Screen Recording allowed: %v\n\n", sck.Allowed())
	work := func() {
		if *soak > 0 {
			n := int(soak.Seconds() * 120)
			fmt.Printf("soak: %d frames, max RSS before %.1f MiB\n", n, rssMB())
			fmt.Println(runSCKCase(sckCase{1920, 1080, 120, vt.Pixel420v, "420v"}, n))
			runtime.GC()
			fmt.Printf("soak: max RSS after %.1f MiB, goroutines %d\n", rssMB(), runtime.NumGoroutine())
			return
		}
		fmt.Println("| res | fps | format | display → callback p50/p95/p99 | interval p50/p99 | VT encode p50/p95/p99 | display → encoded p50/p95/p99 | complete/idle/other/dropped | allocs/frame |")
		fmt.Println("|---|---|---|---|---|---|---|---|---|")
		for _, c := range []sckCase{
			{1920, 1080, 120, vt.Pixel420v, "420v"},
			{2560, 1440, 120, vt.Pixel420v, "420v"},
			{1920, 1080, 120, vt.PixelBGRA, "BGRA"},
			{2560, 1440, 120, vt.PixelBGRA, "BGRA"},
		} {
			fmt.Println(runSCKCase(c, *frames))
		}
	}
	if !*animate {
		work()
		return nil
	}
	apple.RunWithAnimator(40, 40, 160, work)
	return nil
}
