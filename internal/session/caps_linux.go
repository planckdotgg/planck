//go:build linux && !android && (amd64 || arm64)

package session

import (
	"errors"
	"math"
	"slices"

	"github.com/planckdotgg/planck/pkg/capture/display"
	"github.com/planckdotgg/planck/pkg/decode/linuxdec"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/all"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// probeEncoders asks each GPU's encoder what it offers; one that fails
// its probe is left out. Capture is in memory or DMA-BUFs on Linux, so
// no GPU drives a display in the sense of owning the frames.
func probeEncoders(cfg HostConfig) []defaults.EncoderCap {
	var out []defaults.EncoderCap
	for _, d := range all.Devices() {
		if cfg.Adapter != "" && !matchesAdapter(d.Adapter, cfg.Adapter) {
			continue
		}
		if e, ok := probeDevice(d.Adapter); ok {
			out = append(out, e)
		}
	}
	return out
}

func probeDevice(a gpu.Adapter) (defaults.EncoderCap, bool) {
	e := defaults.EncoderCap{Adapter: a, Engines: 1}
	switch a.Vendor {
	case gpu.VendorNVIDIA:
		c, err := nvenc.Probe(a)
		if err != nil {
			return e, false
		}
		e.API, e.Engines, e.SubFrame = defaults.APINVENC, max(1, c.Engines), c.SubFrame
		for _, codec := range c.Codecs {
			ch := c.Chroma[codec]
			e.Codecs = append(e.Codecs, defaults.CodecCap{Codec: defaults.Codec(codec), MaxWidth: c.MaxWidth, MaxHeight: c.MaxHeight,
				TenBit: slices.Contains(ch, encode.P010), Chroma444: slices.Contains(ch, encode.YUV444)})
		}
	default:
		return e, false
	}
	slices.SortFunc(e.Codecs, func(x, y defaults.CodecCap) int { return int(x.Codec) - int(y.Codec) })
	return e, len(e.Codecs) > 0
}

func hostDisplays(cfg HostConfig) []defaults.HostDisplay {
	if cfg.Synth {
		d, err := encodeDevice(cfg.Adapter)
		if err != nil {
			return nil
		}
		return []defaults.HostDisplay{{ID: "synthetic", Width: cfg.Width, Height: cfg.Height, RefreshHz: max(cfg.FPS, 60), Adapter: d.Adapter.LUID, Primary: true}}
	}
	ds, err := display.List()
	if err != nil {
		return nil
	}
	var out []defaults.HostDisplay
	for _, d := range ds {
		out = append(out, defaults.HostDisplay{ID: d.ID, Name: d.Name, Width: d.Width, Height: d.Height,
			RefreshHz: int(math.Round(d.Refresh)), Primary: d.Primary})
	}
	return out
}

func vddCap() defaults.VDDCap { return defaults.VDDCap{} }

type vddManager struct{}

var errNoVDD = errors.New("session: virtual displays need SudoVDA on Windows")

func (*vddManager) create(VideoParams, []byte, bool) (VirtualDisplay, error) {
	return nil, errNoVDD
}
func (*vddManager) close() error { return nil }

// clientDisplay leaves the display to the window: the desktop client
// reads its monitor from GTK (cmd/planck-client), a headless one has none.
func clientDisplay() defaults.ClientDisplay { return defaults.ClientDisplay{} }

// clientDecoders lists VA-API's and NVDEC's codecs: HEVC on either, AV1 on
// NVDEC.
func clientDecoders() []defaults.CodecCap { return linuxdec.Codecs() }

const (
	clientPresent10Bit  = false
	clientPresentHDR10  = false
	clientDirectDisplay = false
)
