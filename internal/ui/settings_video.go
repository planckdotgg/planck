package ui

import (
	"fmt"
	"strconv"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
)

// resolutionLabels are the Resolution dropdown's: literal 16:9 sizes, and
// Auto, which follows the window or the host's screen.
var resolutionLabels = []string{"Auto", "1920×1080", "2560×1440", "3840×2160", "Custom…"}

func (a *App) videoPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Video & Display", "What this "+machineWord(a.goos)+" asks a remote host for. The host’s policy wins.", nil)
	a.scopeBar(c, f.e)
	e := f.e
	columns(c, 1, 1, func() {
		setPanel(c, "Codec", overriddenTag(c, e, false, knobCodec), func() { a.entries(c, f, "video.codec") })
		setPanel(c, "Colour", overriddenTag(c, e, false, knobChroma, knobBitDepth), func() {
			a.entries(c, f, "video.chroma", "video.bitDepth")
			infoWell(c, a.colourReason(f))
		})
		setPanel(c, "Presentation", nil, func() { a.entries(c, f, "video.presentation") })
	}, func() {
		setPanel(c, "Display & rate", overriddenTag(c, e, true, knobResolution, knobFPS, knobDirect, knobVSync, knobHDR), func() {
			a.entries(c, f, "video.resolution", "video.fps", "video.bitrate", "video.direct", "video.vsync", "video.hdr", "video.multi")
		})
		a.advancedEncoder(c, f)
	})
}

// overriddenTag is a panel's "overridden for <host>" tag, when the host's
// scope sets one of ks for itself.
func overriddenTag(c *mui.Context, e *overrideEdit, bitrate bool, ks ...knob) func() {
	if !e.overrides(bitrate, ks...) {
		return nil
	}
	return func() {
		mui.Box(c).Height(18).PaddingX(6).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).Background(theme.RoleWell).Children(func() {
			mono(c, "overridden for "+e.scope.name).FontSize(theme.TextMini).TextColor(theme.RoleText2)
		})
	}
}

// scopeBar picks whose stream format Settings edits, says how much of it
// is the host's own, and clears that.
func (a *App) scopeBar(c *mui.Context, e *overrideEdit) {
	scopes := []overrideScope{{name: "All hosts"}}
	for _, h := range a.backend.Hosts() {
		if h.Paired || h.ID == a.ovScope.hostID {
			scopes = append(scopes, overrideScope{hostID: h.ID, name: h.Name})
		}
	}
	names := make([]string, len(scopes))
	sel := 0
	for i, sc := range scopes {
		names[i] = sc.name
		if sc.hostID == e.scope.hostID {
			sel = i
		}
	}
	mui.Row(c).FillWidth().MinHeight(48).Padding(theme.Space2, theme.Space4).Gap(theme.Space3).AlignItems(mui.Center).Wrap().
		Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).Children(func() {
		label(c, "Stream format for").FontSize(12).FontWeight(500).TextColor(theme.RoleText2)
		c.Key("override-scope")
		if segmented(c, "Stream format for", &sel, names...) {
			a.ovScope = scopes[sel]
			a.drafts.err = ""
		}
		if e.scope.all() {
			caption(c, "Every value on Auto lets the host and this client choose.")
		} else {
			n, of := e.overridden()
			mui.Row(c).Gap(4).AlignItems(mui.Baseline).Shrink(1).Children(func() {
				caption(c, e.scope.name+" overrides")
				mono(c, strconv.Itoa(n)).FontSize(theme.TextCaption).TextColor(theme.RoleText2)
				caption(c, fmt.Sprintf("of %d values. The rest follow All hosts.", of))
			})
		}
		mui.Spacer(c)
		if !e.isAuto() && button(c, buttonSecondary, "Clear overrides", false).Height(28).Clicked() {
			e.resetToAuto()
			a.drafts.err = ""
		}
	})
}

// knobRow is a stream knob, titled title: its segmented choice, or the
// host's value when its policy sets it.
func (f *settingsFrame) knobRow(c *mui.Context, k knob, title, help string) {
	e := f.e
	if s := e.inherited(k); s != "" {
		help = s
	}
	f.row(c, title, help, func() {
		if v, ok := e.forced(k); ok {
			mui.Box(c).Width(220).Children(func() { lockedControl(c, k.title, v, e.scope.name) })
			return
		}
		e.knobControl(c, k)
	})
}

func (a *App) codecRow(c *mui.Context, f *settingsFrame) {
	e := f.e
	help := knobCodec.desc
	if s := e.inherited(knobCodec); s != "" {
		help = s
	}
	if v, ok := e.forced(knobCodec); ok {
		f.row(c, "Codec", help, func() {
			mui.Box(c).Width(220).Children(func() { lockedControl(c, "Codec", v, e.scope.name) })
		})
		return
	}
	cur := &e.o.Codec
	pick := func(value, text string) {
		if chip(c, "Codec: "+text, text, string(*cur) == value, theme.SizeControl) {
			*cur = defaults.CodecPref(value)
		}
	}
	mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
		mui.Row(c).Gap(theme.Space2).Wrap().Children(func() {
			for _, v := range []string{"auto", "hevc", "av1", "h264"} {
				pick(v, knobCodec.labels[indexOf(knobCodec.values, v)])
			}
		})
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			pick("pyrowave", "PyroWave")
			mui.Box(c).Height(18).PaddingX(6).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).Background(theme.RoleWell).Children(func() {
				mono(c, "LAN only").FontSize(theme.TextMini).TextColor(theme.RoleText2)
			})
		})
		caption(c, help)
		if p := f.stats.Profile; f.stats.Live && p != nil && (e.scope.all() || e.scope.hostID == f.stats.HostID) {
			gpu := shortGPU(p.EncodeAdapter.Name)
			caption(c, fmt.Sprintf("%s streams %s now, on %s %s.", f.stats.HostName, codecName(p.Codec), gpu, p.EncoderAPI)).TextColor(theme.RoleText2)
		}
	})
}

// colourReason explains what the host can give: why a live stream cannot
// take 4:4:4, or what takes it.
func (a *App) colourReason(f *settingsFrame) string {
	if f.stats.Live {
		if r := no444Reason(f.stats.Profile); r != "" {
			return "On " + f.stats.HostName + ": " + r
		}
	}
	return "4:4:4 keeps text and UI sharp; 10-bit removes banding. NVENC and oneVPL encode 4:4:4; AMF and Apple Silicon encode 4:2:0, and Planck says so in the stream menu rather than failing quietly."
}

func (a *App) resolutionRow(c *mui.Context, f *settingsFrame) {
	e := f.e
	help := ""
	if s := e.inherited(knobResolution); s != "" {
		help = s
	}
	before := e.o.Resolution
	f.row(c, "Resolution", help, func() {
		if v, ok := e.forced(knobResolution); ok {
			mui.Box(c).Width(260).Children(func() { lockedControl(c, "Resolution", v, e.scope.name) })
			return
		}
		mui.Box(c).Width(260).Children(func() {
			dropdown(c, "Resolution", (*string)(&e.o.Resolution), knobResolution.values, resolutionLabels, 260)
		})
	})
	if e.o.Resolution != "custom" {
		return
	}
	if before != "custom" && (e.o.CustomWidth == 0 || e.o.CustomHeight == 0) {
		e.o.CustomWidth, e.o.CustomHeight = customWidth, customHeight
	}
	f.row(c, "Custom size", fmt.Sprintf("Even sizes from %d×%d to %d×%d (8K).",
		defaults.MinCustomWidth, defaults.MinCustomHeight, defaults.MaxCustomWidth, defaults.MaxCustomHeight), func() {
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			if v, ok := a.drafts.field(c, e.scope.hostID+":customWidth", "Custom width", strconv.Itoa(e.o.CustomWidth), "", 80); ok {
				e.o.CustomWidth = a.drafts.size(v, e.o.CustomWidth, defaults.MinCustomWidth, defaults.MaxCustomWidth)
			}
			label(c, "×").TextColor(theme.RoleText3)
			if v, ok := a.drafts.field(c, e.scope.hostID+":customHeight", "Custom height", strconv.Itoa(e.o.CustomHeight), "", 80); ok {
				e.o.CustomHeight = a.drafts.size(v, e.o.CustomHeight, defaults.MinCustomHeight, defaults.MaxCustomHeight)
			}
		})
	})
}

// bitrateRow is Auto or Manual, with a 5–1000 Mbps slider under Manual,
// or the host's cap when its policy sets one.
func (a *App) bitrateRow(c *mui.Context, f *settingsFrame) {
	e := f.e
	if e.policy != nil && e.policy.BitrateMbps > 0 {
		f.row(c, "Bitrate", "", func() {
			mui.Box(c).Width(260).Children(func() {
				lockedControl(c, "Bitrate", fmt.Sprintf("%d Mbps", e.policy.BitrateMbps), e.scope.name)
			})
		})
		return
	}
	help := e.inheritedBitrate()
	f.row(c, "Bitrate", help, func() {
		if e.o.BitrateMbps == 0 {
			caption(c, "follows the route")
		}
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
	mui.Column(c).FillWidth().Padding(0, 0, theme.Space2).Gap(theme.Space2).Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Baseline).Children(func() {
			caption(c, "Video bitrate").Grow(1)
			hudReading(c, strconv.Itoa(e.o.BitrateMbps), "Mbps")
		})
		v := float64(e.o.BitrateMbps)
		c.Key("knob:" + e.scope.hostID + ":bitrateSlider")
		if mui.StepSlider(c, &v, minBitrateMbps, maxManualBitrateMbps, 5).Label("Video bitrate").FillWidth().Changed() {
			e.o.BitrateMbps = int(v)
		}
	})
}

// hdrRow is HDR10 output, Auto or Off, with why a live stream is SDR.
func (a *App) hdrRow(c *mui.Context, f *settingsFrame) {
	help := knobHDR.desc
	if p := f.stats.Profile; f.stats.Live && p != nil && (f.e.scope.all() || f.e.scope.hostID == f.stats.HostID) {
		switch why := decisionWhy(p, "hdr10"); {
		case p.HDR10:
			help = f.stats.HostName + " streams HDR10 now."
		case why != "" && why != "override":
			help = f.stats.HostName + " streams SDR: " + why + "."
		}
	}
	f.knobRow(c, knobHDR, "HDR10 output", help)
}

// decisionWhy is why p settled knob as it did, "" without a decision.
func decisionWhy(p *defaults.ResolvedProfile, knob string) string {
	for _, d := range p.Decisions {
		if d.Knob == knob {
			return d.Why
		}
	}
	return ""
}

// multiDisplayRows are Open all host displays and, while it is on, how
// many windows it opens at most.
func (a *App) multiDisplayRows(c *mui.Context, f *settingsFrame) {
	f.switchRow(c, "Open all host displays", "A window for each display of a host that streams several at once. A host that streams one moves between them with "+
		a.keyText(HotkeySwitchDisplay)+" or the stream menu.", &f.s.OpenEveryHostDisplay)
	if !f.s.OpenEveryHostDisplay {
		return
	}
	labels := make([]string, 0, MaxStreamWindows-MinStreamWindows+1)
	for n := MinStreamWindows; n <= MaxStreamWindows; n++ {
		labels = append(labels, strconv.Itoa(n))
	}
	sel := min(max(f.s.MaxStreamWindows, MinStreamWindows), MaxStreamWindows) - MinStreamWindows
	f.row(c, "Most windows", "Displays past this many stay one switch away.", func() {
		c.Key("max-stream-windows")
		if segmented(c, "Most windows", &sel, labels...) {
			f.s.MaxStreamWindows = sel + MinStreamWindows
		}
	})
}

// advancedEncoder is the disclosure of the knobs few need: slices, the
// NVENC split, and the host's virtual display.
func (a *App) advancedEncoder(c *mui.Context, f *settingsFrame) {
	e := f.e
	open := !a.set.advancedClosed
	mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space3, theme.Space4).Gap(theme.Space2).Children(func() {
		head := mui.ButtonBase(c).FillWidth().MinHeight(20).Gap(theme.Space2).AlignItems(mui.Center).
			Label("Advanced encoder").Cursor(mui.CursorPointer).Checked(open)
		head.Children(func() {
			rot := float32(0)
			if open {
				rot = 90
			}
			mui.Icon(c, theme.IconChevronRight).Size(12, 12).TextColor(theme.RoleText3).Rotate(rot)
			sectionLabel(c, "Advanced encoder")
			mui.Spacer(c)
			if t := overriddenTag(c, e, false, knobSlices, knobSplit, knobVDD); t != nil {
				t()
			}
		})
		if head.Clicked() {
			a.set.advancedClosed = open
		}
		if !open {
			return
		}
		ids := []string{"video.slices", "video.split", "video.vdd"}
		if e.o.VirtualDisplay != defaults.ToggleOff {
			ids = append(ids, "video.vddGPU")
		}
		a.entries(c, f, ids...)
	})
}

// gpuRow names the host GPU for the virtual display, by part of its name.
func (a *App) gpuRow(c *mui.Context, f *settingsFrame) {
	e := f.e
	help := "The host GPU that renders the virtual display and encodes, by part of its name, so the frame never crosses GPUs."
	if e.base != nil && e.o.VirtualDisplayGPU == "" && e.base.VirtualDisplayGPU != "" {
		help = "Auto follows all hosts: " + e.base.VirtualDisplayGPU
	}
	f.row(c, "Virtual display GPU", help, func() {
		if v, ok := a.drafts.field(c, e.scope.hostID+":gpu", "Virtual display GPU", e.o.VirtualDisplayGPU, "Auto", 160); ok {
			e.o.VirtualDisplayGPU = strings.TrimSpace(v)
		}
	})
}

// presentationRows are the presentation choices this build has no
// control of yet.
func (f *settingsFrame) presentationRows(c *mui.Context) {
	for i, r := range []struct{ title, choices string }{
		{"Scaling", "Fit · Fill · 1:1"},
		{"Decoder", "Hardware · Allow software"},
		{"Keep sending on a still desktop", "Constant frame rate"},
	} {
		if i > 0 {
			divider(c)
		}
		f.row(c, r.title, "", func() { mono(c, r.choices).FontSize(theme.TextCaption).TextColor(theme.RoleText3) })
	}
	caption(c, "Scaling, the decoder and a constant frame rate are not adjustable in this build yet.")
}

// settingsScope is the scope Settings edits: every host, or a paired host
// picked in the scope bar or from its card.
func (a *App) settingsScope() overrideScope {
	if a.ovScope.all() {
		return overrideScope{name: "All hosts"}
	}
	for _, h := range a.backend.Hosts() {
		if h.ID == a.ovScope.hostID {
			a.ovScope.name = h.Name
		}
	}
	return a.ovScope
}
