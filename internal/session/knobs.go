package session

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// Knobs are the stream override flags planck-host and planck-client share,
// as typed. An empty field was not given.
type Knobs struct {
	Codec, Chroma, BitDepth, Resolution, FPS, Slices string
	VDD, VDDGPU, Split, Direct, VSync, Route         string
	PhaseLock                                        string
	// Display picks a host display by ID, and HDR10 is auto or off.
	Display, HDR10 string
	BitrateMbps    int
}

// Overrides turns the knobs into overrides; "auto" leaves a knob to the
// resolver. Chroma also takes the encoder names nv12, p010, yuv444 and
// yuv444p10, which set the bit depth too unless BitDepth is given.
func (k Knobs) Overrides() (defaults.UserOverrides, error) {
	var o defaults.UserOverrides
	lower := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	switch c := lower(k.Codec); c {
	case "", defaults.Auto:
	case "h265":
		o.Codec = "hevc"
	case "avc":
		o.Codec = "h264"
	default:
		o.Codec = defaults.CodecPref(c)
	}
	depth := ""
	switch c := lower(k.Chroma); c {
	case "", defaults.Auto:
	case "420", "nv12":
		o.Chroma, depth = "420", "8"
	case "p010":
		o.Chroma, depth = "420", "10"
	case "444", "yuv444":
		o.Chroma, depth = "444", "8"
	case "yuv444p10":
		o.Chroma, depth = "444", "10"
	default:
		return o, fmt.Errorf("chroma %q: want auto, 420 or 444", k.Chroma)
	}
	if c := lower(k.Chroma); c == "420" || c == "444" {
		depth = ""
	}
	switch b := lower(k.BitDepth); b {
	case "", defaults.Auto:
		o.BitDepth = defaults.BitDepthPref(depth)
	default:
		o.BitDepth = defaults.BitDepthPref(b)
	}
	switch r := lower(k.Resolution); r {
	case "", defaults.Auto:
	case "2160p", "4k":
		o.Resolution = "4k"
	case "1080p", "1440p":
		o.Resolution = defaults.ResolutionPref(r)
	default:
		w, h, ok := strings.Cut(r, "x")
		wi, err1 := strconv.Atoi(w)
		hi, err2 := strconv.Atoi(h)
		if !ok || err1 != nil || err2 != nil {
			return o, fmt.Errorf("resolution %q: want auto, 1080p, 1440p, 4k or WxH", k.Resolution)
		}
		o.Resolution, o.CustomWidth, o.CustomHeight = "custom", wi, hi
	}
	if f := lower(k.FPS); f != "" && f != defaults.Auto {
		o.FPS = defaults.FPSPref(f)
	}
	if s := lower(k.Slices); s != "" && s != defaults.Auto {
		o.SubFrameSlices = defaults.SlicesPref(s)
	}
	o.BitrateMbps = k.BitrateMbps
	o.VirtualDisplay = defaults.Toggle(lower(k.VDD))
	o.VirtualDisplayGPU = strings.TrimSpace(k.VDDGPU)
	o.NVENC2EngineSplit = defaults.Toggle(lower(k.Split))
	o.MetalDirectDisplay = defaults.Toggle(lower(k.Direct))
	o.VSyncMode = defaults.Toggle(lower(k.VSync))
	o.PhaseLock = defaults.Toggle(lower(k.PhaseLock))
	o.RoutePolicy = defaults.RoutePref(lower(k.Route))
	o.Display = strings.TrimSpace(k.Display)
	o.HDR10 = defaults.Toggle(lower(k.HDR10))
	o = o.Normalize()
	return o, o.Validate()
}
