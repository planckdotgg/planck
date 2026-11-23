//go:build windows

package main

import (
	"fmt"
	"strings"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/gpu"
)

type device struct {
	info    gpu.Adapter
	adapter win.Adapter
	dev     win.Device
	ctx     win.Context
	idle    win.Query
}

// openDevice picks the first hardware adapter whose name contains match
// (case-insensitive) and makes a D3D11 device on it.
func openDevice(match string) (*device, error) {
	f, err := win.CreateFactory()
	if err != nil {
		return nil, err
	}
	defer f.Release()
	adapters, err := f.Adapters()
	if err != nil {
		return nil, err
	}
	var found *device
	for _, a := range adapters {
		info, err := a.Info()
		if err != nil || found != nil || info.Software || !strings.Contains(strings.ToLower(info.Name), strings.ToLower(match)) {
			a.Release()
			continue
		}
		dev, ctx, err := win.CreateDevice(a)
		if err != nil {
			a.Release()
			return nil, fmt.Errorf("D3D11CreateDevice on %s: %w", info.Name, err)
		}
		q, err := dev.CreateQuery(win.D3D11_QUERY_EVENT)
		if err != nil {
			return nil, err
		}
		found = &device{info: info, adapter: a, dev: dev, ctx: ctx, idle: q}
	}
	if found == nil {
		return nil, fmt.Errorf("no adapter matches %q", match)
	}
	return found, nil
}

func (d *device) Close() {
	d.idle.Release()
	d.ctx.Release()
	d.dev.Release()
	d.adapter.Release()
}

type inputFormat struct {
	capture capture.Format
	dxgi    uint32
	bytes   func(w, h int) int
	pitch   func(w int) uint32
	fill    func(dst []byte, w, h, i int)
}

var inputFormats = map[string]inputFormat{
	"bgra": {capture.FormatBGRA8, win.DXGI_FORMAT_B8G8R8A8_UNORM,
		func(w, h int) int { return w * h * 4 }, func(w int) uint32 { return uint32(w * 4) }, synth.BGRA},
	"rgb10": {capture.FormatRGB10A2, win.DXGI_FORMAT_R10G10B10A2_UNORM,
		func(w, h int) int { return w * h * 4 }, func(w int) uint32 { return uint32(w * 4) }, synth.RGB10A2},
	"nv12": {capture.FormatNV12, win.DXGI_FORMAT_NV12,
		func(w, h int) int { return w * h * 3 / 2 }, func(w int) uint32 { return uint32(w) }, synth.NV12},
	"p010": {capture.FormatP010, win.DXGI_FORMAT_P010,
		func(w, h int) int { return w * h * 3 }, func(w int) uint32 { return uint32(w * 2) }, synth.P010},
}

// syntheticFrames uploads n distinct frames as GPU textures on d.
func (d *device) syntheticFrames(format string, w, h, n int, misc uint32) ([]win.Texture, error) {
	f, ok := inputFormats[format]
	if !ok {
		return nil, fmt.Errorf("unknown input format %q", format)
	}
	buf := make([]byte, f.bytes(w, h))
	out := make([]win.Texture, 0, n)
	for i := 0; i < n; i++ {
		f.fill(buf, w, h, i)
		desc := win.Texture2DDesc{
			Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: f.dxgi,
			SampleCount: 1, Usage: win.D3D11_USAGE_DEFAULT, BindFlags: win.D3D11_BIND_RENDER_TARGET, MiscFlags: misc,
		}
		t, err := d.dev.CreateTexture2DInit(&desc, buf, f.pitch(w))
		if err != nil {
			return nil, fmt.Errorf("texture %s %dx%d: %w", format, w, h, err)
		}
		out = append(out, t)
	}
	return out, nil
}
