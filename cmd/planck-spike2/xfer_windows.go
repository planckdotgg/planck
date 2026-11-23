//go:build windows

package main

import (
	"flag"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"github.com/planckdotgg/planck/internal/bench"
	"github.com/planckdotgg/planck/internal/clock"
	"github.com/planckdotgg/planck/internal/win"
)

type xferResult struct {
	method, format string
	total          bench.Summary
	parts          string
	allocs         float64
	err            error
}

func texDesc(format string, w, h int, usage, bind, cpu, misc uint32) *win.Texture2DDesc {
	return &win.Texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1,
		Format: inputFormats[format].dxgi, SampleCount: 1, Usage: usage, BindFlags: bind, CPUAccessFlags: cpu, MiscFlags: misc}
}

func bindFor(format string) uint32 {
	if format == "bgra" {
		return win.D3D11_BIND_RENDER_TARGET | win.D3D11_BIND_SHADER_RESOURCE
	}
	return 0
}

// rows is how many rows a mapped surface of this format has (NV12 and P010
// put the chroma plane after the luma rows).
func rows(format string, h int) int {
	if format == "nv12" || format == "p010" {
		return h * 3 / 2
	}
	return h
}

func xferSameGPU(src *device, format string, w, h, frames int) xferResult {
	r := xferResult{method: "same-GPU CopyResource", format: format}
	frs, err := src.syntheticFrames(format, w, h, 2, 0)
	if err != nil {
		r.err = err
		return r
	}
	dst, err := src.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_DEFAULT, bindFor(format), 0, 0))
	if err != nil {
		r.err = err
		return r
	}
	defer dst.Release()
	s := bench.NewSamples(frames)
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < frames; i++ {
		t0 := clock.Now()
		src.ctx.CopyResource(dst.Object, frs[i%2].Object)
		src.ctx.WaitIdle(src.idle)
		s.Add(clock.Ms(t0, clock.Now()))
	}
	runtime.ReadMemStats(&m1)
	r.total = s.Summarize()
	r.allocs = float64(m1.Mallocs-m0.Mallocs) / float64(frames)
	return r
}

func xferShared(src, dst *device, format string, w, h, frames int, nt bool) xferResult {
	r := xferResult{method: "shared legacy handle", format: format}
	misc := uint32(win.D3D11_RESOURCE_MISC_SHARED)
	if nt {
		r.method = "shared NT handle"
		misc = win.D3D11_RESOURCE_MISC_SHARED | win.D3D11_RESOURCE_MISC_SHARED_NTHANDLE
	}
	frs, err := src.syntheticFrames(format, w, h, 2, 0)
	if err != nil {
		r.err = err
		return r
	}
	shared, err := src.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_DEFAULT, bindFor(format), 0, misc))
	if err != nil {
		r.err = fmt.Errorf("create shared texture on %s: %w", src.info.Name, err)
		return r
	}
	defer shared.Release()
	var handle uintptr
	if nt {
		handle, err = shared.CreateSharedHandle()
	} else {
		handle, err = shared.SharedHandle()
	}
	if err != nil {
		r.err = fmt.Errorf("export: %w", err)
		return r
	}
	var opened win.Texture
	if nt {
		opened, err = dst.dev.OpenSharedResource1(handle)
	} else {
		opened, err = dst.dev.OpenSharedResource(handle)
	}
	if err != nil {
		r.err = fmt.Errorf("open on %s: %w", dst.info.Name, err)
		return r
	}
	defer opened.Release()
	local, err := dst.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_DEFAULT, bindFor(format), 0, 0))
	if err != nil {
		r.err = err
		return r
	}
	defer local.Release()
	s := bench.NewSamples(frames)
	for i := 0; i < frames; i++ {
		t0 := clock.Now()
		src.ctx.CopyResource(shared.Object, frs[i%2].Object)
		src.ctx.WaitIdle(src.idle)
		dst.ctx.CopyResource(local.Object, opened.Object)
		dst.ctx.WaitIdle(dst.idle)
		s.Add(clock.Ms(t0, clock.Now()))
	}
	r.total = s.Summarize()
	return r
}

func xferStaging(src, dst *device, format string, w, h, frames int) xferResult {
	r := xferResult{method: "staging readback + upload", format: format}
	frs, err := src.syntheticFrames(format, w, h, 2, 0)
	if err != nil {
		r.err = err
		return r
	}
	read, err := src.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_STAGING, 0, win.D3D11_CPU_ACCESS_READ, 0))
	if err != nil {
		r.err = fmt.Errorf("staging read: %w", err)
		return r
	}
	defer read.Release()
	write, err := dst.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_STAGING, 0, win.D3D11_CPU_ACCESS_WRITE, 0))
	if err != nil {
		r.err = fmt.Errorf("staging write: %w", err)
		return r
	}
	defer write.Release()
	local, err := dst.dev.CreateTexture2D(texDesc(format, w, h, win.D3D11_USAGE_DEFAULT, bindFor(format), 0, 0))
	if err != nil {
		r.err = err
		return r
	}
	defer local.Release()
	rowBytes := int(inputFormats[format].pitch(w))
	n := rows(format, h)
	total, down, cpy, up := bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames), bench.NewSamples(frames)
	var rm, wm win.MappedSubresource
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < frames; i++ {
		t0 := clock.Now()
		src.ctx.CopyResource(read.Object, frs[i%2].Object)
		if err := src.ctx.Map(read.Object, win.D3D11_MAP_READ, &rm); err != nil {
			r.err = err
			return r
		}
		t1 := clock.Now()
		if err := dst.ctx.Map(write.Object, win.D3D11_MAP_WRITE, &wm); err != nil {
			r.err = err
			return r
		}
		from := unsafe.Slice((*byte)(win.Ptr(rm.Data)), int(rm.RowPitch)*n)
		to := unsafe.Slice((*byte)(win.Ptr(wm.Data)), int(wm.RowPitch)*n)
		if rm.RowPitch == wm.RowPitch {
			copy(to, from)
		} else {
			for y := 0; y < n; y++ {
				copy(to[y*int(wm.RowPitch):][:rowBytes], from[y*int(rm.RowPitch):][:rowBytes])
			}
		}
		dst.ctx.Unmap(write.Object)
		src.ctx.Unmap(read.Object)
		t2 := clock.Now()
		dst.ctx.CopyResource(local.Object, write.Object)
		dst.ctx.WaitIdle(dst.idle)
		t3 := clock.Now()
		total.Add(clock.Ms(t0, t3))
		down.Add(clock.Ms(t0, t1))
		cpy.Add(clock.Ms(t1, t2))
		up.Add(clock.Ms(t2, t3))
	}
	runtime.ReadMemStats(&m1)
	r.total = total.Summarize()
	r.allocs = float64(m1.Mallocs-m0.Mallocs) / float64(frames)
	r.parts = fmt.Sprintf("readback p50 %.3f / CPU copy p50 %.3f / upload p50 %.3f", down.Summarize().P50, cpy.Summarize().P50, up.Summarize().P50)
	return r
}

func runXfer(args []string) error {
	fs := flag.NewFlagSet("xfer", flag.ExitOnError)
	from := fs.String("from", "NVIDIA", "source adapter")
	to := fs.String("to", "AMD,Intel", "destination adapters, comma separated")
	formats := fs.String("formats", "bgra,nv12,p010", "surface formats")
	frames := fs.Int("frames", 600, "frames per case")
	sizes := fs.String("sizes", "1920x1080,2560x1440", "sizes")
	fs.Parse(args)

	src, err := openDevice(*from)
	if err != nil {
		return err
	}
	defer src.Close()
	fmt.Println("| from → to | size | format | method | p50 | p95 | p99 | max | breakdown | allocs/frame |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|")
	print := func(path, size string, r xferResult) {
		if r.err != nil {
			fmt.Printf("| %s | %s | %s | %s | failed: %v |||||| \n", path, size, r.format, r.method, r.err)
			return
		}
		fmt.Printf("| %s | %s | %s | %s | %.3f | %.3f | %.3f | %.3f | %s | %.1f |\n", path, size, r.format, r.method,
			r.total.P50, r.total.P95, r.total.P99, r.total.Max, r.parts, r.allocs)
	}
	for _, size := range strings.Split(*sizes, ",") {
		var w, h int
		fmt.Sscanf(size, "%dx%d", &w, &h)
		for _, format := range strings.Split(*formats, ",") {
			print(src.info.Name+" (same GPU)", size, xferSameGPU(src, format, w, h, *frames))
			for _, name := range strings.Split(*to, ",") {
				dst, err := openDevice(name)
				if err != nil {
					return err
				}
				path := src.info.Name + " → " + dst.info.Name
				print(path, size, xferShared(src, dst, format, w, h, *frames, true))
				print(path, size, xferShared(src, dst, format, w, h, *frames, false))
				print(path, size, xferStaging(src, dst, format, w, h, *frames))
				dst.Close()
			}
		}
	}
	return nil
}
