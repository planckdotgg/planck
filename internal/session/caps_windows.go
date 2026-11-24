//go:build windows

package session

import (
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/decode/d3d11"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/amf"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
	"github.com/planckdotgg/planck/pkg/encode/vpl"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/vdd"
)

// pyroPixelRate is PyroWave's real-time pixel rate per vendor, from the WS4
// 1080p back-to-back encode p50 on bigboi (RTX 4080 0.70 ms, RX 6700 XT
// 0.79 ms, UHD 770 9.35 ms), halved for headroom under a game's load,
// which was not measured.
func pyroPixelRate(v gpu.Vendor) int64 {
	switch v {
	case gpu.VendorNVIDIA:
		return 1.4e9
	case gpu.VendorAMD:
		return 1.3e9
	}
	return 1.0e8
}

// probeEncoders asks each adapter's encoder what it offers. A backend that
// fails its probe is left out; PyroWave is listed on every adapter with a
// hardware encoder, as its compute shaders run on any of them.
func probeEncoders(cfg HostConfig) []defaults.EncoderCap {
	adapters, err := dxgi.Adapters()
	if err != nil {
		return nil
	}
	displays, _ := dxgi.Displays()
	virtual := map[string]bool{}
	if ms, err := vdd.Monitors(); err == nil {
		for _, m := range ms {
			virtual[m.GDIName] = true
		}
	}
	var out []defaults.EncoderCap
	// DXGI lists an adapter once per display path, under other LUIDs.
	seen := map[string]bool{}
	for _, a := range adapters {
		if seen[a.Name] || a.Vendor == gpu.VendorUnknown {
			continue
		}
		seen[a.Name] = true
		if cfg.Adapter != "" && !matchesAdapter(a, cfg.Adapter) {
			continue
		}
		drives := slices.ContainsFunc(displays, func(d capture.Display) bool { return d.Adapter.LUID == a.LUID && !virtual[d.ID] })
		e, err := probeAdapter(a)
		if err != nil {
			continue
		}
		e.DrivesDisplay = drives
		out = append(out, e)
		if _, ok := encode.BackendFor(a.Vendor); ok {
			out = append(out, defaults.EncoderCap{Adapter: a, API: defaults.APIPyroWave, Engines: 1, DrivesDisplay: drives,
				MaxPixelRate: pyroPixelRate(a.Vendor),
				Codecs:       []defaults.CodecCap{{Codec: defaults.CodecPyroWave, TenBit: true, Chroma444: true}}})
		}
	}
	return out
}

func probeAdapter(a gpu.Adapter) (defaults.EncoderCap, error) {
	e := defaults.EncoderCap{Adapter: a, Engines: 1}
	switch a.Vendor {
	case gpu.VendorNVIDIA:
		dev, err := dxgi.OpenDevice(a.LUID)
		if err != nil {
			return e, err
		}
		defer dev.Close()
		c, err := nvenc.Probe(dev.D3D, a)
		if err != nil {
			return e, err
		}
		e.API, e.Engines, e.SubFrame = defaults.APINVENC, max(1, c.Engines), c.SubFrame
		for _, codec := range c.Codecs {
			ch := c.Chroma[codec]
			e.Codecs = append(e.Codecs, defaults.CodecCap{Codec: defaults.Codec(codec), MaxWidth: c.MaxWidth, MaxHeight: c.MaxHeight,
				TenBit: slices.Contains(ch, encode.P010), Chroma444: slices.Contains(ch, encode.YUV444)})
		}
	case gpu.VendorAMD:
		dev, err := dxgi.OpenDevice(a.LUID)
		if err != nil {
			return e, err
		}
		defer dev.Close()
		caps, err := amf.Probe(dev.D3D)
		if err != nil {
			return e, err
		}
		e.API = defaults.APIAMF
		for _, c := range caps {
			if c.Err == nil {
				e.Codecs = append(e.Codecs, defaults.CodecCap{Codec: defaults.Codec(c.Codec), TenBit: c.Codec != encode.H264})
			}
		}
	case gpu.VendorIntel:
		impls, err := vpl.Probe()
		if err != nil {
			return e, err
		}
		e.API = defaults.APIOneVPL
		for _, im := range impls {
			if im.VendorID != uint32(gpu.VendorIntel) {
				continue
			}
			for _, c := range im.Encoders {
				cc := defaults.CodecCap{TenBit: true}
				switch c.FourCC {
				case "AVC ":
					cc.Codec, cc.TenBit = defaults.CodecH264, false
				case "HEVC":
					cc.Codec, cc.Chroma444 = defaults.CodecHEVC, true
				case "AV1 ":
					cc.Codec = defaults.CodecAV1
				default:
					continue
				}
				if !slices.ContainsFunc(e.Codecs, func(x defaults.CodecCap) bool { return x.Codec == cc.Codec }) {
					e.Codecs = append(e.Codecs, cc)
				}
			}
			break
		}
	default:
		return e, fmt.Errorf("no encoder for %s", a.Vendor)
	}
	if len(e.Codecs) == 0 {
		return e, fmt.Errorf("%s: no codec", a.Name)
	}
	slices.SortFunc(e.Codecs, func(x, y defaults.CodecCap) int { return int(x.Codec) - int(y.Codec) })
	return e, nil
}

func hostDisplays(cfg HostConfig) []defaults.HostDisplay {
	if cfg.Synth {
		a, err := pickAdapter(cfg.Adapter)
		if err != nil {
			return nil
		}
		return []defaults.HostDisplay{{ID: "synthetic", Width: cfg.Width, Height: cfg.Height, RefreshHz: max(cfg.FPS, 60), Adapter: a.LUID, Primary: true}}
	}
	all, err := dxgi.Displays()
	if err != nil {
		return nil
	}
	var out []defaults.HostDisplay
	for _, d := range all {
		out = append(out, defaults.HostDisplay{ID: d.ID, Width: d.Width, Height: d.Height,
			RefreshHz: int(math.Round(d.Refresh)), Adapter: d.Adapter.LUID, Primary: d.Primary})
	}
	return out
}

func vddCap() defaults.VDDCap {
	info, err := vdd.DriverInfo()
	if err != nil {
		return defaults.VDDCap{RegistryGPU: info.RegistryGPUName}
	}
	// The driver's SDDL lets every user send IOCTL_SET_RENDER_ADAPTER, and
	// an unelevated open proved it on bigboi; a denial shows up as a
	// failed CreateDisplay, which falls back to the physical display.
	return defaults.VDDCap{Available: true, Protocol: info.Protocol.String(), CanSetRenderAdapter: true, RegistryGPU: info.RegistryGPUName}
}

// vddManager owns the host's SudoVDA handle. The driver pings while a
// display is up; Close removes every display it made.
type vddManager struct {
	mu  sync.Mutex
	drv *vdd.Driver
}

// create makes a virtual display for p on p.Adapter's GPU and returns its
// GDI name and the function that removes it.
func (m *vddManager) create(p VideoParams, seed []byte, primary bool) (string, func() error, error) {
	a, err := pickAdapter(p.Adapter)
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drv == nil {
		if m.drv, err = vdd.Open(); err != nil {
			m.drv = nil
			return "", nil, err
		}
	}
	vd, err := m.drv.CreateDisplay(vdd.DisplaySpec{Width: p.Width &^ 1, Height: p.Height &^ 1, RefreshMilliHz: p.FPS * 1000,
		ID: vdd.MonitorID(seed), RenderAdapter: a.LUID, Primary: primary})
	if err != nil {
		return "", nil, err
	}
	return vd.GDIName, vd.Close, nil
}

func (m *vddManager) close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drv == nil {
		return nil
	}
	err := m.drv.Close()
	m.drv = nil
	return err
}

func clientDisplay() defaults.ClientDisplay {
	all, err := dxgi.Displays()
	if err != nil {
		return defaults.ClientDisplay{}
	}
	for _, d := range all {
		if d.Primary {
			hz := int(math.Round(d.Refresh))
			return defaults.ClientDisplay{WidthPx: d.Width, HeightPx: d.Height, Scale: 1, MaxHz: hz, MinHz: hz, HDR: d.HDR}
		}
	}
	return defaults.ClientDisplay{}
}

// clientDecoders lists the Media Foundation decoders installed, at 10 bits
// and 4:2:0, and the PyroWave compute decoder (8-bit output: the surface
// presents BGRA8).
func clientDecoders() []defaults.CodecCap {
	var out []defaults.CodecCap
	for _, c := range []encode.Codec{encode.HEVC, encode.AV1} {
		if names, err := d3d11.Decoders(c); err == nil && len(names) > 0 {
			out = append(out, defaults.CodecCap{Codec: defaults.Codec(c), TenBit: true})
		}
	}
	return append(out, defaults.CodecCap{Codec: defaults.CodecPyroWave, Chroma444: true})
}

const (
	clientPresent10Bit  = false
	clientDirectDisplay = false
)
