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
	knobRoute = knob{id: "routePolicy", title: "Route", desc: "Auto uses the LAN when it reaches the host, and Tailscale when not.",
		values: []string{"auto", "lan", "tailscale"}, labels: []string{"Auto", "LAN", "Tailscale"},
		field: func(o *defaults.UserOverrides) *string { return (*string)(&o.RoutePolicy) }}
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

func (e *overrideEdit) knobRow(c *mui.Context, k knob) {
	desc := k.desc
	if s := e.inherited(k); s != "" {
		desc = s
	}
	settingRow(c, k.title, desc, func() { e.knobControl(c, k) })
}

// resolutionRow adds the custom size's fields under Custom.
func (a *App) resolutionRow(c *mui.Context, e *overrideEdit) {
	before := e.o.Resolution
	e.knobRow(c, knobResolution)
	if e.o.Resolution != "custom" {
		return
	}
	if before != "custom" && (e.o.CustomWidth == 0 || e.o.CustomHeight == 0) {
		e.o.CustomWidth, e.o.CustomHeight = customWidth, customHeight
	}
	settingRow(c, "Custom size", fmt.Sprintf("Even sizes from %d x %d to %d x %d.",
		defaults.MinCustomWidth, defaults.MinCustomHeight, defaults.MaxCustomWidth, defaults.MaxCustomHeight), func() {
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			if v, ok := a.drafts.field(c, e.scope.hostID+":customWidth", "Custom width", strconv.Itoa(e.o.CustomWidth), "", 80); ok {
				e.o.CustomWidth = a.drafts.size(v, e.o.CustomWidth, defaults.MinCustomWidth, defaults.MaxCustomWidth)
			}
			label(c, "x").TextColor(theme.RoleText3)
			if v, ok := a.drafts.field(c, e.scope.hostID+":customHeight", "Custom height", strconv.Itoa(e.o.CustomHeight), "", 80); ok {
				e.o.CustomHeight = a.drafts.size(v, e.o.CustomHeight, defaults.MinCustomHeight, defaults.MaxCustomHeight)
			}
		})
	})
}

// bitrateRow is Auto or Manual, with a slider under Manual.
func (e *overrideEdit) bitrateRow(c *mui.Context) {
	desc := "Auto starts from the route and follows it."
	if s := e.inheritedBitrate(); s != "" {
		desc = s
	}
	settingRow(c, "Bitrate", desc, func() {
		mode := 0
		if e.o.BitrateMbps > 0 {
			mode = 1
		}
		c.Key("knob:" + e.scope.hostID + ":bitrate")
		if segmented(c, "Bitrate", &mode, "Auto", "Manual") {
			e.o.BitrateMbps = 0
			if mode == 1 {
				e.o.BitrateMbps = manualBitrateMbps
			}
		}
	})
	if e.o.BitrateMbps == 0 {
		return
	}
	mui.Column(c).FillWidth().Padding(0, theme.Space5, theme.Space4).Gap(theme.Space3).Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Baseline).Children(func() {
			secondary(c, "Video bitrate").Grow(1)
			hudReading(c, fmt.Sprint(e.o.BitrateMbps), "Mbps")
		})
		v := float64(e.o.BitrateMbps)
		c.Key("knob:" + e.scope.hostID + ":bitrateSlider")
		if mui.StepSlider(c, &v, minBitrateMbps, maxManualBitrateMbps, 5).Label("Video bitrate").FillWidth().Changed() {
			e.o.BitrateMbps = int(v)
		}
	})
}

// gpuRow names the host GPU for the virtual display, by part of its name.
func (a *App) gpuRow(c *mui.Context, e *overrideEdit) {
	desc := "The host GPU that renders the virtual display and encodes, by part of its name."
	if e.base != nil && e.o.VirtualDisplayGPU == "" && e.base.VirtualDisplayGPU != "" {
		desc = "Auto follows all hosts: " + e.base.VirtualDisplayGPU
	}
	settingRow(c, "Virtual display GPU", desc, func() {
		if v, ok := a.drafts.field(c, e.scope.hostID+":gpu", "Virtual display GPU", e.o.VirtualDisplayGPU, "Auto", 160); ok {
			e.o.VirtualDisplayGPU = strings.TrimSpace(v)
		}
	})
}

// videoOverridePanel, hostOverridePanel and presentOverridePanel are
// Settings' stream panels for one scope.
func (a *App) videoOverridePanel(c *mui.Context, e *overrideEdit) {
	panel(c, "Video", e.scope.name, func() {
		e.knobRow(c, knobCodec)
		divider(c)
		e.knobRow(c, knobChroma)
		divider(c)
		e.knobRow(c, knobBitDepth)
		divider(c)
		a.resolutionRow(c, e)
		divider(c)
		e.knobRow(c, knobFPS)
		divider(c)
		e.bitrateRow(c)
	})
}

func (a *App) hostOverridePanel(c *mui.Context, e *overrideEdit) {
	panel(c, "Host display and encoder", e.scope.name, func() {
		e.knobRow(c, knobVDD)
		if e.o.VirtualDisplay != defaults.ToggleOff {
			divider(c)
			a.gpuRow(c, e)
		}
		divider(c)
		e.knobRow(c, knobSlices)
		divider(c)
		e.knobRow(c, knobSplit)
	})
}

func (a *App) presentOverridePanel(c *mui.Context, e *overrideEdit) {
	panel(c, "Presentation and route", e.scope.name, func() {
		e.knobRow(c, knobDirect)
		divider(c)
		e.knobRow(c, knobVSync)
		divider(c)
		e.knobRow(c, knobRoute)
	})
}

// drafts are text fields that edit a draft and commit it on Enter or when
// the focus leaves, so a value is checked once, not at every key.
type drafts struct {
	text    map[string]*string
	focused map[string]bool
	err     string
}

// field draws the draft field key, showing value while it is not
// focused. It returns the draft when the user commits it.
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
	in := mui.TextInput(c.Key("draft:"+key), v).Width(width).Height(theme.SizeControl).Placeholder(placeholder).Label(name).
		Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
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
