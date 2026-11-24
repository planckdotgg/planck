package ui

import (
	"fmt"
	"math"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
)

const (
	menuWidth   = 320
	flyoutWidth = 236
	menuGap     = 6
)

// rebuildNote closes each flyout whose choice restarts the stream.
const rebuildNote = "The host rebuilds the stream. The picture holds until the new one starts."

// streamMenu is the quick-action menu: the stream's live controls, a
// second face on the knobs Settings has. btn is the round button it opens
// from; while only the tab shows, the menu waits for the button to pop
// out.
func (a *App) streamMenu(c *mui.Context, tel Telemetry, btn mui.Element) {
	o := &a.ovl
	m := mui.MenuBase(c.Key("overlay-menu"), &o.menuOpen)
	m.Trigger.Absolute().Left(0).Top(0).Size(0, 0).TabStop(false).AccessHidden(true)
	if !o.menuOpen {
		o.tray = ""
		return
	}
	if btn == (mui.Element{}) {
		return
	}
	before := a.backend.Settings()
	s := before
	scope := overrideScope{hostID: tel.HostID, name: tel.HostName}
	e := a.editOverrides(scope)
	m.Popup(func(panel mui.Element) {
		a.anchorMenu(panel, btn)
		panel.Width(menuWidth).Radius(theme.RadiusMd).Clip().Background(theme.HUDGlass).Border(1, theme.HUDEdge).
			Shadow(0, 16, 40, 0, theme.FloatShadow).Label("Stream controls menu")
		if o.tray != "" && panel.Shortcut(0, mui.KeyEscape) {
			o.tray = ""
		}
		a.menuHead(c, tel)
		mui.Column(c).FillWidth().Padding(6).Gap(1).Children(func() {
			a.modeGroup(c, m, e, &s)
			a.videoGroup(c, m, e, tel)
			a.soundGroup(c, m, &s, tel)
			a.overlayGroup(c, m)
		})
		a.menuFoot(c, m)
	})
	a.saveSettings(before, s)
	a.saveOverrides(e)
}

// anchorMenu opens the menu away from the edge the button sits on, so the
// button never comes between the pointer and the menu.
func (a *App) anchorMenu(panel, btn mui.Element) {
	o := &a.ovl
	switch {
	case o.dock == DockTop:
		panel.AttachTo(btn, mui.AnchorBottomStart, mui.AnchorTopStart).Margin(menuGap, 0, 0, 0)
	case o.dock == DockBottom:
		panel.AttachTo(btn, mui.AnchorTopStart, mui.AnchorBottomStart).Margin(0, 0, menuGap, 0)
	case o.menuOpensLeft():
		panel.AttachTo(btn, mui.AnchorTopStart, mui.AnchorTopEnd).Margin(0, menuGap, 0, 0)
	default:
		panel.AttachTo(btn, mui.AnchorTopEnd, mui.AnchorTopStart).Margin(0, 0, 0, menuGap)
	}
}

// menuHead answers "how is it going" without the metrics window: host,
// route and glass to glass. It is not a drag handle.
func (a *App) menuHead(c *mui.Context, tel Telemetry) {
	mui.Row(c).FillWidth().Height(44).Padding(0, 10, 0, 12).Gap(theme.Space2).AlignItems(mui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(theme.HUDDivider).Children(func() {
		mui.Text(c, tel.HostName).Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(600).TextColor(theme.RoleText).
			SingleLine().Grow(1).Shrink(1)
		pathBadge(c, tel.PathKind, pathShort(tel.PathKind), theme.TextMini)
		mui.Row(c).AlignItems(mui.Baseline).Gap(2).Children(func() {
			mono(c, formatMs(tel.TotalMs)).FontWeight(500).TextColor(theme.RoleAccent)
			mono(c, "ms").FontSize(theme.TextMini).TextColor(theme.RoleText3)
		})
	})
}

// pathBadge is the route as a pill: its slice meter and name in the
// route's colour, so the bars carry the state without the colour.
func pathBadge(c *mui.Context, k discovery.PathKind, name string, size float32) mui.Element {
	col := signalColor(k)
	return mui.Row(c).Height(18).PaddingX(7).Gap(5).AlignItems(mui.Center).Radius(theme.RadiusPill).
		Border(1, col.Alpha(0.4)).Label(pathLabel(k)).Children(func() {
		mui.Box(c).Size(8, 8).Draw(func(p *mui.Painter, r mui.Rect) {
			filled := pathBars(k)
			for i := 0; i < 3; i++ {
				bc := theme.RoleBorderStrong
				if i < filled {
					bc = col
				}
				p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*3), W: r.W, H: 2}, bc, 0)
			}
		})
		mui.Text(c, name).Font(theme.FontMono).FontSize(size).TextColor(col).SingleLine()
	})
}

func pathShort(k discovery.PathKind) string {
	switch k {
	case discovery.PathLANDirect:
		return "LAN"
	case discovery.PathTailscaleDirect:
		return "Tailscale"
	case discovery.PathTailscaleDERP:
		return "Relay"
	}
	return "Unknown"
}

func groupLabel(c *mui.Context, s string, first bool) {
	top := float32(8)
	if first {
		top = 0
	}
	mui.Row(c).FillWidth().Height(22+top).Padding(top, 8, 0, 8).AlignItems(mui.Center).Children(func() {
		mui.Text(c, strings.ToUpper(s)).Font(theme.FontMono).FontSize(theme.TextMini).LetterSpacing(0.08 * theme.TextMini).
			TextColor(theme.RoleText3).SingleLine()
	})
}

// rowParts are what a menu row shows besides its label.
type rowParts struct {
	icon     *mui.SVG
	caption  string
	value    string
	open     bool
	trailing func()
}

// menuRow lays an item out as a row of the menu: icon, label (and its
// caption), value, and what trails it.
func menuRow(c *mui.Context, item mui.Element, label string, r rowParts) {
	h := float32(32)
	if r.caption != "" {
		h = 44
	}
	item.FillWidth().Height(h).Radius(theme.RadiusSm).Padding(0, 8).Gap(10).AlignItems(mui.Center).
		Label(label).Cursor(mui.CursorPointer)
	if item.Highlighted() || r.open {
		item.Background(theme.HUDRowHover)
	}
	item.Children(func() {
		ic := theme.RoleText2
		if r.open {
			ic = theme.RoleText
		}
		mui.Icon(c, r.icon).Size(16, 16).TextColor(ic).Shrink(0)
		mui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
			mui.Text(c, label).Font(theme.FontUI).FontSize(theme.TextBody).TextColor(theme.RoleText).SingleLine()
			if r.caption != "" {
				mui.Text(c, r.caption).Font(theme.FontUI).FontSize(theme.TextCaption).TextColor(theme.RoleText3).SingleLine()
			}
		})
		if r.value != "" {
			mui.Text(c, r.value).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText2).SingleLine().Shrink(0)
		}
		if r.trailing != nil {
			r.trailing()
		}
	})
}

// switchItem is a row with a switch, which flips without closing the
// menu. It reports whether it was clicked.
func switchItem(c *mui.Context, m mui.MenuParts, label string, on bool, r rowParts, keys string) bool {
	it := m.CheckItem(on).KeepOpen()
	r.trailing = func() {
		if keys != "" {
			badge(c, keys)
		}
		switchTrack(c, on)
	}
	menuRow(c, it, label, r)
	return it.Clicked()
}

// chevron trails a row that opens a flyout (right) or a tray (down, up
// while open).
func chevron(c *mui.Context, tray, open bool) {
	col := theme.RoleText3
	if open {
		col = theme.RoleText
	}
	if !tray {
		mui.Icon(c, theme.IconChevronRight).Size(12, 12).TextColor(col).Shrink(0)
		return
	}
	ic := mui.Icon(c, theme.IconChevron).Size(12, 12).TextColor(col).Shrink(0)
	if open {
		ic.Rotate(180)
	}
}

// choice is one choice of a flyout or a tray.
type choice struct {
	label, detail string
	selected      bool
	// reason, when set, is why the choice cannot be taken.
	reason string
	pick   func()
}

// flyout is a row opening a panel of four or more choices, or of choices
// that carry a detail, on the side away from the button.
func (a *App) flyout(c *mui.Context, m mui.MenuParts, label string, r rowParts, note string, choices []choice) {
	it := m.SubmenuItem()
	r.trailing = func() { chevron(c, false, r.open) }
	menuRow(c, it, label, r)
	m.Submenu(it, func(panel mui.Element) {
		if a.ovl.menuOpensLeft() {
			panel.AttachTo(it, mui.AnchorTopStart, mui.AnchorTopEnd).Margin(-7, menuGap+6, 0, 0)
		} else {
			panel.AttachTo(it, mui.AnchorTopEnd, mui.AnchorTopStart).Margin(-7, 0, 0, menuGap+6)
		}
		panel.Width(flyoutWidth).Padding(6, 6, 0, 6).Radius(theme.RadiusMd).Clip().Background(theme.HUDGlass).
			Border(1, theme.HUDEdge).Shadow(0, 16, 40, 0, theme.FloatShadow).Label(label)
		for _, ch := range choices {
			fi := m.RadioItem(ch.selected).Label(label+": "+ch.label).Height(30).Padding(0, 8, 0, 6).Gap(8).
				Radius(theme.RadiusSm).AlignItems(mui.Center).Cursor(mui.CursorPointer)
			if fi.Highlighted() {
				fi.Background(theme.HUDRowHover)
			}
			fi.Children(func() {
				selSlice(c, ch.selected, 12)
				mui.Text(c, ch.label).Font(theme.FontUI).FontSize(theme.TextBody).TextColor(theme.RoleText).SingleLine().Grow(1)
				if ch.detail != "" {
					mui.Text(c, ch.detail).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText3).SingleLine()
				}
			})
			if fi.Clicked() {
				ch.pick()
			}
		}
		mui.Text(c, note).Font(theme.FontUI).FontSize(theme.TextCaption).FixedLineHeight(15).TextColor(theme.RoleText3).
			FillWidth().Padding(8, 8, 10, 8)
	})
}

// selSlice is the selection marker everywhere in the overlay: a 2 px
// ember slice, transparent on the choices not in force.
func selSlice(c *mui.Context, on bool, h float32) {
	s := mui.Box(c).Size(2, h).Radius(1).Shrink(0)
	if on {
		s.Background(theme.RoleAccent).Label("In force")
	}
}

// tray is a row that opens its two or three choices, or its slider, in
// place under it, pushing the rows below down.
func (a *App) tray(c *mui.Context, m mui.MenuParts, id, label string, r rowParts, body func()) {
	o := &a.ovl
	it := m.Item().KeepOpen()
	r.open = o.tray == id
	it.Expanded(r.open)
	r.trailing = func() { chevron(c, true, r.open) }
	menuRow(c, it, label, r)
	if it.Clicked() {
		if o.tray == id {
			o.tray = ""
		} else {
			o.tray = id
		}
		r.open = o.tray == id
	}
	if r.open {
		mui.Column(c).FillWidth().Padding(10).Gap(9).Radius(theme.RadiusSm).Background(theme.HUDTray).
			Label(label + " choices").Children(body)
	}
}

// chips lays a tray's choices out in a row the keys take as one item,
// with the reason under it for any that cannot be taken.
func chips(c *mui.Context, m mui.MenuParts, label string, choices []choice) {
	var reason string
	m.Group(func(g mui.Element) {
		g.Gap(6).Label(label).Wrap()
		for _, ch := range choices {
			chip := m.RadioItem(ch.selected).KeepOpen().Label(label+": "+ch.label).Height(26).Padding(0, 10).
				Gap(6).Radius(theme.RadiusSm).AlignItems(mui.Center).Disabled(ch.reason != "")
			text := theme.RoleText2
			switch {
			case ch.reason != "":
				chip.Border(1, theme.RoleBorder)
				text = theme.Carbon500
				reason = ch.reason
			case ch.selected:
				chip.Background(theme.RoleHover).Border(1, theme.RoleBorderStrong)
				text = theme.RoleText
			default:
				chip.Border(1, theme.RoleBorderStrong).Cursor(mui.CursorPointer)
				if chip.Highlighted() {
					chip.Background(theme.HUDRowHover)
				}
			}
			chip.Children(func() {
				if ch.selected {
					selSlice(c, true, 10)
				}
				mui.Text(c, ch.label).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(text).SingleLine()
			})
			if chip.Clicked() && ch.reason == "" {
				ch.pick()
			}
		}
	})
	if reason != "" {
		mui.Text(c, reason).Font(theme.FontUI).FontSize(theme.TextCaption).FixedLineHeight(15).TextColor(theme.RoleText3).FillWidth()
	}
}

// traySlider is a tray's slider: ember at 45 % while auto holds the
// value, full once the user took it, with the scale's ends printed.
func traySlider(c *mui.Context, label string, v *float64, lo, hi float64, live bool, loText, hiText string) bool {
	changed := false
	mui.Column(c).FillWidth().Gap(4).Children(func() {
		s := mui.SliderBase(c.Key(label), v, lo, hi).FillWidth().Height(16).Label(label)
		val := *v
		s.Draw(func(p *mui.Painter, r mui.Rect) {
			const knob = 12
			track := mui.Rect{X: r.X + knob/2, Y: r.Y + r.H/2 - 2, W: r.W - knob, H: 4}
			k := float32(0)
			if hi > lo {
				k = float32((val - lo) / (hi - lo))
			}
			fill, kc := theme.RoleAccent.Alpha(0.45), theme.RoleText3
			if live {
				fill, kc = theme.RoleAccent, theme.RoleText
			}
			p.Fill(track, theme.RoleHover, 2)
			p.Fill(mui.Rect{X: track.X, Y: track.Y, W: track.W * k, H: 4}, fill, 2)
			p.Fill(mui.Rect{X: track.X + track.W*k - knob/2, Y: r.Y + r.H/2 - knob/2, W: knob, H: knob}, kc, knob/2)
		})
		changed = s.Changed()
		mui.Row(c).FillWidth().Children(func() {
			mui.Text(c, loText).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText3).Grow(1)
			mui.Text(c, hiText).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleText3)
		})
	})
	return changed
}

func (a *App) modeGroup(c *mui.Context, m mui.MenuParts, e *overrideEdit, s *Settings) {
	groupLabel(c, "Mode & window", true)
	if w := a.window; w != nil {
		if switchItem(c, m, "Fullscreen", w.Fullscreen(), rowParts{icon: theme.IconFullscreen}, a.shortcut(false, "F")) {
			w.SetFullscreen(!w.Fullscreen())
		}
	}
	if switchItem(c, m, "Immersive mode", s.SystemKeysCapture, rowParts{icon: theme.IconImmersive}, a.shortcut(false, "I")) {
		s.SystemKeysCapture = !s.SystemKeysCapture
	}
	if a.goos == "darwin" {
		on := e.o.MetalDirectDisplay != defaults.ToggleOff
		if switchItem(c, m, "Direct to display", on, rowParts{icon: theme.IconDirect, caption: "macOS · pauses while the overlay is open"}, "") {
			e.o.MetalDirectDisplay = defaults.ToggleOn
			if on {
				e.o.MetalDirectDisplay = defaults.ToggleOff
			}
		}
	}
}

func (a *App) videoGroup(c *mui.Context, m mui.MenuParts, e *overrideEdit, tel Telemetry) {
	groupLabel(c, "Video", false)
	p := tel.Profile
	virtual := p != nil && p.VirtualDisplay
	shown := "Physical"
	if virtual {
		shown = "Virtual · VDD"
	}
	a.knobFlyout(c, m, e, knobVDD, theme.IconDisplay, "Display", shown, map[string][2]string{
		"auto": {"Auto", "virtual where the host can"}, "on": {"Virtual display", "SudoVDA"}, "off": {"Physical display", ""},
	})
	size := strings.ReplaceAll(resolutionLabel(tel.Width, tel.Height), " x ", "×")
	custom := ""
	if e.o.CustomWidth > 0 && e.o.CustomHeight > 0 {
		custom = fmt.Sprintf("%d×%d", e.o.CustomWidth, e.o.CustomHeight)
	}
	a.knobFlyout(c, m, e, knobResolution, theme.IconResolution, "Resolution", size, map[string][2]string{
		"auto": {"Auto", "match window"}, "1080p": {"1920 × 1080", ""}, "1440p": {"2560 × 1440", ""},
		"4k": {"3840 × 2160", "virtual only"}, "custom": {"Custom", custom},
	})
	codec := ""
	if p != nil {
		codec = codecName(p.Codec)
	}
	a.knobFlyout(c, m, e, knobCodec, theme.IconCodec, "Codec", codec, nil)
	a.colorTray(c, m, e, tel)
	fps := ""
	if p != nil && p.FPS > 0 {
		fps = fmt.Sprint(p.FPS)
	}
	a.knobFlyout(c, m, e, knobFPS, theme.IconFrameRate, "Frame rate", fps, nil)
	a.bitrateTray(c, m, e, tel)
	vs := e.o.VSyncMode
	a.tray(c, m, knobVSync.id, "VSync", rowParts{icon: theme.IconVSync, value: knobLabel(knobVSync, string(vs))}, func() {
		chips(c, m, "VSync", knobChoices(knobVSync, string(vs), func(v string) { e.o.VSyncMode = defaults.Toggle(v) }))
	})
}

// knobFlyout is a knob's flyout. value is what the stream resolved to,
// shown after "Auto · " while the knob is on auto; names relabel and
// detail the knob's choices.
func (a *App) knobFlyout(c *mui.Context, m mui.MenuParts, e *overrideEdit, k knob, icon *mui.SVG, label, value string, names map[string][2]string) {
	cur := *k.field(&e.o)
	shown := knobLabel(k, cur)
	if cur == defaults.Auto && value != "" {
		shown = "Auto · " + value
	}
	var choices []choice
	for i, v := range k.values {
		ch := choice{label: k.labels[i], selected: v == cur, pick: func() { *k.field(&e.o) = v }}
		if n, ok := names[v]; ok {
			ch.label, ch.detail = n[0], n[1]
		}
		choices = append(choices, ch)
	}
	a.flyout(c, m, label, rowParts{icon: icon, value: shown}, rebuildNote, choices)
}

func knobLabel(k knob, v string) string {
	if i := indexOf(k.values, v); i >= 0 {
		return k.labels[i]
	}
	return v
}

func knobChoices(k knob, cur string, set func(string)) []choice {
	out := make([]choice, len(k.values))
	for i, v := range k.values {
		out[i] = choice{label: k.labels[i], selected: v == cur, pick: func() { set(v) }}
	}
	return out
}

// colorPair is one of the colour tray's choices: a bit depth with a chroma.
type colorPair struct {
	depth  defaults.BitDepthPref
	chroma defaults.ChromaPref
}

func (p colorPair) label() string {
	ch := "4:2:0"
	if p.chroma == "444" {
		ch = "4:4:4"
	}
	return fmt.Sprintf("%s-bit %s", p.depth, ch)
}

var colorPairs = []colorPair{{"8", "420"}, {"10", "420"}, {"10", "444"}}

// colorTray sets bit depth and chroma together. The pair in force is the
// overrides' when both are set, else what the stream resolved to.
func (a *App) colorTray(c *mui.Context, m mui.MenuParts, e *overrideEdit, tel Telemetry) {
	cur := colorPair{e.o.BitDepth, e.o.Chroma}
	if cur.depth == defaults.Auto || cur.chroma == defaults.Auto {
		cur = resolvedColor(tel)
	}
	pairs := colorPairs
	if indexOf(pairs, cur) < 0 && cur.depth != defaults.Auto {
		pairs = append(pairs[:len(pairs):len(pairs)], cur)
	}
	reason := no444Reason(tel.Profile)
	var choices []choice
	for _, p := range pairs {
		ch := choice{label: p.label(), selected: p == cur, pick: func() { e.o.BitDepth, e.o.Chroma = p.depth, p.chroma }}
		if p.chroma == "444" && !ch.selected {
			ch.reason = reason
		}
		choices = append(choices, ch)
	}
	value := ""
	if cur.depth != defaults.Auto {
		value = cur.label()
	}
	a.tray(c, m, "color", "Color", rowParts{icon: theme.IconColor, value: value}, func() {
		chips(c, m, "Color", choices)
	})
}

func resolvedColor(tel Telemetry) colorPair {
	p := colorPair{depth: "8", chroma: "420"}
	switch {
	case tel.Profile != nil:
		if tel.Profile.BitDepth == 10 {
			p.depth = "10"
		}
		if tel.Profile.Chroma444 {
			p.chroma = "444"
		}
	case tel.Chroma != 0:
		if tel.Chroma.TenBit() {
			p.depth = "10"
		}
		if tel.Chroma.Full() {
			p.chroma = "444"
		}
	default:
		return colorPair{defaults.Auto, defaults.Auto}
	}
	return p
}

// no444Reason says why this stream cannot take 4:4:4, from what the host
// resolved: empty when it could.
func no444Reason(p *defaults.ResolvedProfile) string {
	if p == nil {
		return ""
	}
	for _, d := range p.Decisions {
		if d.Knob == "chroma" && d.Why == "4:4:4 not on both ends" {
			if p.EncoderAPI != "" {
				return "4:4:4 needs an encoder and a decoder that both do it. This host is encoding on " + p.EncoderAPI + ", without 4:4:4 here."
			}
			return "4:4:4 needs an encoder and a decoder that both do it, and this stream's do not."
		}
	}
	return ""
}

// bitrateTray is Auto, or a bitrate the slider sets.
func (a *App) bitrateTray(c *mui.Context, m mui.MenuParts, e *overrideEdit, tel Telemetry) {
	value := fmt.Sprintf("%d Mbps", e.o.BitrateMbps)
	if e.o.BitrateMbps == 0 {
		value = "Auto · " + formatMbps(tel.BitrateMbps) + " Mbps"
	}
	a.tray(c, m, "bitrate", "Bitrate", rowParts{icon: theme.IconBitrate, value: value}, func() {
		chips(c, m, "Bitrate", []choice{{label: "Auto", selected: e.o.BitrateMbps == 0, pick: func() { e.o.BitrateMbps = 0 }}})
		v := float64(e.o.BitrateMbps)
		if v == 0 {
			v = min(max(tel.BitrateMbps, minBitrateMbps), maxManualBitrateMbps)
		}
		if traySlider(c, "Video bitrate", &v, minBitrateMbps, maxManualBitrateMbps, e.o.BitrateMbps > 0,
			fmt.Sprint(minBitrateMbps), fmt.Sprint(maxManualBitrateMbps)+" Mbps") {
			e.o.BitrateMbps = max(int(math.Round(v/5)*5), minBitrateMbps)
		}
	})
}

func (a *App) soundGroup(c *mui.Context, m mui.MenuParts, s *Settings, tel Telemetry) {
	groupLabel(c, "Sound & input", false)
	level := fmt.Sprintf("%.0f %%", s.AudioVolume*100)
	if s.AudioMuted {
		level = "muted"
	}
	mic := "mic off"
	if !tel.MicMuted {
		mic = "mic on"
	}
	a.tray(c, m, "sound", "Sound & mic", rowParts{icon: theme.IconSound, value: level + " · " + mic}, func() {
		v := s.AudioVolume * 100
		if traySlider(c, "Volume", &v, 0, 100, !s.AudioMuted, "0", "100 %") {
			s.AudioVolume = math.Round(v) / 100
		}
		if switchItem(c, m, "Mute", s.AudioMuted, rowParts{icon: theme.IconSound}, "") {
			s.AudioMuted = !s.AudioMuted
		}
		if switchItem(c, m, "Microphone", !tel.MicMuted, rowParts{icon: theme.IconSound}, a.shortcut(false, "U")) {
			go a.ToggleMic()
		}
		if hint := a.micHint(tel); hint != "" {
			mui.Text(c, hint).Font(theme.FontUI).FontSize(theme.TextCaption).FixedLineHeight(15).TextColor(theme.RoleText3).FillWidth()
		}
	})
	both := s.ClipboardSend && s.ClipboardReceive
	clip := rowParts{icon: theme.IconClipboard}
	switch {
	case both:
	case s.ClipboardSend:
		clip.value = "send only"
	case s.ClipboardReceive:
		clip.value = "receive only"
	}
	if switchItem(c, m, "Shared clipboard", both, clip, "") {
		s.ClipboardSend, s.ClipboardReceive = !both, !both
	}
}

// micHint says where the microphone goes, or why it cannot.
func (a *App) micHint(t Telemetry) string {
	a.mu.Lock()
	err := a.micErr
	a.mu.Unlock()
	switch {
	case t.MicDevice != "" && t.MicMuted:
		return "Off · plays into " + t.MicDevice + " on the host"
	case t.MicDevice != "":
		return "Live in " + t.MicDevice + " on the host"
	case err != "":
		return err
	}
	return t.MicHint
}

func (a *App) overlayGroup(c *mui.Context, m mui.MenuParts) {
	o := &a.ovl
	groupLabel(c, "Overlay", false)
	caption := ""
	if o.vh < compactBelow {
		caption = "Compact under a 720 px window"
	}
	if switchItem(c, m, "Metrics window", o.metrics.shown, rowParts{icon: theme.IconMetrics, caption: caption}, a.shortcut(false, "M")) {
		o.metrics.toggle(o.vw, o.vh)
	}
	hide := m.Item()
	menuRow(c, hide, "Hide button", rowParts{icon: theme.IconHide, trailing: func() { badge(c, a.shortcut(false, "P")) }})
	if hide.Clicked() {
		a.toggleHidden()
	}
}

// menuFoot is Disconnect, the menu's one destructive action.
func (a *App) menuFoot(c *mui.Context, m mui.MenuParts) {
	mui.Row(c).FillWidth().Height(48).Padding(0, 8).AlignItems(mui.Center).BorderWidth(1, 0, 0, 0).BorderColor(theme.HUDDivider).Children(func() {
		d := m.Item().FillWidth().Height(32).Radius(theme.RadiusSm).Border(1, theme.RoleFault).Padding(0, 10).Gap(10).
			AlignItems(mui.Center).Label("Disconnect").Cursor(mui.CursorPointer)
		if d.Highlighted() {
			d.Background(theme.RoleFault.Alpha(0.12))
		}
		d.Children(func() {
			mui.Icon(c, theme.IconPower).Size(16, 16).TextColor(theme.RoleFault)
			mui.Text(c, "Disconnect").Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(600).TextColor(theme.RoleFault).Grow(1)
			mui.Row(c).Height(18).PaddingX(5).AlignItems(mui.Center).Radius(3).Border(1, theme.RoleFault.Alpha(0.5)).Children(func() {
				mui.Text(c, a.shortcut(true, "D")).Font(theme.FontMono).FontSize(theme.TextMini).TextColor(theme.RoleFault).SingleLine()
			})
		})
		if d.Clicked() {
			a.disconnect()
		}
	})
}
