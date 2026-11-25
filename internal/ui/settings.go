package ui

import (
	"strconv"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

const minBitrateMbps = 5

// settingsCategory is one of Settings' seven pages.
type settingsCategory int

const (
	catGeneral settingsCategory = iota
	catHost
	catVideo
	catAudio
	catInput
	catGamepad
	catNetwork
	numCategories
)

var categoryNames = [numCategories]string{"General", "Host", "Video & Display", "Audio", "Input & Hotkeys", "Gamepad", "Network"}

// settingsState is Settings' own state across frames.
type settingsState struct {
	cat settingsCategory
	// query is the search field; focusSearch focuses it on the next frame.
	query       string
	focusSearch bool
	// advancedClosed folds the advanced encoder group; portsOpen unfolds
	// the ports.
	advancedClosed bool
	portsOpen      bool
	// ports is the last TestPorts answer, for ports, taken when they
	// changed or when the ports came into view: another app may have
	// taken one while they were out of it. frame counts Settings' frames;
	// portsShownAt is the last one that showed the ports.
	ports        PortStatus
	portsTested  HostPorts
	frame        uint64
	portsShownAt uint64
	// revoking is the fingerprint whose row asks to confirm its revoke,
	// "*" for every device.
	revoking  string
	revokeErr string
	// wantFixed shows the fixed code's field before a code was kept.
	wantFixed bool
	// clientNameDraft and clientNameFocused edit the device name.
	clientNameDraft   string
	clientNameFocused bool
	// keys is the chord capture on Input & Hotkeys.
	keys hotkeyEdit
	// appAdding opens the field and picks that add an app to leave out of
	// the sound; appDraft is the field.
	appAdding bool
	appDraft  string
	// pad is the Gamepad page's pick and its last mapping message.
	pad gamepadEdit
}

// settingsFrame is one frame's edit of everything Settings changes; the
// page stores what differs from before at the end of the frame.
type settingsFrame struct {
	s, sBefore             Settings
	hs, hsBefore           HostSettings
	gp, gpBefore           GamepadSettings
	layout, layoutBefore   OverlayLayout
	e                      *overrideEdit
	query                  string
	hosting                HostingState
	stats                  SessionStats
	searching              bool
	firstHit, firstHitDone bool
	// devices is the backend's AudioDevices, and pads its Gamepads, asked
	// at most once a frame.
	devices *AudioDevices
	pads    *[]GamepadInfo
}

func (f *settingsFrame) audioDevices(a *App) AudioDevices {
	if f.devices == nil {
		d := a.backend.AudioDevices()
		f.devices = &d
	}
	return *f.devices
}

// settingEntry is one searchable setting: its words and how it draws.
type settingEntry struct {
	id    string
	cat   settingsCategory
	title string
	help  string
	// aliases are more words that find it.
	aliases string
	build   func(a *App, c *mui.Context, f *settingsFrame)
}

// matches: the entry's title, help or aliases hold q, which is lower case.
func (s settingEntry) matches(q string) bool {
	return strings.Contains(strings.ToLower(s.title+" "+s.help+" "+s.aliases), q)
}

// entryByID indexes settingEntries.
var entryByID = func() map[string]*settingEntry {
	m := map[string]*settingEntry{}
	for i := range settingEntries {
		m[settingEntries[i].id] = &settingEntries[i]
	}
	return m
}()

// hits are the entries that match q, by category.
func hits(q string) [numCategories][]*settingEntry {
	var out [numCategories][]*settingEntry
	for i := range settingEntries {
		if e := &settingEntries[i]; e.matches(q) {
			out[e.cat] = append(out[e.cat], e)
		}
	}
	return out
}

// categoryPages lay out each category's entries in two columns.
var categoryPages = [numCategories]func(a *App, c *mui.Context, f *settingsFrame){
	catGeneral: (*App).generalPage,
	catHost:    (*App).hostPage,
	catVideo:   (*App).videoPage,
	catAudio:   (*App).audioPage,
	catInput:   (*App).inputPage,
	catGamepad: (*App).gamepadPage,
	catNetwork: (*App).networkPage,
}

// settingsPadTop is one px under the other screens' 24: Settings' title is a
// 25 px display line, so its cap height centres where the 31 px titles' does.
const settingsPadTop = 25

func (a *App) settingsScreen(c *mui.Context) {
	f := &settingsFrame{s: a.backend.Settings(), hs: a.backend.HostSettings(), layout: a.backend.OverlayLayout(),
		hosting: a.backend.Hosting(), stats: a.backend.SessionStats(), gp: a.backend.GamepadSettings()}
	// An unset knob in host.json is auto: the dropdowns show it so.
	f.hs.Policy = f.hs.Policy.Normalize()
	f.sBefore, f.hsBefore, f.layoutBefore, f.gpBefore = f.s, f.hs, f.layout, f.gp
	f.e = a.editOverrides(a.settingsScope())
	if f.stats.Live && f.stats.HostID != "" && f.stats.HostID == f.e.scope.hostID {
		f.e.policy = &f.stats.HostPolicy
	}
	f.query = strings.ToLower(strings.TrimSpace(a.set.query))
	f.searching = f.query != ""
	a.set.frame++

	c.OnWindowShortcut(mui.Cmd, mui.KeyF, func() {
		a.set.focusSearch = true
		a.Notify()
	})
	mui.Row(c).Grow(1).FillHeight().Children(func() {
		a.settingsNav(c, f)
		mui.Scroll(c).Grow(1).FillHeight().Children(func() {
			mui.Column(c).FillWidth().MaxWidth(968).Padding(settingsPadTop, theme.Space6, theme.Space5).Gap(18).Children(func() {
				a.settingsError(c)
				if f.searching {
					a.searchResults(c, f)
					return
				}
				categoryPages[a.set.cat](a, c, f)
			})
		})
	})

	a.saveSettings(f.sBefore, f.s)
	a.saveOverrides(f.e)
	if f.hs != f.hsBefore {
		a.setSettingsErr(a.backend.SetHostSettings(f.hs))
	}
	if f.gp != f.gpBefore {
		a.setSettingsErr(a.backend.SetGamepadSettings(f.gp))
	}
	if f.layout != f.layoutBefore {
		a.setSettingsErr(a.backend.SaveOverlayLayout(f.layout))
	}
}

func (a *App) setSettingsErr(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	a.settingsErr = msg
	a.mu.Unlock()
}

func (a *App) settingsError(c *mui.Context) {
	a.mu.Lock()
	msg := a.settingsErr
	a.mu.Unlock()
	if a.drafts.err != "" {
		msg = a.drafts.err
	}
	if msg == "" {
		return
	}
	mui.Row(c).FillWidth().Padding(theme.Space3, theme.Space4).Radius(theme.RadiusSm).
		Border(1, theme.RoleFault).Children(func() {
		label(c, "Could not save settings: "+msg).TextColor(theme.RoleFault)
	})
}

// settingsNav is the search field and the seven categories, which turn
// into the search's result counts while it has text.
func (a *App) settingsNav(c *mui.Context, f *settingsFrame) {
	found := hits(f.query)
	mui.Column(c).Width(theme.SizeSidebar).Shrink(0).FillHeight().BorderEndWidth(1).BorderColor(theme.RoleBorder).
		Padding(theme.Space5, theme.Space4).Gap(theme.Space4).Children(func() {
		a.searchField(c, f, found)
		mui.Column(c).FillWidth().Gap(2).Children(func() {
			for cat := range numCategories {
				a.categoryRow(c, f, cat, len(found[cat]))
			}
		})
	})
}

func (a *App) searchField(c *mui.Context, f *settingsFrame, found [numCategories][]*settingEntry) {
	field := mui.Row(c).FillWidth().Height(34).PaddingStart(10).PaddingEnd(6).Gap(theme.Space2).AlignItems(mui.Center).
		Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
	var in mui.Element
	field.Children(func() {
		mui.Icon(c, theme.IconSearch).Size(14, 14).TextColor(theme.RoleText3)
		in = mui.TextInputBase(c.Key("settings-search"), &a.set.query).Grow(1).Shrink(1).FillHeight().
			Label("Search settings").Placeholder("Search settings…").PlaceholderColor(theme.RoleText3).
			FontSize(theme.TextBody).TextColor(theme.RoleText).CaretColor(theme.RoleAccent).
			OnShortcut(0, mui.KeyEscape, func() { a.set.query = "" })
		key := "Ctrl F"
		if a.goos == "darwin" {
			key = "⌘F"
		}
		mui.Box(c).Height(20).PaddingX(5).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).
			Background(theme.RoleWell).Border(1, theme.RoleBorder).Children(func() {
			mono(c, key).FontSize(theme.TextMini).TextColor(theme.RoleText3)
		})
	})
	if field.FocusWithin() {
		field.Border(1, theme.RoleFocus)
	}
	if a.set.focusSearch {
		a.set.focusSearch = false
		in.Focus()
	}
	if in.Submitted() {
		for cat := range numCategories {
			if len(found[cat]) > 0 {
				a.set.cat, a.set.query = cat, ""
				break
			}
		}
	}
}

func (a *App) categoryRow(c *mui.Context, f *settingsFrame, cat settingsCategory, n int) {
	name := categoryNames[cat]
	active := !f.searching && a.set.cat == cat
	b := mui.ButtonBase(c).FillWidth().Height(34).PaddingX(10).Radius(theme.RadiusSm).AlignItems(mui.Center).
		Label(name).Cursor(mui.CursorPointer).Selected(active).Disabled(f.searching && n == 0)
	col, weight := theme.RoleText2, 500
	switch {
	case active:
		b.Background(theme.RoleRaised)
		col, weight = theme.RoleText, 600
	case f.searching && n == 0:
		col = theme.RoleText3
	case b.Hovered():
		b.Background(theme.RoleHover)
	}
	b.Children(func() {
		label(c, name).FontWeight(weight).TextColor(col).Grow(1)
		if f.searching && n > 0 {
			mui.Box(c).Height(18).PaddingX(6).AlignItems(mui.Center).Radius(theme.RadiusXs).Background(theme.RoleWell).
				Children(func() { mono(c, strconv.Itoa(n)).FontSize(theme.TextMini).TextColor(theme.RoleText2) })
		}
	})
	if b.Clicked() {
		a.set.cat, a.set.query = cat, ""
	}
}

// searchResults shows the rows that match, under their categories.
func (a *App) searchResults(c *mui.Context, f *settingsFrame) {
	found := hits(f.query)
	total := 0
	for _, h := range found {
		total += len(h)
	}
	settingsHeader(c, "Search", strconv.Itoa(total)+" "+plural(total, "setting matches", "settings match")+" “"+strings.TrimSpace(a.set.query)+"”", nil)
	for cat, list := range found {
		if len(list) == 0 {
			continue
		}
		mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
			label(c, categoryNames[cat]).FontWeight(600).TextColor(theme.RoleText2)
			setPanel(c, "", nil, func() {
				for i, e := range list {
					if i > 0 {
						divider(c)
					}
					e.build(a, c, f)
				}
			})
		})
	}
}

// settingsHeader is a category's title, the scope it speaks for, and an
// optional control on the right.
func settingsHeader(c *mui.Context, title, sub string, trailing func()) {
	mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space4).Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			heading(c, title, theme.TextDisplay).FixedLineHeight(theme.LeadingDisplay)
			caption(c, sub).FontSize(12).FixedLineHeight(18)
		})
		if trailing != nil {
			trailing()
		}
	})
}

// columns lays a category out in two columns of panels.
func columns(c *mui.Context, left, right float32, l, r func()) {
	mui.Row(c).FillWidth().Gap(theme.Space4).AlignItems(mui.Start).Children(func() {
		mui.Column(c).Grow(left).Basis(0).MinWidth(0).Gap(14).Children(l)
		mui.Column(c).Grow(right).Basis(0).MinWidth(0).Gap(14).Children(r)
	})
}

// setPanel is a Settings panel: a section label, an optional tag on its
// right, and its rows.
func setPanel(c *mui.Context, title string, tag func(), body func()) mui.Element {
	return mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).
		Radius(theme.RadiusMd).Padding(theme.Space3, theme.Space4).Gap(theme.Space2).Children(func() {
		if title != "" || tag != nil {
			mui.Row(c).FillWidth().MinHeight(20).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
				if title != "" {
					sectionLabel(c, title)
				}
				mui.Spacer(c)
				if tag != nil {
					tag()
				}
			})
		}
		body()
	})
}

// entries draws the rows ids name, a rule between each.
func (a *App) entries(c *mui.Context, f *settingsFrame, ids ...string) {
	for i, id := range ids {
		if i > 0 {
			divider(c)
		}
		entryByID[id].build(a, c, f)
	}
}

// title draws a row's title, the search's match in full text against the
// rest.
func (f *settingsFrame) title(c *mui.Context, s string, col mui.Color) {
	i := -1
	if f.searching {
		i = strings.Index(strings.ToLower(s), f.query)
	}
	if i < 0 {
		label(c, s).FontWeight(500).TextColor(col)
		return
	}
	j := i + len(f.query)
	mui.Row(c).Shrink(1).Children(func() {
		for _, part := range []struct {
			s   string
			col mui.Color
		}{{s[:i], theme.RoleText2}, {s[i:j], theme.RoleText}, {s[j:], theme.RoleText2}} {
			if part.s != "" {
				label(c, part.s).FontWeight(500).TextColor(part.col).SingleLine()
			}
		}
	})
}

// row is a setting: title and help on the left, its control on the right.
func (f *settingsFrame) row(c *mui.Context, title, help string, control func()) {
	mui.Row(c).FillWidth().MinHeight(36).AlignItems(mui.Center).Gap(theme.Space4).Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			f.title(c, title, theme.RoleText)
			if help != "" {
				caption(c, help).FixedLineHeight(theme.LeadingCaption)
			}
		})
		control()
	})
}

// laneRow is a setting whose title keeps a fixed lane and whose control
// takes the rest.
func (f *settingsFrame) laneRow(c *mui.Context, title string, lane float32, control func()) {
	mui.Row(c).FillWidth().MinHeight(36).AlignItems(mui.Center).Gap(theme.Space3).Children(func() {
		mui.Box(c).Width(lane).Shrink(0).Children(func() { f.title(c, title, theme.RoleText) })
		mui.Row(c).Grow(1).Shrink(1).AlignItems(mui.Center).Children(control)
	})
}

// switchRow is a whole row that flips *on: its title names the switch for
// assistive technology. It reports whether it changed.
func (f *settingsFrame) switchRow(c *mui.Context, title, help string, on *bool) bool {
	r := mui.SwitchBase(c.Key("switch:"+title), on).FillWidth().MinHeight(36).AlignItems(mui.Center).Gap(theme.Space4).
		Label(title).Cursor(mui.CursorPointer)
	v := *on
	r.Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			f.title(c, title, theme.RoleText)
			if help != "" {
				caption(c, help).FixedLineHeight(theme.LeadingCaption)
			}
		})
		settingsSwitch(c, v, false)
	})
	return r.Changed()
}

// unavailableRow is a switch this build cannot turn on yet, with why.
func (f *settingsFrame) unavailableRow(c *mui.Context, title, reason string) {
	mui.Row(c).FillWidth().MinHeight(36).AlignItems(mui.Center).Gap(theme.Space4).Label(title).Disabled(true).Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			f.title(c, title, theme.RoleText3)
			caption(c, reason).FixedLineHeight(theme.LeadingCaption)
		})
		settingsSwitch(c, false, true)
	})
}

// settingsSwitch draws the 34 x 20 switch: ember with an ink knob when on,
// a strong hairline when off, and the plain border when disabled.
func settingsSwitch(c *mui.Context, on, disabled bool) {
	mui.Box(c).Size(34, 20).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		switch {
		case on:
			const knob = 14
			p.Fill(r, theme.RoleAccent, r.H/2)
			p.Fill(mui.Rect{X: r.X + r.W - knob - 3, Y: r.Y + 3, W: knob, H: knob}, theme.RoleOnAccent, knob/2)
		default:
			const knob = 12
			col := theme.RoleBorderStrong
			if disabled {
				col = theme.RoleBorder
			}
			p.Stroke(r, col, r.H/2, 1)
			p.Fill(mui.Rect{X: r.X + 4, Y: r.Y + 4, W: knob, H: knob}, col, knob/2)
		}
	})
}

// chip is a choice chip, checked or not; it reports a click.
func chip(c *mui.Context, name, text string, checked bool, height float32) bool {
	b := mui.ButtonBase(c).Height(height).PaddingX(10).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusXs).
		Label(name).Checked(checked).Cursor(mui.CursorPointer)
	col := theme.RoleText3
	if checked {
		b.Background(theme.RoleHover).Border(1, theme.RoleBorderStrong)
		col = theme.RoleText
	} else {
		b.Border(1, theme.RoleBorder)
		if b.Hovered() {
			b.Background(theme.RoleRaised)
		}
	}
	b.Children(func() {
		if checked {
			checkMark(c, 11, theme.RoleText)
		}
		mono(c, text).FontSize(theme.TextCaption).TextColor(col)
	})
	return b.Clicked()
}

// dropdown picks one of values, by labels, into *v.
func dropdown[T comparable](c *mui.Context, name string, v *T, values []T, labels []string, width float32) bool {
	sel := mui.SelectBase(c.Key("dropdown:"+name), v)
	cur := ""
	if i := indexOf(values, *v); i >= 0 {
		cur = labels[i]
	}
	t := sel.Trigger.Height(theme.SizeControl).PaddingX(10).Gap(theme.Space2).AlignItems(mui.Center).
		Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong).Label(name).Cursor(mui.CursorPointer)
	if width > 0 {
		t.Width(width)
	} else {
		t.Grow(1)
	}
	t.Children(func() {
		mono(c, cur).FontSize(12).Grow(1).Shrink(1)
		mui.Icon(c, theme.IconChevron).Size(12, 12).TextColor(theme.RoleText3)
	})
	sel.Popup(func(panel mui.Element) {
		panel.Padding(4).Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong).
			Shadow(0, 16, 40, 0, theme.FloatShadow)
		for i, val := range values {
			item := sel.Item(val).Height(28).PaddingX(10).AlignItems(mui.Center).Radius(theme.RadiusXs).Label(labels[i])
			if item.Highlighted() {
				item.Background(theme.RoleHover)
			}
			item.Children(func() { mono(c, labels[i]).FontSize(12) })
		}
	})
	return t.Changed()
}

// lockedControl stands in for a control the host's policy sets: the value,
// greyed, and a badge that says why.
func lockedControl(c *mui.Context, name, value, host string) {
	mui.Row(c).Grow(1).Height(theme.SizeControl).PaddingStart(10).PaddingEnd(4).Gap(theme.Space2).AlignItems(mui.Center).
		Radius(theme.RadiusSm).Border(1, theme.RoleBorder).Label(name + ": " + value + ", set by host").Children(func() {
		mono(c, value).FontSize(12).TextColor(theme.RoleText3).Grow(1)
		badge := mui.Row(c).Height(22).PaddingX(theme.Space2).Gap(5).AlignItems(mui.Center).Radius(theme.RadiusXs).
			Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong)
		badge.Children(func() {
			mui.Icon(c, theme.IconLock).Size(11, 11).TextColor(theme.RoleText2)
			label(c, "Set by host").FontSize(theme.TextCaption).FontWeight(500).TextColor(theme.RoleText2)
		})
		tooltip(c, badge, func() {
			label(c, host+" sets "+strings.ToLower(name)+" to "+value+" for every client.").FontSize(12)
		})
	})
}

// infoWell is a well with the info glyph, for why a choice will not take.
func infoWell(c *mui.Context, text string) {
	mui.Row(c).FillWidth().Padding(10, theme.Space3).Gap(10).AlignItems(mui.Start).Radius(theme.RadiusSm).
		Background(theme.RoleWell).Children(func() {
		mui.Icon(c, theme.IconInfo).Size(14, 14).TextColor(theme.RoleText2).Margin(1, 0, 0)
		caption(c, text).TextColor(theme.RoleText2).Grow(1).Shrink(1)
	})
}

// warningLine is a fault in a row, such as a port another app holds.
func warningLine(c *mui.Context, text string) {
	mui.Row(c).FillWidth().Gap(6).AlignItems(mui.Start).Children(func() {
		mui.Icon(c, theme.IconWarning).Size(12, 12).TextColor(theme.RoleFault).Margin(2, 0, 0)
		caption(c, text).TextColor(theme.RoleFault).Grow(1).Shrink(1)
	})
}

// textLink is an underlined caption that acts.
func textLink(c *mui.Context, text string, col mui.Color) bool {
	b := mui.ButtonBase(c).Label(text).Cursor(mui.CursorPointer)
	b.Children(func() { caption(c, text).TextColor(col).Underline() })
	return b.Clicked()
}
