package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
)

// calibrateUI is the Auto-tune window's own state; the run it shows is the
// backend's Calibration.
type calibrateUI struct {
	open             bool
	hostID, hostName string
	// startErr is why StartCalibration refused, actErr why Apply or Reset
	// did, and note what the last of them did.
	startErr, actErr, note string
	applied                bool
}

const (
	calModalWidth = 840
	calPad        = theme.Space5
)

// calPhases are the stepper's stops, in the order a run takes them.
var calPhases = []struct {
	phase CalibrationPhase
	name  string
}{
	{CalibratingNetwork, "Network"},
	{CalibratingCodec, "Codec"},
	{CalibratingDisplay, "Display"},
	{CalibrationFinished, "Done"},
}

// openCalibration opens the Auto-tune window on a host and starts the run.
func (a *App) openCalibration(hostID, hostName string) {
	a.cal = calibrateUI{open: true, hostID: hostID, hostName: hostName}
	a.startCalibration()
}

// startCalibration (re)starts the run; a run already measuring this host is
// joined, not refused.
func (a *App) startCalibration() {
	a.cal.startErr, a.cal.actErr, a.cal.note, a.cal.applied = "", "", "", false
	err := a.backend.StartCalibration(a.cal.hostID)
	if st := a.backend.Calibration(); errors.Is(err, ErrCalibrating) && st.Running && st.HostID == a.cal.hostID {
		return
	}
	if err != nil {
		a.cal.startErr = err.Error()
	}
}

// closeCalibration closes the window; a run measuring ends only with Cancel.
func (a *App) closeCalibration() {
	if a.backend.Calibration().Running && a.cal.startErr == "" {
		return
	}
	a.cal = calibrateUI{}
}

func (a *App) applyCalibration() {
	if err := a.backend.ApplyCalibration(a.cal.hostID); err != nil {
		a.cal.actErr = err.Error()
		return
	}
	a.cal.actErr, a.cal.applied = "", true
	a.cal.note = a.cal.hostName + " streams with these settings on this route wherever the stream format is Auto."
}

// calSummary is the kept calibration a host's card shows: the one on its
// current route, else the first.
func (a *App) calSummary(h HostInfo) (CalibrationSummary, bool) {
	all := a.backend.CalibrationStatus(h.ID)
	for _, s := range all {
		if s.PathKind == h.PathKind {
			return s, true
		}
	}
	if len(all) > 0 {
		return all[0], true
	}
	return CalibrationSummary{}, false
}

// calStatusLine is the host card's line: what is kept for the route, and
// the way to tune it.
func (a *App) calStatusLine(c *mui.Context, h HostInfo, st cardState) {
	sum, tuned := a.calSummary(h)
	streaming := h.Connected || a.streaming
	if !tuned && (st == cardOffline || st == cardUnpaired) {
		return
	}
	name := "Auto-tune " + h.Name
	disabled := streaming
	if disabled {
		name += ", end the stream first"
	}
	b := mui.ButtonBase(c).Label(name).Disabled(disabled).FillWidth().Height(16).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusXs).
		Cursor(mui.CursorPointer)
	b.Children(func() {
		col := theme.RoleText3
		text := "Not tuned"
		switch {
		case tuned:
			col = theme.RoleDirect
			if len(sum.Warnings) > 0 {
				col = theme.RoleRelay
			}
			text = "Tuned · " + routeShort(sum.PathKind)
			if sum.GlassMs > 0 {
				text = "Tuned · " + formatMs(sum.GlassMs) + " ms · " + routeShort(sum.PathKind)
			}
		case streaming:
			text = "End the stream to auto-tune"
		}
		mui.Box(c).Size(6, 6).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
			if tuned {
				p.Fill(r, col, 3)
			} else {
				p.Stroke(r, col, 3, 1.5)
			}
		})
		mono(c, text).FontSize(theme.TextCaption).TextColor(col).Shrink(1)
		mui.Spacer(c)
		if !streaming && st != cardOffline {
			action, ac := "Auto-tune", theme.RoleAccent
			if tuned {
				action = "Re-tune"
			}
			if b.Hovered() {
				ac = theme.RoleAccentHover
			}
			caption(c, action).TextColor(ac).FontWeight(600).Underline().SingleLine().Shrink(0)
		}
	})
	if b.Clicked() && !disabled && st != cardOffline {
		a.openCalibration(h.ID, h.Name)
	}
}

// autoTunePanel is Settings → Video & Display's Auto-tune panel for the
// host whose stream format is on screen.
func (a *App) autoTunePanel(c *mui.Context, e *overrideEdit) {
	setPanel(c, "Auto-tune", nil, func() {
		if e.scope.all() {
			infoWell(c, "Choose a host under “Stream format for” to auto-tune it. Calibration is kept per host and route.")
			return
		}
		var host HostInfo
		for _, h := range a.backend.Hosts() {
			if h.ID == e.scope.hostID {
				host = h
			}
		}
		kept := a.backend.CalibrationStatus(e.scope.hostID)
		if len(kept) == 0 {
			caption(c, e.scope.name+" is not tuned. Auto-tune measures the route and picks the codec, slices, vsync and bitrate for it.").
				TextColor(theme.RoleText2).FillWidth()
		}
		for _, s := range kept {
			calKeptRow(c, s)
		}
		streaming := host.Connected || a.streaming
		blocked := ""
		switch {
		case streaming:
			blocked = "End the stream to auto-tune."
		case !host.Online:
			blocked = e.scope.name + " is offline."
		}
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Margin(theme.Space1, 0, 0).Children(func() {
			if button(c, buttonSecondary, "Auto-tune", false).Disabled(blocked != "").Clicked() {
				a.openCalibration(e.scope.hostID, e.scope.name)
			}
			if len(kept) > 0 {
				if button(c, buttonDestructive, "Reset calibration", false).Clicked() {
					msg := ""
					if err := a.backend.ResetCalibration(e.scope.hostID); err != nil {
						msg = err.Error()
					}
					a.mu.Lock()
					a.settingsErr = msg
					a.mu.Unlock()
				}
			}
			if blocked != "" {
				caption(c, blocked).Shrink(1)
			}
		})
	})
}

// calKeptRow is one kept calibration: its route and what it measured.
func calKeptRow(c *mui.Context, s CalibrationSummary) {
	mui.Column(c).FillWidth().Gap(theme.Space2).Padding(theme.Space2, theme.Space3).Radius(theme.RadiusSm).
		Background(theme.RoleWell).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			routeChip(c, s.PathKind)
			mono(c, "tuned "+s.At.Local().Format("Jan 2, 15:04")).FontSize(theme.TextCaption).TextColor(theme.RoleText2)
		})
		var parts []string
		if s.GlassMs > 0 {
			parts = append(parts, formatMs(s.GlassMs)+" ms to glass")
		}
		if s.RTTMs > 0 {
			parts = append(parts, "round trip "+formatMs(s.RTTMs)+" ms")
		}
		if s.BandwidthMbps > 0 {
			parts = append(parts, strconv.Itoa(s.BandwidthMbps)+" Mbps")
		}
		if s.QueueBytes > 0 {
			parts = append(parts, fmt.Sprintf("queue %d KB, drains %d Mbps", s.QueueBytes/1024, s.DrainMbps))
		}
		mui.Row(c).FillWidth().Gap(theme.Space3).Wrap().Children(func() {
			for _, p := range parts {
				mono(c, p).FontSize(theme.TextCaption).TextColor(theme.RoleText2)
			}
		})
		if chips := recommendedChips(s.Overrides); len(chips) > 0 {
			caption(c, "Picks "+strings.Join(chips, ", ")).TextColor(theme.RoleText3).FillWidth()
		}
		for _, w := range s.Warnings {
			calCallout(c, w)
		}
	})
}

// routeChip is the route's name in the signal colour of its kind.
func routeChip(c *mui.Context, k discovery.PathKind) {
	col := signalColor(k)
	mui.Row(c).Height(20).PaddingX(theme.Space2).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusPill).
		Background(theme.RoleRaised).Border(1, theme.RoleBorder).Shrink(0).Children(func() {
		routeMeter(c, pathBars(k), col)
		mui.Text(c, pathLabel(k)).Font(theme.FontUI).FontSize(theme.TextCaption).FontWeight(500).TextColor(col).SingleLine()
	})
}

// calCallout is an advisory: something that costs latency, and its fix.
func calCallout(c *mui.Context, text string) {
	mui.Row(c).FillWidth().Gap(10).Padding(10, theme.Space3).AlignItems(mui.Start).Radius(theme.RadiusSm).
		Background(theme.RoleRelay.Alpha(0.08)).Border(1, theme.RoleRelay.Alpha(0.55)).Label("Warning: " + text).Children(func() {
		mui.Icon(c, theme.IconWarning).Size(14, 14).TextColor(theme.RoleRelay).Margin(1, 0, 0).Shrink(0)
		mui.Text(c, text).Font(theme.FontUI).FontSize(theme.TextBody - 1).TextColor(theme.RoleText).Grow(1).Shrink(1)
	})
}

// recommendedChips names the knobs a calibration settled, as "Codec HEVC".
func recommendedChips(o defaults.UserOverrides) []string {
	var out []string
	for _, k := range allKnobs {
		v := *k.field(&o)
		if v == "" || v == defaults.Auto {
			continue
		}
		val := v
		if i := indexOf(k.values, v); i >= 0 {
			val = k.labels[i]
		}
		if k.id == "resolution" && v == "custom" && o.CustomWidth > 0 {
			val = fmt.Sprintf("%d×%d", o.CustomWidth, o.CustomHeight)
		}
		out = append(out, k.title+" "+val)
	}
	if v := string(o.PhaseLock); v != "" && v != defaults.Auto {
		out = append(out, "Phase lock "+strings.ToUpper(v[:1])+v[1:])
	}
	if o.BitrateMbps > 0 {
		out = append(out, fmt.Sprintf("Bitrate %d Mbps", o.BitrateMbps))
	}
	return out
}

// calibrationModal draws the Auto-tune window over the launcher.
func (a *App) calibrationModal(c *mui.Context) {
	if !a.cal.open {
		return
	}
	st := a.backend.Calibration()
	if st.HostID != a.cal.hostID {
		st = CalibrationState{HostID: a.cal.hostID, HostName: a.cal.hostName, Running: a.cal.startErr == "", Phase: CalibratingNetwork, Step: "Starting"}
	}
	kept := a.backend.CalibrationStatus(a.cal.hostID)
	w, h := c.Size()
	width := min(calModalWidth, w-2*theme.Space6)
	running := st.Running && a.cal.startErr == ""

	scrim := mui.Column(c.Key("calibration-scrim")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
		Background(theme.RoleGround.Alpha(0.78)).AlignItems(mui.Center).Justify(mui.Center)
	scrim.Children(func() {
		panel := mui.Column(c).Width(width).MaxHeight(h-2*theme.Space6).Radius(theme.RadiusMd).Clip().
			Background(theme.RoleSurface).Border(1, theme.RoleBorderStrong).Shadow(0, 16, 40, 0, theme.FloatShadow).
			Label("Auto-tune "+a.cal.hostName).OnShortcut(0, mui.KeyEscape, a.closeCalibration)
		panel.Children(func() {
			a.calHeader(c, st, running)
			divider(c)
			mui.Scroll(c).FillWidth().Shrink(1).Children(func() {
				mui.Column(c).FillWidth().Padding(calPad).Gap(theme.Space4).Children(func() {
					a.calBody(c, st, running)
				})
			})
			divider(c)
			a.calFooter(c, st, running, len(kept) > 0)
		})
	})
}

func (a *App) calHeader(c *mui.Context, st CalibrationState, running bool) {
	mui.Column(c).FillWidth().Padding(theme.Space4, calPad).Gap(theme.Space3).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Center).Children(func() {
			heading(c, "Auto-tune", theme.TextHeading).FixedLineHeight(theme.LeadingHeading).SingleLine()
			mono(c, a.cal.hostName).FontSize(theme.TextBody).TextColor(theme.RoleText2).Shrink(1)
			mui.Spacer(c)
			status, col := "Measuring", theme.RoleAccent
			switch {
			case a.cal.startErr != "" || st.Err != "":
				status, col = "Failed", theme.RoleFault
			case st.Cancelled:
				status, col = "Cancelled", theme.RoleText3
			case st.Result != nil:
				status, col = "Finished", theme.RoleDirect
			case !running:
				status, col = "Ready", theme.RoleText3
			}
			mono(c, status).FontSize(theme.TextCaption).TextColor(col)
		})
		if a.cal.startErr == "" && (running || st.Result != nil || st.Err != "" || st.Cancelled) {
			calStepper(c, st)
		}
	})
}

// calStepper is the run's phases as four slices, then the step and the
// run's share done.
func calStepper(c *mui.Context, st CalibrationState) {
	cur := 0
	for i, p := range calPhases {
		if p.phase == st.Phase {
			cur = i
		}
	}
	finished := st.Result != nil
	mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
		for i, p := range calPhases {
			col, tcol, weight := theme.RoleBorderStrong, theme.RoleText3, 500
			switch {
			case finished:
				col, tcol = theme.RoleDirect, theme.RoleText2
			case i < cur:
				col, tcol = theme.RoleText2, theme.RoleText2
			case i == cur && !st.Cancelled && st.Err == "":
				col, tcol, weight = theme.RoleAccent, theme.RoleText, 600
			case i == cur:
				col = theme.RoleFault
			}
			state := "pending"
			switch {
			case finished || i < cur:
				state = "done"
			case i == cur:
				state = "active"
			}
			mui.Column(c).Grow(1).Basis(0).Gap(6).Label("Phase " + p.name + ", " + state).Children(func() {
				mui.Box(c).FillWidth().Height(4).Radius(1).Background(col)
				mui.Row(c).Gap(6).AlignItems(mui.Baseline).Children(func() {
					mono(c, strconv.Itoa(i+1)).FontSize(theme.TextMini).TextColor(tcol)
					mui.Text(c, p.name).Font(theme.FontUI).FontSize(theme.TextCaption + 1).FontWeight(weight).TextColor(tcol).SingleLine()
				})
			})
		}
	})
	if finished || st.Err != "" || st.Cancelled {
		return
	}
	pct := min(max(st.Percent, 0), 100)
	mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Center).Children(func() {
		mui.Box(c).Grow(1).Height(6).Label("Progress " + strconv.Itoa(pct) + " percent").Draw(func(p *mui.Painter, r mui.Rect) {
			p.Fill(r, theme.RoleWell, 3)
			if pct > 0 {
				p.Fill(mui.Rect{X: r.X, Y: r.Y, W: max(r.W*float32(pct)/100, 6), H: r.H}, theme.RoleAccent, 3)
			}
		})
		mono(c, strconv.Itoa(pct)+"%").FontSize(theme.TextBody).TextColor(theme.RoleText).Width(40).Shrink(0)
	})
	if st.Step != "" {
		secondary(c, st.Step).FillWidth().SingleLine()
	}
}

func (a *App) calBody(c *mui.Context, st CalibrationState, running bool) {
	if a.cal.startErr != "" {
		calFailure(c, "Could not start", a.cal.startErr)
		return
	}
	if st.Err != "" {
		calFailure(c, "Calibration failed", st.Err)
	}
	if st.Cancelled {
		infoWell(c, "Calibration cancelled. Nothing was kept; the settings you had are unchanged.")
	}
	if r := st.Result; r != nil {
		calOutcome(c, r.BeforeMs, r.AfterMs, r.Display.Status == CalibrationDisplayMeasured, r.Recommended)
		for _, w := range r.Warnings {
			calCallout(c, w)
		}
		calLink(c, &r.Link)
		calCandidates(c, r.Candidates)
		calDisplay(c, &r.Display)
	} else {
		switch {
		case st.Link != nil:
			calLink(c, st.Link)
		case running:
			caption(c, "Measuring the route to "+a.cal.hostName+"…").TextColor(theme.RoleText2)
		}
		calCandidates(c, st.Candidates)
		if st.Display != nil {
			calDisplay(c, st.Display)
		}
	}
	if a.cal.note != "" {
		mui.Row(c).FillWidth().Gap(10).Padding(10, theme.Space3).AlignItems(mui.Start).Radius(theme.RadiusSm).
			Background(theme.RoleDirect.Alpha(0.10)).Border(1, theme.RoleDirect.Alpha(0.5)).Children(func() {
			mui.Icon(c, theme.IconShieldCheck).Size(14, 14).TextColor(theme.RoleDirect).Margin(1, 0, 0).Shrink(0)
			label(c, a.cal.note).Grow(1).Shrink(1)
		})
	}
	if a.cal.actErr != "" {
		warningLine(c, a.cal.actErr)
	}
}

func calFailure(c *mui.Context, title, msg string) {
	mui.Row(c).FillWidth().Gap(10).Padding(theme.Space3, theme.Space4).AlignItems(mui.Start).Radius(theme.RadiusSm).
		Background(theme.RoleFault.Alpha(0.08)).Border(1, theme.RoleFault).Label(title + ": " + msg).Children(func() {
		mui.Icon(c, theme.IconWarning).Size(16, 16).TextColor(theme.RoleFault).Margin(1, 0, 0).Shrink(0)
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			label(c, title).FontWeight(600).TextColor(theme.RoleFault)
			label(c, msg).TextColor(theme.RoleText)
		})
	})
}

// calSection is a titled block of the window.
func calSection(c *mui.Context, title string, tag string, body func()) {
	mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Baseline).Children(func() {
			sectionLabel(c, title)
			if tag != "" {
				mono(c, tag).FontSize(theme.TextMini).TextColor(theme.RoleText3)
			}
		})
		body()
	})
}

// calOutcome is the finished run's headline: the default's latency, the
// pick's, and the settings that make the difference.
// calOutcome shows the before and after p50s: to glass when the display
// phase measured it, otherwise to the decoded picture.
func calOutcome(c *mui.Context, before, after float64, toGlass bool, rec defaults.UserOverrides) {
	ms := func(v float64) string {
		if v <= 0 {
			return "—"
		}
		return formatMs(v)
	}
	span, note := "capture to decode", "p50 capture to decode: from the host’s capture to the decoded picture. The display phase did not measure the time to glass."
	if toGlass {
		span, note = "to glass", "p50 to glass: from the host’s capture to the picture on this screen."
	}
	mui.Column(c).FillWidth().Gap(theme.Space3).Padding(theme.Space4, calPad).Radius(theme.RadiusMd).
		Background(theme.RoleWell).Border(1, theme.RoleBorder.Mix(theme.RoleAccent, 0.35)).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space5).AlignItems(mui.End).Wrap().Children(func() {
			mui.Column(c).Gap(2).Label("Before " + ms(before) + " milliseconds " + span).Children(func() {
				caption(c, "BEFORE · DEFAULTS")
				mui.Row(c).Gap(6).AlignItems(mui.Baseline).Children(func() {
					mono(c, ms(before)).FontSize(theme.TextDisplay).TextColor(theme.RoleText2)
					mono(c, "ms").FontSize(theme.TextBody).TextColor(theme.RoleText3)
				})
			})
			mui.Icon(c, theme.IconChevronRight).Size(20, 20).TextColor(theme.RoleText3).Margin(0, 0, 6)
			mui.Column(c).Gap(2).Label("After " + ms(after) + " milliseconds " + span).Children(func() {
				caption(c, "AFTER · AUTO-TUNED").TextColor(theme.RoleAccent)
				mui.Row(c).Gap(6).AlignItems(mui.Baseline).Children(func() {
					mono(c, ms(after)).FontSize(theme.TextReadout).TextColor(theme.RoleAccent)
					mono(c, "ms").FontSize(theme.TextBody).TextColor(theme.RoleText3)
				})
			})
			mui.Spacer(c)
			if before > 0 && after > 0 {
				d := before - after
				text, col := fmt.Sprintf("%s ms faster · %.0f%% lower", formatMs(d), 100*d/before), theme.RoleDirect
				if d < 0.05 {
					text, col = "No faster than the defaults", theme.RoleText2
				}
				mono(c, text).FontSize(theme.TextBody).TextColor(col).Margin(0, 0, 6)
			}
		})
		caption(c, note).FillWidth()
		divider(c)
		chips := recommendedChips(rec)
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Wrap().Children(func() {
			label(c, "Recommended").FontWeight(600).FontSize(12).TextColor(theme.RoleText2)
			if len(chips) == 0 {
				caption(c, "the defaults are already the best pick on this route")
			}
			for _, ch := range chips {
				mui.Box(c).Height(22).PaddingX(theme.Space2).AlignItems(mui.Center).Radius(theme.RadiusXs).
					Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong).Children(func() {
					mono(c, ch).FontSize(theme.TextCaption)
				})
			}
		})
	})
}

// calLink is the route as calibration measured it.
func calLink(c *mui.Context, l *CalibrationLink) {
	queue := "no limit"
	if l.QueueDatagrams > 0 {
		queue = fmt.Sprintf("%d dgrams · %d Mbps", l.QueueDatagrams, l.DrainMbps)
	}
	rtt := formatMs(l.RTT.P50) + " ms"
	if l.RTT.P99 > 0 {
		rtt += " · p99 " + formatMs(l.RTT.P99)
	}
	lossCol := theme.RoleText
	if l.LossPct >= 1 {
		lossCol = theme.RoleRelay
	}
	calSection(c, "ROUTE", "", func() {
		mui.Row(c).FillWidth().Gap(theme.Space2).Wrap().Children(func() {
			calTile(c, "Path", func() { routeChip(c, l.PathKind) })
			calTile(c, "Round trip", func() { calValue(c, rtt, theme.RoleText) })
			calTile(c, "Jitter", func() { calValue(c, formatMs(l.JitterMs)+" ms", theme.RoleText) })
			calTile(c, "Bandwidth", func() { calValue(c, formatMbps(l.BandwidthMbps)+" Mbps", theme.RoleText) })
			calTile(c, "Loss", func() { calValue(c, fmt.Sprintf("%.1f%%", l.LossPct), lossCol) })
			calTile(c, "Receive queue", func() { calValue(c, queue, theme.RoleText) })
		})
	})
}

func calValue(c *mui.Context, s string, col mui.Color) {
	mono(c, s).FontSize(theme.TextBody).TextColor(col)
}

func calTile(c *mui.Context, title string, body func()) {
	mui.Column(c).Height(52).PaddingX(theme.Space3).Gap(6).Justify(mui.Center).Radius(theme.RadiusSm).
		Background(theme.RoleWell).Label("Route " + strings.ToLower(title)).Children(func() {
		caption(c, title).SingleLine()
		body()
	})
}

// calCol is a table column; width 0 takes what the others leave.
type calCol struct {
	title string
	width float32
	right bool
}

// calCell is a table cell, in a colour when tinted.
type calCell struct {
	text string
	col  mui.Color
	tint bool
}

func plain(s string) calCell { return calCell{text: s} }

func tinted(s string, col mui.Color) calCell { return calCell{text: s, col: col, tint: true} }

type calRow struct {
	cells []calCell
	// err replaces the measurements of a row that did not run.
	err  string
	best bool
}

// calTable draws rows under cols, the best row marked with the ember.
func calTable(c *mui.Context, name string, cols []calCol, rows []calRow) {
	cell := func(i int, text string, col mui.Color, size float32, weight int) {
		cc := cols[i]
		box := mui.Row(c).Height(26).AlignItems(mui.Center).Shrink(0)
		if cc.width == 0 {
			box.Grow(1).Shrink(1).MinWidth(0)
		} else {
			box.Width(cc.width)
		}
		if cc.right {
			box.Justify(mui.End)
		}
		box.PaddingEnd(theme.Space2).Children(func() {
			mono(c, text).FontSize(size).TextColor(col).FontWeight(weight)
		})
	}
	mui.Column(c).FillWidth().Radius(theme.RadiusSm).Border(1, theme.RoleBorder).Clip().Label(name).Children(func() {
		mui.Row(c).FillWidth().PaddingX(theme.Space3).Background(theme.RoleWell).Children(func() {
			for i, cc := range cols {
				cell(i, cc.title, theme.RoleText3, theme.TextMini, 500)
			}
		})
		for _, r := range rows {
			row := mui.Row(c).FillWidth().PaddingX(theme.Space3).AlignItems(mui.Center)
			if r.best {
				row.Background(theme.RoleAccent.Alpha(0.10))
			}
			row.Children(func() {
				if r.best {
					mui.Box(c).Absolute().Left(0).Top(0).Bottom(0).Width(2).Background(theme.RoleAccent)
				}
				for i := range cols {
					text, col := "", theme.RoleText
					if i < len(r.cells) {
						text = r.cells[i].text
						if r.cells[i].tint {
							col = r.cells[i].col
						}
					}
					if r.err != "" && i > 0 {
						if i == 1 {
							mui.Row(c).Grow(1).Height(26).AlignItems(mui.Center).Children(func() {
								mono(c, r.err).FontSize(theme.TextCaption).TextColor(theme.RoleFault).Shrink(1)
							})
						}
						continue
					}
					weight := 500
					if r.best {
						weight = 600
					}
					cell(i, text, col, theme.TextCaption, weight)
				}
			})
		}
	})
}

func calStat(s StageStat) calCell {
	if s.P50 <= 0 {
		return plain("—")
	}
	return plain(formatMs(s.P50))
}

var candidateCols = []calCol{
	{"CANDIDATE", 0, false}, {"BEST", 44, false}, {"CODEC", 96, false},
	{"ENC", 52, true}, {"NET", 52, true}, {"DEC", 52, true}, {"TOTAL", 58, true},
	{"MBPS", 52, true}, {"BAD", 48, true}, {"SCORE", 56, true},
}

func calCandidates(c *mui.Context, cs []CalibrationCandidate) {
	if len(cs) == 0 {
		return
	}
	rows := make([]calRow, len(cs))
	for i, k := range cs {
		best := plain("")
		if k.Best {
			best = tinted("BEST", theme.RoleAccent)
		}
		codec := k.Codec.String()
		if k.BitDepth > 0 {
			codec += fmt.Sprintf(" %d-bit", k.BitDepth)
		}
		bad := plain(fmt.Sprintf("%.1f%%", k.BadPct))
		if k.BadPct >= 1 {
			bad = tinted(bad.text, theme.RoleRelay)
		}
		rows[i] = calRow{best: k.Best, err: k.Err, cells: []calCell{
			plain(k.Name), best, plain(codec), calStat(k.Encode), calStat(k.Network), calStat(k.Decode), calStat(k.Total),
			plain(formatMbps(k.BitrateMbps)), bad, plain(formatMs(k.Score)),
		}}
	}
	calSection(c, "CODEC CANDIDATES", "p50 ms · lower score wins", func() {
		calTable(c, "Codec candidates", candidateCols, rows)
	})
}

var displayCols = []calCol{
	{"VARIANT", 0, false}, {"BEST", 44, false}, {"SCAN-OUT", 140, false},
	{"GLASS", 64, true}, {"TOTAL", 64, true}, {"REFRESH", 72, true}, {"LOCK", 60, true},
}

func calDisplay(c *mui.Context, d *CalibrationDisplay) {
	calSection(c, "DISPLAY", "present to glass, p50 ms", func() {
		switch d.Status {
		case CalibrationDisplaySkipped, CalibrationDisplayUnavailable:
			note := d.Note
			if note == "" {
				note = "The display phase did not measure this run."
			}
			infoWell(c, note)
			return
		}
		if d.Note != "" {
			caption(c, d.Note).FillWidth().TextColor(theme.RoleText2)
		}
		rows := make([]calRow, len(d.Variants))
		for i, v := range d.Variants {
			best := plain("")
			if v.Best {
				best = tinted("BEST", theme.RoleAccent)
			}
			scan := plain("—")
			switch v.ScanOut {
			case "direct":
				scan = tinted("direct", theme.RoleDirect)
			case "composited":
				text := "composited"
				if v.CompositedPct > 0 {
					text = fmt.Sprintf("composited %.0f%%", v.CompositedPct)
				}
				scan = tinted(text, theme.RoleRelay)
			}
			hz, lock := plain("—"), plain("off")
			if v.RefreshHz > 0 {
				hz = plain(fmt.Sprintf("%.0f Hz", v.RefreshHz))
			}
			if v.Locked {
				lock = tinted("locked", theme.RoleDirect)
			}
			rows[i] = calRow{best: v.Best, err: v.Err, cells: []calCell{plain(v.Name), best, scan, calStat(v.Glass), calStat(v.Total), hz, lock}}
		}
		calTable(c, "Display variants", displayCols, rows)
	})
}

func (a *App) calFooter(c *mui.Context, st CalibrationState, running, kept bool) {
	mui.Row(c).FillWidth().Padding(theme.Space3, calPad).Gap(theme.Space2).AlignItems(mui.Center).Background(theme.RoleSurface).Children(func() {
		switch {
		case running:
			caption(c, "About 40 seconds. The stream window opens for the display phase.").Shrink(1)
			mui.Spacer(c)
			if button(c, buttonSecondary, "Cancel", false).Clicked() {
				a.backend.CancelCalibration()
			}
		case a.cal.startErr != "" || st.Err != "" || st.Cancelled:
			mui.Spacer(c)
			if button(c, buttonGhost, "Close", false).Clicked() {
				a.closeCalibration()
			}
			if button(c, buttonPrimary, "Re-run", false).Clicked() {
				a.startCalibration()
			}
		default:
			if kept {
				if button(c, buttonDestructive, "Reset calibration", false).Clicked() {
					if err := a.backend.ResetCalibration(a.cal.hostID); err != nil {
						a.cal.actErr = err.Error()
					} else {
						a.cal.actErr, a.cal.applied = "", false
						a.cal.note = a.cal.hostName + " streams with the defaults again."
					}
				}
			}
			mui.Spacer(c)
			if button(c, buttonGhost, "Close", false).Clicked() {
				a.closeCalibration()
			}
			if button(c, buttonSecondary, "Re-run", false).Clicked() {
				a.startCalibration()
			}
			apply := "Apply"
			if a.cal.applied {
				apply = "Applied"
			}
			if button(c, buttonPrimary, apply, false).Disabled(st.Result == nil || a.cal.applied).Clicked() {
				a.applyCalibration()
			}
		}
	})
}
