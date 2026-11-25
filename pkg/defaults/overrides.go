package defaults

import (
	"fmt"
	"slices"
)

// Every knob is a string enum; "" and "auto" both mean auto.
type (
	CodecPref      string // auto | pyrowave | hevc | av1 | h264
	ChromaPref     string // auto | 420 | 444
	BitDepthPref   string // auto | 8 | 10
	ResolutionPref string // auto | 1080p | 1440p | 4k | custom
	FPSPref        string // auto | 60 | 120 | 144 | 240
	SlicesPref     string // auto | 1 | 4 | 8
	Toggle         string // auto | on | off
	RoutePref      string // auto | lan | tailscale
)

const Auto = "auto"

const (
	ToggleOn  Toggle = "on"
	ToggleOff Toggle = "off"
)

var (
	codecPrefs      = []string{"pyrowave", "hevc", "av1", "h264"}
	chromaPrefs     = []string{"420", "444"}
	bitDepthPrefs   = []string{"8", "10"}
	resolutionPrefs = []string{"1080p", "1440p", "4k", "custom"}
	fpsPrefs        = []string{"60", "120", "144", "240"}
	slicesPrefs     = []string{"1", "4", "8"}
	togglePrefs     = []string{"on", "off"}
	// hdrPrefs: HDR10 is never forced on; auto takes it where both ends
	// and the desktop have it.
	hdrPrefs   = []string{"off"}
	routePrefs = []string{"lan", "tailscale"}
)

const (
	MinCustomWidth  = 640
	MaxCustomWidth  = 7680
	MinCustomHeight = 360
	MaxCustomHeight = 4320
	MaxBitrateMbps  = 3000
)

type UserOverrides struct {
	Codec        CodecPref      `json:"codec,omitempty"`
	Chroma       ChromaPref     `json:"chroma,omitempty"`
	BitDepth     BitDepthPref   `json:"bitDepth,omitempty"`
	Resolution   ResolutionPref `json:"resolution,omitempty"`
	CustomWidth  int            `json:"customWidth,omitempty"`  // with Resolution "custom": even, 640..7680
	CustomHeight int            `json:"customHeight,omitempty"` // even, 360..4320
	FPS          FPSPref        `json:"fps,omitempty"`
	BitrateMbps  int            `json:"bitrateMbps,omitempty"` // 0 = auto, else 1..3000

	VirtualDisplay Toggle `json:"virtualDisplay,omitempty"`
	// VirtualDisplayGPU picks the GPU that renders the virtual display and
	// encodes (they must match for zero copy): a substring of the adapter
	// name, matched case-insensitively ("4080", "Radeon"). "" = auto. A LUID
	// is not stored because it changes on every boot.
	VirtualDisplayGPU  string     `json:"virtualDisplayGPU,omitempty"`
	SubFrameSlices     SlicesPref `json:"subFrameSlices,omitempty"`
	NVENC2EngineSplit  Toggle     `json:"nvenc2EngineSplit,omitempty"`
	MetalDirectDisplay Toggle     `json:"metalDirectDisplay,omitempty"`
	VSyncMode          Toggle     `json:"vsyncMode,omitempty"` // auto = off
	RoutePolicy        RoutePref  `json:"routePolicy,omitempty"`
	// Display is the host display to capture, by HostDisplay.ID; "" lets
	// the host choose. Picking one streams that physical display, not a
	// virtual one.
	Display string `json:"display,omitempty"`
	HDR10   Toggle `json:"hdr10,omitempty"` // auto | off
}

// MaxDisplayIDLen bounds UserOverrides.Display.
const MaxDisplayIDLen = 256

func isAuto(s string) bool { return s == "" || s == Auto }

func checkEnum(knob, v string, allowed []string) error {
	if isAuto(v) || slices.Contains(allowed, v) {
		return nil
	}
	return fmt.Errorf("defaults: %s %q: want auto or one of %v", knob, v, allowed)
}

// ViewSize is the virtual display size for a w x h pixel stream window:
// even, and within the custom size bounds.
func ViewSize(w, h int) (int, int) {
	return min(max(w, MinCustomWidth), MaxCustomWidth) &^ 1, min(max(h, MinCustomHeight), MaxCustomHeight) &^ 1
}

func validCustomSize(w, h int) bool {
	return w%2 == 0 && h%2 == 0 && w >= MinCustomWidth && w <= MaxCustomWidth && h >= MinCustomHeight && h <= MaxCustomHeight
}

func (o UserOverrides) Validate() error {
	for _, c := range []struct {
		knob, v string
		allowed []string
	}{
		{"codec", string(o.Codec), codecPrefs},
		{"chroma", string(o.Chroma), chromaPrefs},
		{"bitDepth", string(o.BitDepth), bitDepthPrefs},
		{"resolution", string(o.Resolution), resolutionPrefs},
		{"fps", string(o.FPS), fpsPrefs},
		{"virtualDisplay", string(o.VirtualDisplay), togglePrefs},
		{"subFrameSlices", string(o.SubFrameSlices), slicesPrefs},
		{"nvenc2EngineSplit", string(o.NVENC2EngineSplit), togglePrefs},
		{"metalDirectDisplay", string(o.MetalDirectDisplay), togglePrefs},
		{"vsyncMode", string(o.VSyncMode), togglePrefs},
		{"routePolicy", string(o.RoutePolicy), routePrefs},
		{"hdr10", string(o.HDR10), hdrPrefs},
	} {
		if err := checkEnum(c.knob, c.v, c.allowed); err != nil {
			return err
		}
	}
	if o.Resolution == "custom" && !validCustomSize(o.CustomWidth, o.CustomHeight) {
		return fmt.Errorf("defaults: custom resolution %dx%d: want even sizes, width %d..%d, height %d..%d",
			o.CustomWidth, o.CustomHeight, MinCustomWidth, MaxCustomWidth, MinCustomHeight, MaxCustomHeight)
	}
	if len(o.Display) > MaxDisplayIDLen {
		return fmt.Errorf("defaults: display ID is %d bytes, at most %d", len(o.Display), MaxDisplayIDLen)
	}
	if o.BitrateMbps < 0 || o.BitrateMbps > MaxBitrateMbps {
		return fmt.Errorf("defaults: bitrateMbps %d: want 0 (auto) or 1..%d", o.BitrateMbps, MaxBitrateMbps)
	}
	return nil
}

// Normalize turns every "" knob into "auto".
func (o UserOverrides) Normalize() UserOverrides {
	norm := func(s *string) {
		if *s == "" {
			*s = Auto
		}
	}
	norm((*string)(&o.Codec))
	norm((*string)(&o.Chroma))
	norm((*string)(&o.BitDepth))
	norm((*string)(&o.Resolution))
	norm((*string)(&o.FPS))
	norm((*string)(&o.VirtualDisplay))
	norm((*string)(&o.SubFrameSlices))
	norm((*string)(&o.NVENC2EngineSplit))
	norm((*string)(&o.MetalDirectDisplay))
	norm((*string)(&o.VSyncMode))
	norm((*string)(&o.RoutePolicy))
	norm((*string)(&o.HDR10))
	return o
}

// Merge returns base with every non-auto field of top replacing base's.
// CustomWidth and CustomHeight travel with Resolution.
func Merge(base, top UserOverrides) UserOverrides {
	pick := func(b, t *string) {
		if !isAuto(*t) {
			*b = *t
		}
	}
	pick((*string)(&base.Codec), (*string)(&top.Codec))
	pick((*string)(&base.Chroma), (*string)(&top.Chroma))
	pick((*string)(&base.BitDepth), (*string)(&top.BitDepth))
	if !isAuto(string(top.Resolution)) {
		base.Resolution, base.CustomWidth, base.CustomHeight = top.Resolution, top.CustomWidth, top.CustomHeight
	}
	pick((*string)(&base.FPS), (*string)(&top.FPS))
	if top.BitrateMbps != 0 {
		base.BitrateMbps = top.BitrateMbps
	}
	pick((*string)(&base.VirtualDisplay), (*string)(&top.VirtualDisplay))
	pick(&base.VirtualDisplayGPU, &top.VirtualDisplayGPU)
	pick((*string)(&base.SubFrameSlices), (*string)(&top.SubFrameSlices))
	pick((*string)(&base.NVENC2EngineSplit), (*string)(&top.NVENC2EngineSplit))
	pick((*string)(&base.MetalDirectDisplay), (*string)(&top.MetalDirectDisplay))
	pick((*string)(&base.VSyncMode), (*string)(&top.VSyncMode))
	pick((*string)(&base.RoutePolicy), (*string)(&top.RoutePolicy))
	pick(&base.Display, &top.Display)
	pick((*string)(&base.HDR10), (*string)(&top.HDR10))
	return base
}
