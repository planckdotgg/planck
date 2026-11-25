package ui

import (
	"fmt"
	"strconv"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
)

// knob is one stream override: its row, its choices, auto first, and the
// UserOverrides field it sets.
type knob struct {
	id, title, desc string
	values, labels  []string
	field           func(o *defaults.UserOverrides) *string
}

var (
	knobCodec = knob{id: "codec", title: "Codec", desc: "Auto picks for the GPUs and the route. PyroWave is for wired LANs.",
		values: []string{"auto", "pyrowave", "hevc", "av1", "h264"}, labels: []string{"Auto", "PyroWave", "HEVC", "AV1", "H.264"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.Codec) }}
	knobChroma = knob{id: "chroma", title: "Chroma", desc: "4:4:4 keeps text and UI sharp. NVENC and oneVPL encode it; AMF and Apple Silicon encode 4:2:0.",
		values: []string{"auto", "420", "444"}, labels: []string{"Auto", "4:2:0", "4:4:4"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.Chroma) }}
	knobBitDepth = knob{id: "bitDepth", title: "Bit depth", desc: "10-bit removes banding in gradients.",
		values: []string{"auto", "8", "10"}, labels: []string{"Auto", "8-bit", "10-bit"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.BitDepth) }}
	knobResolution = knob{id: "resolution", title: "Resolution", desc: "Auto streams the host's screen, or this screen's size on a virtual display. Other sizes need one.",
		values: []string{"auto", "1080p", "1440p", "4k", "custom"}, labels: []string{"Auto", "1080p", "1440p", "4K", "Custom"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.Resolution) }}
	knobFPS = knob{id: "fps", title: "Frame rate", desc: "Auto follows the refresh rate of this screen and the host's.",
		values: []string{"auto", "60", "120", "144", "240"}, labels: []string{"Auto", "60", "120", "144", "240"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.FPS) }}
	knobVDD = knob{id: "virtualDisplay", title: "Virtual display", desc: "A SudoVDA display on a Windows host, at the stream's size.",
		values: []string{"auto", "on", "off"}, labels: []string{"Auto", "On", "Off"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.VirtualDisplay) }}
	knobSlices = knob{id: "subFrameSlices", title: "Sub-frame slices", desc: "Encode each frame in parts so the first part leaves sooner.",
		values: []string{"auto", "1", "4", "8"}, labels: []string{"Auto", "1", "4", "8"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.SubFrameSlices) }}
	knobSplit = knob{id: "nvenc2EngineSplit", title: "NVENC two-engine split", desc: "Split each frame across two NVENC engines, for 1440p and up or high frame rates.",
		values: []string{"auto", "on", "off"}, labels: []string{"Auto", "On", "Off"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.NVENC2EngineSplit) }}
	knobDirect = knob{id: "metalDirectDisplay", title: "Direct to display", desc: "In full screen on macOS, the video skips the compositor while the stream controls are closed.",
		values: []string{"auto", "on", "off"}, labels: []string{"Auto", "On", "Off"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.MetalDirectDisplay) }}
	knobVSync = knob{id: "vsyncMode", title: "VSync", desc: "Off shows each frame at once and may tear. Auto is off.",
		values: []string{"auto", "off", "on"}, labels: []string{"Auto", "Off", "On"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.VSyncMode) }}
	knobHDR = knob{id: "hdr10", title: "HDR10 output", desc: "Auto streams 10-bit BT.2020 PQ when the host desktop and this display are both in HDR.",
		values: []string{"auto", "off"}, labels: []string{"Auto", "Off"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.HDR10) }}
	knobRoute = knob{id: "routePolicy", title: "Route", desc: "Auto uses the LAN when it reaches the host, and Tailscale when not.",
		values: []string{"auto", "lan", "tailscale"}, labels: []string{"Auto", "LAN", "Tailscale"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.RoutePolicy) }}

	allKnobs = []knob{knobCodec, knobChroma, knobBitDepth, knobResolution, knobFPS, knobVDD, knobSlices, knobSplit, knobDirect, knobVSync, knobHDR, knobRoute}
)

const (
	// manualBitrateMbps is where Manual starts; the slider spans
	// minBitrateMbps to maxManualBitrateMbps.
	manualBitrateMbps    = 100
	maxManualBitrateMbps = 1000
	// customWidth and customHeight are where Custom starts.
	customWidth, customHeight = 1920, 1080
)

// overrideScope is what the overrides being edited apply to: every host,
// or one.
type overrideScope struct {
	hostID, name string
}

func (s overrideScope) all() bool { return s.hostID == "" }

// overrideEdit is a frame's edit of one scope's overrides. base is what
// the scope's auto knobs follow: nil for every host.
type overrideEdit struct {
	scope  overrideScope
	before defaults.UserOverrides
	o      defaults.UserOverrides
	base   *defaults.UserOverrides
	// policy is what the scope's host forces, while it streams: a knob it
	// sets shows as Set by host.
	policy *defaults.UserOverrides
}

func (a *App) editOverrides(scope overrideScope) *overrideEdit {
	e := &overrideEdit{scope: scope, before: a.backend.Overrides(scope.hostID).Normalize()}
	e.o = e.before
	if !scope.all() {
		g := a.backend.Overrides("").Normalize()
		e.base = &g
	}
	return e
}

// save stores the edit if a control changed it, keeping the error for
// the top of Settings.
func (a *App) saveOverrides(e *overrideEdit) {
	if e.o == e.before {
		return
	}
	msg := ""
	if err := a.backend.SaveOverrides(e.scope.hostID, e.o); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	a.settingsErr = msg
	a.mu.Unlock()
}

// resetToAuto makes every knob of the edit's scope auto.
func (e *overrideEdit) resetToAuto() { e.o = defaults.UserOverrides{}.Normalize() }

func (e *overrideEdit) isAuto() bool { return e.o == defaults.UserOverrides{}.Normalize() }

// inherited is the description of a knob left on auto in one host's scope
// while every host's sets it.
func (e *overrideEdit) inherited(k knob) string {
	if e.base == nil || *k.field(&e.o) != defaults.Auto {
		return ""
	}
	v := *k.field(e.base)
	if v == defaults.Auto {
		return ""
	}
	return "Auto follows all hosts: " + k.labels[indexOf(k.values, v)]
}

// inheritedBitrate is inherited for the bitrate, which is not a knob.
func (e *overrideEdit) inheritedBitrate() string {
	if e.base == nil || e.o.BitrateMbps != 0 || e.base.BitrateMbps <= 0 {
		return ""
	}
	return fmt.Sprintf("Auto follows all hosts: %d Mbps", e.base.BitrateMbps)
}

// knobControl is the knob's segmented control, keyed by knob and scope so
// its state stays its own as rows come and go.
func (e *overrideEdit) knobControl(c *mui.Context, k knob) {
	c.Key("knob:" + e.scope.hostID + ":" + k.id)
	pickFrom(c, k.title, k.values, k.labels, k.field(&e.o))
}

// forced is the value the host's policy sets k to, if it sets one.
func (e *overrideEdit) forced(k knob) (string, bool) {
	if e.policy == nil {
		return "", false
	}
	v := *k.field(e.policy)
	if v == "" || v == defaults.Auto {
		return "", false
	}
	if i := indexOf(k.values, v); i >= 0 {
		return k.labels[i], true
	}
	return v, true
}

// overridden counts the values a host's scope sets for itself, of all it
// could.
func (e *overrideEdit) overridden() (n, of int) {
	for _, k := range allKnobs {
		if *k.field(&e.o) != defaults.Auto {
			n++
		}
	}
	if e.o.BitrateMbps != 0 {
		n++
	}
	if e.o.VirtualDisplayGPU != "" {
		n++
	}
	return n, len(allKnobs) + 2
}

// overrides: the scope sets one of ks, or the bitrate with bitrate, for
// itself.
func (e *overrideEdit) overrides(bitrate bool, ks ...knob) bool {
	if e.scope.all() {
		return false
	}
	if bitrate && e.o.BitrateMbps != 0 {
		return true
	}
	for _, k := range ks {
		if *k.field(&e.o) != defaults.Auto {
			return true
		}
	}
	return false
}

// drafts are text fields that edit a draft and commit it on Enter or when
// the focus leaves, so a value is checked once, not at every key.
type drafts struct {
	text    map[string]*string
	focused map[string]bool
	err     string
}

// field draws the draft field key, width wide or as wide as its parent
// for 0, showing value while it is not focused. It returns the draft when
// the user commits it.
func (d *drafts) field(c *mui.Context, key, name, value, placeholder string, width float32) (string, bool) {
	if d.text == nil {
		d.text, d.focused = map[string]*string{}, map[string]bool{}
	}
	v := d.text[key]
	if v == nil {
		v = new(string)
		d.text[key] = v
	}
	if !d.focused[key] {
		*v = value
	}
	in := mui.TextInput(c.Key("draft:"+key), v).Height(theme.SizeControl).Placeholder(placeholder).Label(name).
		Font(theme.FontMono).FontSize(12).Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
	if width > 0 {
		in.Width(width)
	} else {
		in.FillWidth()
	}
	focused := in.Focused()
	commit := in.Submitted() || d.focused[key] && !focused
	d.focused[key] = focused
	return *v, commit
}

// size parses a custom size draft: an even number in [lo, hi], or old with
// the reason kept in err.
func (d *drafts) size(s string, old, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < lo || n > hi || n%2 != 0 {
		d.err = fmt.Sprintf("%q is not an even size from %d to %d", s, lo, hi)
		return old
	}
	d.err = ""
	return n
}

func codecName(c defaults.Codec) string {
	switch c {
	case defaults.CodecH264:
		return "H.264"
	case defaults.CodecHEVC:
		return "HEVC"
	case defaults.CodecAV1:
		return "AV1"
	case defaults.CodecPyroWave:
		return "PyroWave"
	}
	return "Unknown"
}

// shortGPU drops the vendor's brand words: "NVIDIA GeForce RTX 4080" is
// "RTX 4080".
func shortGPU(name string) string {
	for _, p := range []string{"NVIDIA ", "GeForce ", "AMD ", "Radeon(TM) ", "Intel(R) ", "Apple "} {
		name = strings.TrimPrefix(name, p)
	}
	return strings.TrimSpace(name)
}
