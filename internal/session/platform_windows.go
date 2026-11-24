//go:build windows

package session

import (
	"fmt"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/d3d11"
	"github.com/planckdotgg/planck/pkg/encode"
	_ "github.com/planckdotgg/planck/pkg/encode/all"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/input"
)

func openDisplay(p VideoParams) (VideoPipe, error) {
	all, err := display.List()
	if err != nil {
		return nil, err
	}
	var d capture.Display
	for _, x := range all {
		if x.ID == p.Display || p.Display == "" && x.Primary {
			d = x
		}
	}
	if d.ID == "" {
		return nil, fmt.Errorf("session: no display %q", p.Display)
	}
	if !matchesAdapter(d.Adapter, p.Adapter) {
		return nil, errCrossAdapter
	}
	// The power policy must be in place before the D3D11 device exists.
	power := gpu.AcquireHighPerf(d.Adapter)
	// 10-bit and FP16 surfaces come from DuplicateOutput1, which answers
	// DXGI_ERROR_UNSUPPORTED on an SDR desktop (a SudoVDA display with
	// advanced color off); an SDR desktop is 8-bit, and the encoder
	// raises it to the coded depth.
	src, err := display.Open(d.ID, display.Options{HDR: p.Chroma.TenBit() && d.HDR})
	if err != nil {
		power.Close()
		return nil, err
	}
	fps := p.FPS
	refresh := int(src.Display.Refresh + 0.5)
	switch {
	case fps == 0:
		fps = max(refresh, 60)
	case refresh > 0 && refresh < fps:
		// The display changes at most refresh times a second: rate control
		// and the one-frame VBV are sized for the frames that come.
		fps = refresh
	}
	p.FPS = fps
	prio := win.Device{Object: win.ObjectAt(src.Device.Handle)}.RaiseGPUThreadPriority()
	return &sourcePipe{
		src: src, devices: []gpu.Device{src.Device}, params: p, power: power,
		info: VideoInfo{Width: src.Display.Width, Height: src.Display.Height, FPS: fps, Codec: p.Codec, Chroma: p.Chroma,
			Adapter: src.Device.Adapter, Power: power.Mode, GPUPriority: prio, Source: "display " + d.ID},
	}, nil
}

func pickAdapter(match string) (gpu.Adapter, error) {
	all, err := dxgi.Adapters()
	if err != nil {
		return gpu.Adapter{}, err
	}
	for _, a := range all {
		if matchesAdapter(a, match) {
			return a, nil
		}
	}
	return gpu.Adapter{}, fmt.Errorf("session: no GPU matches %q", match)
}

func openSynth(p VideoParams) (VideoPipe, error) {
	a, err := pickAdapter(p.Adapter)
	if err != nil {
		return nil, err
	}
	power := gpu.AcquireHighPerf(a)
	dev, err := dxgi.OpenDevice(a.LUID)
	if err != nil {
		power.Close()
		return nil, err
	}
	w, h := p.Width&^1, p.Height&^1
	format, dxf, fill := capture.FormatBGRA8, uint32(win.DXGI_FORMAT_B8G8R8A8_UNORM), synth.BGRA
	if p.Chroma.TenBit() {
		format, dxf, fill = capture.FormatRGB10A2, win.DXGI_FORMAT_R10G10B10A2_UNORM, synth.RGB10A2
	}
	buf := make([]byte, w*h*4)
	var textures []win.Texture
	release := func() {
		for _, t := range textures {
			t.Release()
		}
		dev.Close()
	}
	var frames []capture.Frame
	for i := 0; i < synthFrameCount; i++ {
		fill(buf, w, h, i*8)
		desc := win.Texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: dxf,
			SampleCount: 1, Usage: win.D3D11_USAGE_DEFAULT, BindFlags: win.D3D11_BIND_RENDER_TARGET | win.D3D11_BIND_SHADER_RESOURCE}
		tex, err := dev.D3D.CreateTexture2DInit(&desc, buf, uint32(w*4))
		if err != nil {
			release()
			power.Close()
			return nil, fmt.Errorf("session: synthetic %dx%d texture on %s: %w", w, h, a.Name, err)
		}
		textures = append(textures, tex)
		frames = append(frames, capture.Frame{Surface: uintptr(unsafe.Pointer(tex.Object)), Adapter: a.LUID, Format: format, Width: w, Height: h})
	}
	device := gpu.Device{Adapter: dev.Info, Handle: uintptr(unsafe.Pointer(dev.D3D.Object))}
	prio := dev.D3D.RaiseGPUThreadPriority()
	return &sourcePipe{
		src: newSynthPattern(frames, p.FPS, release), devices: []gpu.Device{device}, params: p, power: power,
		info: VideoInfo{Width: w, Height: h, FPS: p.FPS, Codec: p.Codec, Chroma: p.Chroma, Adapter: a, Power: power.Mode, GPUPriority: prio, Source: "synthetic"},
	}, nil
}

func newAudioCapturer() (audio.Capturer, error) { return audio.NewCapturer() }

func newInjector() (input.Injector, error) { return input.NewInjector() }

func newAudioPlayer() (audio.Player, error) { return audio.NewPlayer(audio.PlayerConfig{}) }

func playerQueued(p audio.Player) time.Duration {
	if d, ok := p.(*audio.DevicePlayer); ok {
		return d.Stats().Queued
	}
	return 0
}

// OpenDecodeSink decodes on the default adapter with Media Foundation and
// hands each picture straight back: no pixel is copied.
func OpenDecodeSink(cfg decode.Config) (Sink, error) {
	all, err := dxgi.Adapters()
	if err != nil || len(all) == 0 {
		return nil, fmt.Errorf("session: no GPU to decode on: %v", err)
	}
	dev, err := dxgi.OpenDevice(all[0].LUID)
	if err != nil {
		return nil, err
	}
	var d decode.Decoder
	if cfg.Codec == encode.PyroWave {
		d, err = decode.Open(decode.Target{Device: uintptr(unsafe.Pointer(dev.D3D.Object)), Adapter: dev.Info}, cfg)
	} else {
		d, err = d3d11.Open(gpu.Device{Adapter: dev.Info, Handle: uintptr(unsafe.Pointer(dev.D3D.Object))}, cfg)
	}
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &decodeSink{dec: d, after: dev.Close}, nil
}

// boostVideoThread puts the locked video thread in MMCSS's "Games" task.
// Measured at 1080p240 on NVENC: paced encode p99 4.4-7 ms and max 17-30 ms
// at normal priority, p99 1.6 ms and max 2.3 ms with it.
func boostVideoThread() (func(), error) { return win.BoostThread("Games") }
