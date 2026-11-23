//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"runtime"
	"time"

	"github.com/planckdotgg/planck/internal/bench"
	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
)

// openCapture finds the first output of the matching adapter and duplicates it.
func openCapture(d *device, hdr bool) (*dxgi.Source, win.OutputDesc, error) {
	outs, err := d.adapter.Outputs()
	if err != nil {
		return nil, win.OutputDesc{}, err
	}
	if len(outs) == 0 {
		return nil, win.OutputDesc{}, fmt.Errorf("%s drives no output; Desktop Duplication must run on the adapter that owns the display", d.info.Name)
	}
	for _, o := range outs[1:] {
		o.Release()
	}
	defer outs[0].Release()
	desc, err := outs[0].Desc()
	if err != nil {
		return nil, desc, err
	}
	src, err := dxgi.Open(d.dev, d.info, outs[0], hdr)
	return src, desc, err
}

func startAnimator(desc win.OutputDesc) (stop func()) {
	ch := make(chan struct{})
	done := make(chan struct{})
	const size = 160
	r := desc.DesktopCoordinates
	go func() {
		defer close(done)
		if err := win.Animate(r.Right-size-40, r.Bottom-size-80, size, ch); err != nil {
			fmt.Println("animator:", err)
		}
	}()
	return func() { close(ch); <-done }
}

func runDDA(args []string) error {
	fs := flag.NewFlagSet("dda", flag.ExitOnError)
	adapter := fs.String("adapter", "AMD", "adapter that owns the display")
	frames := fs.Int("frames", 600, "measured frames")
	hdr := fs.Bool("hdr", false, "use DuplicateOutput1 with 10-bit/FP16 formats")
	animate := fs.Bool("animate", true, "drive desktop motion with a small window")
	fs.Parse(args)

	d, err := openDevice(*adapter)
	if err != nil {
		return err
	}
	defer d.Close()
	src, desc, err := openCapture(d, *hdr)
	if err != nil {
		return err
	}
	defer src.Close()
	w, h := src.Size()
	fmt.Printf("# DDA on %s %s: %dx%d @ %.2f Hz, format %s\n", d.info.Name, desc.Name(), w, h, src.Refresh(), src.Format())
	if *animate {
		stop := startAnimator(desc)
		defer stop()
		time.Sleep(200 * time.Millisecond)
	}
	own, err := d.dev.CreateTexture2D(&win.Texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
		Format: win.DXGI_FORMAT_B8G8R8A8_UNORM, SampleCount: 1, Usage: win.D3D11_USAGE_DEFAULT, BindFlags: win.D3D11_BIND_RENDER_TARGET})
	if err != nil {
		return err
	}
	defer own.Release()

	interval := bench.NewSamples(*frames)
	age := bench.NewSamples(*frames)
	copyMs := bench.NewSamples(*frames)
	total := bench.NewSamples(*frames)
	var prev time.Duration
	var accumulated, timeouts, cursorOnly int
	var ms0, ms1 runtime.MemStats
	const warmup = 30
	for n := 0; n < warmup+*frames; {
		if n == warmup && prev != 0 {
			runtime.ReadMemStats(&ms0)
		}
		f, err := src.Next(100 * time.Millisecond)
		if errors.Is(err, capture.ErrTimeout) {
			timeouts++
			continue
		}
		if err != nil {
			return err
		}
		if f.Presented == 0 {
			cursorOnly++
			continue
		}
		c0 := clock.Now()
		if src.Format() == capture.FormatBGRA8 {
			d.ctx.CopyResource(own.Object, win.ObjectAt(f.Surface))
		}
		d.ctx.WaitIdle(d.idle)
		c1 := clock.Now()
		if n >= warmup {
			if prev != 0 {
				interval.Add(clock.Ms(prev, f.Acquired))
			}
			age.Add(clock.Ms(f.Presented, f.Acquired))
			copyMs.Add(clock.Ms(c0, c1))
			total.Add(clock.Ms(f.Presented, c1))
			if src.AccumulatedFrames() > 1 {
				accumulated++
			}
		}
		prev = f.Acquired
		n++
	}
	runtime.ReadMemStats(&ms1)
	fmt.Println("| metric | p50 | p95 | p99 | max | mean | n |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, row := range []struct {
		name string
		s    bench.Summary
	}{
		{"acquire interval ms", interval.Summarize()},
		{"present → acquire ms", age.Summarize()},
		{"GPU copy + sync ms", copyMs.Summarize()},
		{"present → frame ready ms", total.Summarize()},
	} {
		fmt.Printf("| %s | %.3f | %.3f | %.3f | %.3f | %.3f | %d |\n", row.name, row.s.P50, row.s.P95, row.s.P99, row.s.Max, row.s.Mean, row.s.N)
	}
	fmt.Printf("\nframes with >1 accumulated present: %d, timeouts: %d, cursor-only: %d, allocs/frame: %.2f\n",
		accumulated, timeouts, cursorOnly, float64(ms1.Mallocs-ms0.Mallocs)/float64(*frames))
	return nil
}
