//go:build linux && !android && (amd64 || arm64)

package session

import (
	"fmt"
	"time"

	"github.com/planckdotgg/planck/internal/rtprio"
	"github.com/planckdotgg/planck/internal/synth"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/linuxdec"
	"github.com/planckdotgg/planck/pkg/encode/all"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/input"
)

// encodeDevice is the first GPU with an encoder whose name matches
// match ("" for any): NVENC's CUDA devices come before VA-API's render
// nodes.
func encodeDevice(match string) (gpu.Device, error) {
	devs := all.Devices()
	for _, d := range devs {
		if matchesAdapter(d.Adapter, match) {
			return d, nil
		}
	}
	if len(devs) == 0 {
		return gpu.Device{}, fmt.Errorf("session: no GPU encoder (NVENC or VA-API) on this machine")
	}
	return gpu.Device{}, fmt.Errorf("session: no GPU encoder matches %q", match)
}

// openDisplay captures with MIT-SHM under X11 or the desktop portal under
// Wayland. Frames are in memory or DMA-BUFs on no particular device, so
// the encoding GPU is whichever p.Adapter names.
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
	dev, err := encodeDevice(p.Adapter)
	if err != nil {
		return nil, err
	}
	refresh := int(d.Refresh + 0.5)
	switch {
	case p.FPS == 0:
		p.FPS = max(refresh, 60)
	case refresh > 0 && refresh < p.FPS:
		p.FPS = refresh
	}
	power := gpu.AcquireHighPerf(dev.Adapter)
	src, err := display.Open(d.ID, display.Options{FPS: p.FPS})
	if err != nil {
		power.Close()
		return nil, err
	}
	info := VideoInfo{Width: src.Display.Width, Height: src.Display.Height, FPS: p.FPS, Codec: p.Codec, Chroma: p.Chroma,
		Adapter: dev.Adapter, Power: power.Mode, Source: "display " + src.Display.ID}
	info.Display, info.Desktop = inputArea(src.Display, all)
	return &sourcePipe{src: src, devices: []gpu.Device{dev}, params: p, power: power, info: info}, nil
}

// openSynth paces pattern frames from memory, as X11 capture hands them
// on, into the encoder of p.Adapter's GPU.
func openSynth(p VideoParams) (VideoPipe, error) {
	dev, err := encodeDevice(p.Adapter)
	if err != nil {
		return nil, err
	}
	w, h := p.Width&^1, p.Height&^1
	bufs := make([]*capture.DMABuf, synthFrameCount)
	var frames []capture.Frame
	for i := range bufs {
		b := make([]byte, w*h*4)
		synth.BGRA(b, w, h, i*8)
		bufs[i] = capture.MemoryDMABuf(capture.FourccXRGB8888, w, h, [][]byte{b}, []int{w * 4})
		frames = append(frames, capture.Frame{Surface: bufs[i].Handle(), Format: capture.FormatBGRA8, Width: w, Height: h})
	}
	power := gpu.AcquireHighPerf(dev.Adapter)
	// The pattern keeps bufs, which frames point into, until it closes.
	release := func() { clear(bufs) }
	return &sourcePipe{
		src: newSynthPattern(frames, p.FPS, release), devices: []gpu.Device{dev}, params: p, power: power,
		info: VideoInfo{Width: w, Height: h, FPS: p.FPS, Codec: p.Codec, Chroma: p.Chroma, Adapter: dev.Adapter, Power: power.Mode, Source: "synthetic"},
	}, nil
}

func newAudioCapturer(opts audio.CaptureOptions) (audio.Capturer, error) {
	return audio.NewCapturer(opts)
}

func newInjector() (input.Injector, error) {
	inj, err := input.NewInjector()
	if err != nil {
		return nil, err
	}
	return linuxPadInjector{inj}, nil
}

// linuxPadInjector gives a host its injector's uinput controllers.
type linuxPadInjector struct{ *input.LinuxInjector }

var (
	_ PadInjector    = linuxPadInjector{}
	_ input.Targeter = linuxPadInjector{}
)

func (l linuxPadInjector) VirtualPads() (VirtualPadSlots, error) {
	pads, err := l.Gamepads()
	if err != nil {
		return nil, err
	}
	return pads, nil
}

func newAudioPlayer() (audio.Player, error) { return audio.NewPlayer(audio.PlayerConfig{}) }

func playerQueued(p audio.Player) time.Duration {
	if d, ok := p.(*audio.DevicePlayer); ok {
		return d.Stats().Queued
	}
	return 0
}

// OpenDecodeSink decodes with VA-API or NVDEC and hands each picture
// straight back. A VA-API picture stays in its surface: the sink takes
// any DMA-BUF, so nothing is copied.
func OpenDecodeSink(cfg decode.Config) (Sink, error) {
	d, err := linuxdec.Open(decode.Target{Accepts: func(uint32, uint64) bool { return true }}, cfg)
	if err != nil {
		return nil, err
	}
	return &decodeSink{dec: d}, nil
}

// boostVideoThread raises the video thread to SCHED_FIFO where the
// process may, or else to a lower nice value, itself or through
// RealtimeKit. rtprio.Boost's LockOSThread outlives the video loop's, so
// the thread ends with the loop and its class reaches no other goroutine.
func boostVideoThread() (func(), error) { return func() {}, rtprio.Boost() }
