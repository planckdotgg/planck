//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/bench"
	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/amf"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
)

// runPipeline measures display present → encoded packet on the real desktop:
// DDA on the display GPU, then either a same-GPU encoder (AMF) or a staging
// transfer to the RTX 4080 and NVENC.
func runPipeline(args []string) error {
	fs := flag.NewFlagSet("pipeline", flag.ExitOnError)
	frames := fs.Int("frames", 600, "measured frames per path")
	fs.Parse(args)

	amd, err := openDevice("AMD")
	if err != nil {
		return err
	}
	defer amd.Close()
	nv, err := openDevice("NVIDIA")
	if err != nil {
		return err
	}
	defer nv.Close()
	src, desc, err := openCapture(amd, false)
	if err != nil {
		return err
	}
	defer src.Close()
	w, h := src.Size()
	stop := startAnimator(desc)
	defer stop()
	time.Sleep(200 * time.Millisecond)
	fmt.Printf("# %s %dx%d @ %.0f Hz → encode\n\n", desc.Name(), w, h, src.Refresh())
	fmt.Println("| path | capture copy p50 | transfer p50/p99 | encode p50/p99 | present → encoded p50 | p95 | p99 | max | n |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|")

	cfg := encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: capture.FormatBGRA8, Width: w, Height: h, FPS: 60, BitrateKbps: 30000}
	rt := uint32(win.D3D11_BIND_RENDER_TARGET | win.D3D11_BIND_SHADER_RESOURCE)
	amdTex, err := amd.dev.CreateTexture2D(texDesc("bgra", w, h, win.D3D11_USAGE_DEFAULT, rt, 0, 0))
	if err != nil {
		return err
	}
	defer amdTex.Release()

	// Same GPU: AMF on the display adapter.
	if enc, err := amf.Open(amd.dev, amd.info, cfg); err != nil {
		fmt.Printf("| DDA → AMF (same GPU) | failed: %v |\n", err)
	} else {
		res := pipelineRun(src, amd, amdTex, *frames, func(f capture.Frame, dst []byte) ([]byte, float64, error) {
			p, err := enc.Encode(capture.Frame{Surface: uintptr(unsafe.Pointer(amdTex.Object)), Format: capture.FormatBGRA8, Width: w, Height: h}, false, dst)
			return p.Data, 0, err
		})
		enc.Close()
		fmt.Printf("| DDA (AMD) → AMF (same GPU, zero copy) | %s |\n", res)
	}

	// Cross adapter: staging round trip to the RTX 4080, then NVENC.
	read, err := amd.dev.CreateTexture2D(texDesc("bgra", w, h, win.D3D11_USAGE_STAGING, 0, win.D3D11_CPU_ACCESS_READ, 0))
	if err != nil {
		return err
	}
	defer read.Release()
	write, err := nv.dev.CreateTexture2D(texDesc("bgra", w, h, win.D3D11_USAGE_STAGING, 0, win.D3D11_CPU_ACCESS_WRITE, 0))
	if err != nil {
		return err
	}
	defer write.Release()
	nvTex, err := nv.dev.CreateTexture2D(texDesc("bgra", w, h, win.D3D11_USAGE_DEFAULT, rt, 0, 0))
	if err != nil {
		return err
	}
	defer nvTex.Release()
	enc, err := nvenc.Open(nv.dev, nv.info, cfg, nvenc.Options{})
	if err != nil {
		return err
	}
	defer enc.Close()
	var rm, wm win.MappedSubresource
	res := pipelineRun(src, amd, amdTex, *frames, func(f capture.Frame, dst []byte) ([]byte, float64, error) {
		t0 := clock.Now()
		amd.ctx.CopyResource(read.Object, amdTex.Object)
		if err := amd.ctx.Map(read.Object, win.D3D11_MAP_READ, &rm); err != nil {
			return nil, 0, err
		}
		if err := nv.ctx.Map(write.Object, win.D3D11_MAP_WRITE, &wm); err != nil {
			return nil, 0, err
		}
		from := unsafe.Slice((*byte)(win.Ptr(rm.Data)), int(rm.RowPitch)*h)
		to := unsafe.Slice((*byte)(win.Ptr(wm.Data)), int(wm.RowPitch)*h)
		for y := 0; y < h; y++ {
			copy(to[y*int(wm.RowPitch):][:w*4], from[y*int(rm.RowPitch):][:w*4])
		}
		nv.ctx.Unmap(write.Object)
		amd.ctx.Unmap(read.Object)
		nv.ctx.CopyResource(nvTex.Object, write.Object)
		xfer := clock.Ms(t0, clock.Now())
		p, err := enc.Encode(capture.Frame{Surface: uintptr(unsafe.Pointer(nvTex.Object)), Format: capture.FormatBGRA8, Width: w, Height: h}, false, dst)
		return p.Data, xfer, err
	})
	fmt.Printf("| DDA (AMD) → staging → NVENC (RTX 4080) | %s |\n", res)
	return nil
}

type stepFn func(f capture.Frame, dst []byte) ([]byte, float64, error)

func pipelineRun(src capture.Source, d *device, own win.Texture, frames int, step stepFn) string {
	copyMs, xfer, enc, total := bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames)
	dst := make([]byte, 0, 4<<20)
	const warmup = 30
	for n := 0; n < warmup+frames; {
		f, err := src.Next(100 * time.Millisecond)
		if errors.Is(err, capture.ErrTimeout) {
			continue
		}
		if err != nil {
			return "capture failed: " + err.Error()
		}
		if f.Presented == 0 {
			continue
		}
		c0 := clock.Now()
		d.ctx.CopyResource(own.Object, win.ObjectAt(f.Surface))
		src.Release()
		c1 := clock.Now()
		out, x, err := step(f, dst)
		end := clock.Now()
		if err != nil {
			return "encode failed: " + err.Error()
		}
		dst = out
		if n >= warmup {
			copyMs.Add(clock.Ms(c0, c1))
			xfer.Add(x)
			enc.Add(clock.Ms(c1, end) - x)
			total.Add(clock.Ms(f.Presented, end))
		}
		n++
	}
	c, x, e, t := copyMs.Summarize(), xfer.Summarize(), enc.Summarize(), total.Summarize()
	return fmt.Sprintf("%.3f | %.3f / %.3f | %.3f / %.3f | %.3f | %.3f | %.3f | %.3f | %d",
		c.P50, x.P50, x.P99, e.P50, e.P99, t.P50, t.P95, t.P99, t.Max, t.N)
}
