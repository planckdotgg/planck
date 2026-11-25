package ui

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/audio"
)

// statsSample is one second of a stream, for the Statistics page's charts.
type statsSample struct {
	latencyMs, bitrateMbps, lossPct float64
	// lost is the frames parity could not rebuild that second.
	lost uint64
}

// statsKeep is how many seconds of a stream the charts keep.
const statsKeep = 3600

// statsHistory is the live stream's samples, one a second, and the window
// the glass-to-glass chart shows.
type statsHistory struct {
	started  time.Time
	last     time.Time
	lostSeen uint64
	samples  []statsSample
	window   int
}

// statsWindows are the glass-to-glass chart's windows, in seconds; 0 is
// the whole session.
var statsWindows = []int{60, 300, 0}

// add keeps s if a second has passed since the last sample, starting over
// for a new stream.
func (h *statsHistory) add(s SessionStats, now time.Time) {
	if !s.Started.Equal(h.started) {
		*h = statsHistory{started: s.Started, window: h.window, lostSeen: s.FramesLost}
	}
	if !h.last.IsZero() && now.Sub(h.last) < time.Second {
		return
	}
	h.last = now
	lost := s.FramesLost - min(h.lostSeen, s.FramesLost)
	h.lostSeen = s.FramesLost
	if len(h.samples) == statsKeep {
		h.samples = slices.Delete(h.samples, 0, 1)
	}
	h.samples = append(h.samples, statsSample{latencyMs: s.Total.P50, bitrateMbps: s.BitrateMbps, lossPct: s.LossPct, lost: lost})
}

// tail is the last n samples, or all of them for n 0.
func (h *statsHistory) tail(n int) []statsSample {
	if n <= 0 || n > len(h.samples) {
		return h.samples
	}
	return h.samples[len(h.samples)-n:]
}

func (a *App) statisticsScreen(c *mui.Context) {
	st := a.backend.SessionStats()
	mui.Column(c).FillWidth().MaxWidth(hostsColumn).AlignSelf(mui.Center).Gap(theme.Space4).Children(func() {
		if st.Live {
			a.stats.add(st, c.Now())
			a.liveStats(c, st)
			return
		}
		a.idleStats(c)
	})
}

// statPanel is a Statistics panel: a section label, an optional head on
// its right, and its body.
func statPanel(c *mui.Context, title string, head func(), body func()) mui.Element {
	return mui.Column(c).Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space4, 20).Gap(theme.Space3).Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
			sectionLabel(c, title)
			mui.Spacer(c)
			if head != nil {
				head()
			}
		})
		body()
	})
}

// figure is a big mono reading and its unit.
func figure(c *mui.Context, v, unit string, size float32, col mui.Color) {
	mui.Row(c).AlignItems(mui.Baseline).Gap(6).Children(func() {
		mono(c, v).FontSize(size).FontWeight(500).TextColor(col)
		mono(c, unit).FontSize(theme.TextBody + 1).TextColor(theme.RoleText3)
	})
}

// counter is a label over a mono value.
func counter(c *mui.Context, name, value string, col mui.Color) mui.Element {
	return mui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
		caption(c, name)
		mono(c, value).TextColor(col)
	})
}

// capPill is one of the session header's capabilities.
func capPill(c *mui.Context, fn func()) {
	mui.Row(c).Height(theme.SizeTargetMin).PaddingX(theme.Space2).Gap(6).AlignItems(mui.Center).
		Radius(theme.RadiusXs).Border(1, theme.RoleBorderStrong).Children(fn)
}

func capText(c *mui.Context, s string) mui.Element {
	return mono(c, s).FontSize(theme.TextCaption).TextColor(theme.RoleText2)
}

// checkMark draws a tick in col.
func checkMark(c *mui.Context, size float32, col mui.Color) {
	mui.Box(c).Size(size, size).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		s := r.W / 12
		p.StrokePolyline([][2]float32{{r.X + 2*s, r.Y + 6.5*s}, {r.X + 4.75*s, r.Y + 9.25*s}, {r.X + 10*s, r.Y + 3*s}}, 1.5, col)
	})
}

// formatClock reads a duration as 00:42:17.
func formatClock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// formatFormat reads the stream's format as "HEVC 4:4:4 10-bit".
func formatFormat(st SessionStats) string {
	if st.Codec == 0 {
		return ""
	}
	parts := []string{st.Codec.String()}
	if st.Chroma.Full() {
		parts = append(parts, "4:4:4")
	}
	if st.BitDepth > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit", st.BitDepth))
	}
	return strings.Join(parts, " ")
}

// frameBudgetMs is one frame at the stream's rate; 0 is unknown.
func frameBudgetMs(fps int) float64 {
	if fps <= 0 {
		return 0
	}
	return 1000 / float64(fps)
}

func (a *App) liveStats(c *mui.Context, st SessionStats) {
	a.sessionHeader(c, st)
	mui.Row(c).FillWidth().Gap(theme.Space4).AlignItems(mui.Stretch).Children(func() {
		a.glassStatPanel(c, st).Grow(648).Basis(0)
		framesStatPanel(c, st).Grow(408).Basis(0)
	})
	mui.Row(c).FillWidth().Gap(theme.Space4).AlignItems(mui.Stretch).Children(func() {
		a.bitrateStatPanel(c, st).Grow(360).Basis(0)
		a.lossStatPanel(c, st).Grow(360).Basis(0)
		audioStatPanel(c, st).Grow(320).Basis(0)
	})
	a.diagnostics(c, st)
	// Figures move at 4 Hz without a backend change to wake the page.
	mui.Box(c).Size(0, 0).Draw(func(p *mui.Painter, r mui.Rect) { p.After(250 * time.Millisecond) })
}

func (a *App) sessionHeader(c *mui.Context, st SessionStats) {
	mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space4, 20).Gap(14).Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space3).Children(func() {
			heading(c, st.HostName, theme.TextDisplay)
			mui.Row(c).Height(theme.SizeTargetMin).PaddingX(10).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusPill).
				Background(hostingOnFill).Children(func() {
				mui.Box(c).Size(6, 6).Radius(3).Background(theme.RoleAccent)
				live := "STREAMING"
				if !st.Started.IsZero() {
					live += " " + formatClock(c.Now().Sub(st.Started))
				}
				mono(c, live).FontSize(theme.TextCaption).LetterSpacing(0.04 * theme.TextCaption).TextColor(theme.RoleAccent)
			})
			mui.Spacer(c)
			if a.streamWin != nil && button(c, buttonSecondary, "Bring stream forward", false).Clicked() {
				a.streamWin.OpenStreamWindow(st.HostName)
			}
			d := mui.ButtonBase(c).Label("Disconnect").Height(theme.SizeControl).PaddingX(theme.Space3).
				Radius(theme.RadiusSm).Border(1, theme.RoleBorder.Mix(theme.RoleFault, 0.55)).Cursor(mui.CursorPointer)
			if d.Hovered() {
				d.Background(theme.RoleFault.Alpha(0.12))
			}
			d.Children(func() { label(c, "Disconnect").FontWeight(600).TextColor(theme.RoleFault) })
			if d.Clicked() {
				a.disconnect()
			}
		})
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Wrap().Children(func() {
			capPill(c, func() {
				col := signalColor(st.PathKind)
				sliceMeter(c, pathBars(st.PathKind), 3, col)
				label(c, pathLabel(st.PathKind)).FontSize(theme.TextCaption + 1).FontWeight(500)
				if st.RTTMs > 0 {
					capText(c, fmt.Sprintf("%.2f ms rtt", st.RTTMs)).TextColor(col)
				}
			})
			if st.Width > 0 {
				res := fmt.Sprintf("%d×%d", st.Width, st.Height)
				if st.TargetFPS > 0 {
					res += fmt.Sprintf(" @ %d Hz", st.TargetFPS)
				}
				capPill(c, func() { capText(c, res) })
			}
			if f := formatFormat(st); f != "" {
				capPill(c, func() { capText(c, f) })
			}
			if st.EncoderGPU != "" {
				capPill(c, func() {
					capText(c, shortGPU(st.EncoderGPU)).TextColor(theme.RoleText)
					if st.EncoderAPI != "" {
						capText(c, st.EncoderAPI).TextColor(theme.RoleText3)
					}
				})
			}
			if st.Profile != nil && st.Profile.DirectDisplay {
				mui.Row(c).Gap(6).AlignItems(mui.Center).Children(func() {
					checkMark(c, 12, theme.RoleDirect)
					capText(c, "direct to display").TextColor(theme.RoleDirect)
				})
			}
		})
	})
}

func (a *App) glassStatPanel(c *mui.Context, st SessionStats) mui.Element {
	budget := frameBudgetMs(st.TargetFPS)
	return statPanel(c, "Glass to glass", func() {
		c.Key("stats-window")
		segmented(c, "Chart window", &a.stats.window, "60 s", "5 min", "session")
	}, func() {
		mui.Row(c).FillWidth().AlignItems(mui.End).Gap(theme.Space5).Children(func() {
			figure(c, formatMs(st.Total.P50), "ms p50", 48, theme.RoleAccent)
			mui.Column(c).Gap(2).Margin(0, 0, 6).Children(func() {
				if st.Total.P99 > 0 {
					mono(c, formatMs(st.Total.P99)+" ms p99").TextColor(theme.RoleText2)
				}
				if budget > 0 {
					caption(c, fmt.Sprintf("budget %.2f ms at %d Hz", budget, st.TargetFPS))
				}
			})
			mui.Spacer(c)
			if budget > 0 && st.Total.P50 > 0 {
				mui.Row(c).Gap(6).AlignItems(mui.Center).Margin(0, 0, 6).Children(func() {
					if st.Total.P50 <= budget {
						checkMark(c, 12, theme.RoleDirect)
						caption(c, "inside one frame").TextColor(theme.RoleDirect)
					} else {
						caption(c, "over budget").TextColor(theme.RoleFault)
					}
				})
			}
		})
		stageBreakdown(c, st)
		hist := a.stats.tail(statsWindows[a.stats.window])
		latencyChart(c, hist, budget)
		mui.Row(c).FillWidth().Children(func() {
			span := "session start"
			switch n := statsWindows[a.stats.window]; {
			case n >= 120:
				span = fmt.Sprintf("%d min ago", n/60)
			case n > 0:
				span = fmt.Sprintf("%d s ago", n)
			}
			mono(c, span).FontSize(theme.TextMini).TextColor(theme.RoleText3)
			mui.Spacer(c)
			if budget > 0 {
				mono(c, fmt.Sprintf("--- %.2f ms frame budget", budget)).FontSize(theme.TextMini).TextColor(theme.RoleText3)
			}
			mui.Spacer(c)
			mono(c, "now").FontSize(theme.TextMini).TextColor(theme.RoleText3)
		})
	})
}

// liveStages are the stream's stages in pipeline order, coloured along the
// blackbody spectrum.
func liveStages(st SessionStats) [5]stage {
	return [5]stage{
		{"Capture", st.Capture.P50, theme.SpectrumK2000},
		{"Encode", st.Encode.P50, theme.SpectrumK3000},
		{"Network", st.Network.P50, theme.SpectrumK4000},
		{"Decode", st.Decode.P50, theme.SpectrumK6500},
		{"Present", st.Present.P50, theme.SpectrumK10000},
	}
}

// stageBreakdown is the stage bar, each segment as wide as its stage's
// share, with its legend under it in the same proportions.
func stageBreakdown(c *mui.Context, st SessionStats) {
	stg := liveStages(st)
	var sum float64
	for _, s := range stg {
		sum += s.ms
	}
	mui.Box(c).FillWidth().Height(14).Label("Stage times").Draw(func(p *mui.Painter, r mui.Rect) {
		if sum <= 0 {
			p.Fill(r, theme.RoleBorder, 2)
			return
		}
		const gap = 2
		avail := r.W - gap*float32(len(stg)-1)
		x := r.X
		for _, s := range stg {
			if s.ms <= 0 {
				continue
			}
			w := avail * float32(s.ms/sum)
			p.Fill(mui.Rect{X: x, Y: r.Y, W: w, H: r.H}, s.col, 2)
			x += w + gap
		}
	})
	mui.Row(c).FillWidth().Gap(2).Children(func() {
		for _, s := range stg {
			if s.ms <= 0 && sum > 0 {
				continue
			}
			mui.Column(c).Grow(float32(max(s.ms, 0.5))).Basis(0).MinWidth(56).Gap(2).Children(func() {
				caption(c, s.title)
				mono(c, fmt.Sprintf("%.2f", s.ms))
			})
		}
		mui.Column(c).Shrink(0).AlignItems(mui.End).Gap(2).Children(func() {
			caption(c, "ms, p50")
			mono(c, fmt.Sprintf("= %.2f", sum)).TextColor(theme.RoleText2)
		})
	})
}

// dashedLine draws a horizontal dashed rule.
func dashedLine(p *mui.Painter, x0, x1, y float32, col mui.Color) {
	for x := x0; x < x1; x += 6 {
		p.Line(x, y, min(x+3, x1), y, 1, col)
	}
}

// chartPoints maps values onto r, oldest at the left, between lo and hi.
func chartPoints(r mui.Rect, vals []float64, lo, hi float64) [][2]float32 {
	pts := make([][2]float32, len(vals))
	for i, v := range vals {
		x := r.X + r.W
		if len(vals) > 1 {
			x = r.X + r.W*float32(i)/float32(len(vals)-1)
		}
		k := (v - lo) / (hi - lo)
		k = math.Min(math.Max(k, 0), 1)
		pts[i] = [2]float32{x, r.Y + r.H - float32(k)*r.H}
	}
	return pts
}

// areaChart draws vals as a line over a filled area, with a dashed line at
// mark when it is above 0.
func areaChart(c *mui.Context, name string, h float32, vals []float64, mark float64, col mui.Color, fill float32, dot bool) {
	mui.Box(c).FillWidth().Height(h).Radius(4).Background(theme.RoleWell).Label(name).Draw(func(p *mui.Painter, r mui.Rect) {
		for i := 1; i <= 3; i++ {
			y := r.Y + r.H*float32(i)/4
			p.Line(r.X, y, r.X+r.W, y, 1, theme.RoleBorder.Alpha(0.6))
		}
		hi := mark * 1.6
		for _, v := range vals {
			hi = math.Max(hi, v*1.15)
		}
		if hi <= 0 || len(vals) == 0 {
			return
		}
		in := mui.Rect{X: r.X, Y: r.Y + 4, W: r.W, H: r.H - 8}
		pts := chartPoints(in, vals, 0, hi)
		if mark > 0 {
			y := chartPoints(in, []float64{mark}, 0, hi)[0][1]
			dashedLine(p, r.X, r.X+r.W, y, theme.RoleText3)
		}
		if len(pts) > 1 {
			area := &mui.Path{}
			area.MoveTo(pts[0][0], r.Y+r.H)
			for _, pt := range pts {
				area.LineTo(pt[0], pt[1])
			}
			area.LineTo(pts[len(pts)-1][0], r.Y+r.H)
			area.Close()
			p.FillPath(area, col.Alpha(fill))
			p.StrokePolyline(pts, 1.5, col)
		}
		if dot {
			last := pts[len(pts)-1]
			p.Fill(mui.Rect{X: last[0] - 2.5, Y: last[1] - 2.5, W: 5, H: 5}, col, 2.5)
		}
	})
}

// downsample keeps at most n values, each the mean of its bucket.
func downsample(vals []float64, n int) []float64 {
	if len(vals) <= n {
		return vals
	}
	out := make([]float64, n)
	for i := range n {
		lo, hi := i*len(vals)/n, (i+1)*len(vals)/n
		var s float64
		for _, v := range vals[lo:hi] {
			s += v
		}
		out[i] = s / float64(hi-lo)
	}
	return out
}

func latencyChart(c *mui.Context, hist []statsSample, budget float64) {
	vals := make([]float64, len(hist))
	for i, s := range hist {
		vals[i] = s.latencyMs
	}
	areaChart(c, "Glass to glass history", 76, downsample(vals, 300), budget, theme.RoleAccent, 0.13, true)
}

// frameStats are the mean of the frame times and their jitter: the p95 of
// each one's distance from the mean.
func frameStats(ft []float64) (mean, jitter float64) {
	if len(ft) == 0 {
		return 0, 0
	}
	for _, v := range ft {
		mean += v
	}
	mean /= float64(len(ft))
	dev := make([]float64, len(ft))
	for i, v := range ft {
		dev[i] = math.Abs(v - mean)
	}
	slices.Sort(dev)
	return mean, dev[int(math.Ceil(0.95*float64(len(dev))))-1]
}

// frameBars is how many frames the pacing chart shows.
const frameBars = 46

func framesStatPanel(c *mui.Context, st SessionStats) mui.Element {
	return statPanel(c, "Frames & pacing", nil, func() {
		mui.Row(c).FillWidth().AlignItems(mui.End).Gap(theme.Space5).Children(func() {
			figure(c, fmt.Sprintf("%.0f", st.FPS), "fps", 48, theme.RoleText)
			if mean, jitter := frameStats(st.FrameTimesMs); mean > 0 {
				mui.Column(c).Gap(2).Margin(0, 0, 6).Children(func() {
					mono(c, fmt.Sprintf("%.2f ms ± %.2f", mean, jitter)).TextColor(theme.RoleText2)
					caption(c, "frame time, jitter p95")
				})
			}
		})
		mui.Row(c).FillWidth().Gap(theme.Space3).Children(func() {
			bad := func(n uint64) mui.Color {
				if n > 0 {
					return theme.RoleFault
				}
				return theme.RoleText
			}
			counter(c, "Received", fmt.Sprint(st.FramesReceived), theme.RoleText)
			counter(c, "Dropped", fmt.Sprint(st.FramesDropped), bad(st.FramesDropped))
			counter(c, "Lost", fmt.Sprint(st.FramesLost), bad(st.FramesLost))
			counter(c, "Keyframes", fmt.Sprint(st.Keyframes), theme.RoleText)
		})
		target := frameBudgetMs(st.TargetFPS)
		ft := st.FrameTimesMs[max(0, len(st.FrameTimesMs)-frameBars):]
		mui.Box(c).FillWidth().Height(76).Radius(4).Background(theme.RoleWell).Label("Frame times").Draw(func(p *mui.Painter, r mui.Rect) {
			if target <= 0 || len(ft) == 0 {
				return
			}
			lo, hi := target*0.7, target*1.45
			const gap = 2
			w := (r.W - gap*(frameBars-1)) / frameBars
			x := r.X + (r.W - float32(len(ft))*(w+gap) + gap)
			for _, v := range ft {
				k := math.Min(math.Max((v-lo)/(hi-lo), 0.04), 1)
				h := float32(k) * (r.H - 4)
				col := theme.RoleText3.Alpha(0.5)
				if v > target*1.14 {
					col = theme.RoleFault
				}
				p.Fill(mui.Rect{X: x, Y: r.Y + r.H - h, W: w, H: h}, col, 0)
				x += w + gap
			}
			y := r.Y + r.H - float32((target-lo)/(hi-lo))*(r.H-4)
			dashedLine(p, r.X, r.X+r.W, y, theme.RoleText3)
		})
		mui.Row(c).FillWidth().Children(func() {
			mono(c, fmt.Sprintf("last %d frames", len(ft))).FontSize(theme.TextMini).TextColor(theme.RoleText3)
			mui.Spacer(c)
			if target > 0 {
				mono(c, fmt.Sprintf("--- %.2f ms", target)).FontSize(theme.TextMini).TextColor(theme.RoleText3)
			}
		})
	})
}

func (a *App) bitrateStatPanel(c *mui.Context, st SessionStats) mui.Element {
	return statPanel(c, "Bitrate & rate control", nil, func() {
		mui.Row(c).FillWidth().AlignItems(mui.End).Children(func() {
			figure(c, fmt.Sprintf("%.1f", st.BitrateMbps), "Mbps", theme.TextReadout, theme.RoleText)
			mui.Spacer(c)
			if st.CeilingMbps > 0 {
				mui.Column(c).AlignItems(mui.End).Gap(2).Children(func() {
					mono(c, "cap "+formatMbps(st.CeilingMbps)).TextColor(theme.RoleText2)
					caption(c, "the most the host accepts here")
				})
			}
		})
		hist := a.stats.tail(60)
		vals := make([]float64, len(hist))
		for i, s := range hist {
			vals[i] = s.bitrateMbps
		}
		areaChart(c, "Bitrate history", 72, vals, st.CeilingMbps, theme.RoleText2, 0.10, false)
		mui.Row(c).FillWidth().Children(func() {
			mono(c, "60 s").FontSize(theme.TextMini).TextColor(theme.RoleText3)
			mui.Spacer(c)
			if st.CeilingMbps > 0 {
				mono(c, "--- cap "+formatMbps(st.CeilingMbps)+" Mbps").FontSize(theme.TextMini).TextColor(theme.RoleText3)
			}
		})
	})
}

func (a *App) lossStatPanel(c *mui.Context, st SessionStats) mui.Element {
	return statPanel(c, "Packet loss & FEC recovery", nil, func() {
		mui.Row(c).FillWidth().AlignItems(mui.End).Children(func() {
			figure(c, fmt.Sprintf("%.2f", st.LossPct), "% lost", theme.TextReadout, theme.RoleText)
			mui.Spacer(c)
			pill, col := "nothing unrepaired", theme.RoleDirect
			if st.FramesLost > 0 {
				pill, col = fmt.Sprintf("%d unrepaired", st.FramesLost), theme.RoleFault
			}
			mui.Row(c).Height(theme.SizeTargetMin).PaddingX(theme.Space2).Gap(6).AlignItems(mui.Center).
				Radius(theme.RadiusXs).Border(1, theme.RoleBorder.Mix(col, 0.45)).Children(func() {
				if st.FramesLost == 0 {
					checkMark(c, 12, col)
				}
				mono(c, pill).FontSize(theme.TextCaption).TextColor(col)
			})
		})
		hist := a.stats.tail(60)
		mui.Box(c).FillWidth().Height(24).Label("Loss per second").Draw(func(p *mui.Painter, r mui.Rect) {
			const tick, gap = 4, 1
			x := r.X + r.W - float32(len(hist))*(tick+gap)
			for _, s := range hist {
				h, col := float32(4), theme.RoleDirect.Alpha(0.35)
				switch {
				case s.lost > 0:
					h, col = 17, theme.RoleFault
				case s.lossPct > 1:
					h, col = 17, theme.RoleRelay
				case s.lossPct > 0.1:
					h, col = 13, theme.RoleRelay
				case s.lossPct > 0:
					h, col = 9, theme.RoleRelay
				}
				p.Fill(mui.Rect{X: x, Y: r.Y + r.H - h, W: tick, H: h}, col, 0)
				x += tick + gap
			}
		})
		mono(c, "loss per second, last 60 s").FontSize(theme.TextMini).TextColor(theme.RoleText3)
		divider(c)
		mui.Row(c).FillWidth().Gap(theme.Space3).Children(func() {
			counter(c, "Parity", fmt.Sprintf("%.0f %%", st.FECParityPct), theme.RoleText)
			counter(c, "Repaired", fmt.Sprint(st.FECRepaired), theme.RoleText)
			col := theme.RoleText
			if st.FramesLost > 0 {
				col = theme.RoleFault
			}
			counter(c, "Unrepaired", fmt.Sprint(st.FramesLost), col)
		})
	})
}

func audioStatPanel(c *mui.Context, st SessionStats) mui.Element {
	return statPanel(c, "Audio & microphone", nil, func() {
		figure(c, fmt.Sprintf("%.0f", st.AudioBufMs), "ms buffered", theme.TextReadout, theme.RoleText)
		divider(c)
		row := func(name string, value func()) {
			mui.Row(c).FillWidth().AlignItems(mui.Center).Children(func() {
				caption(c, name).Width(88).Shrink(0)
				value()
			})
		}
		row("Codec", func() { capText(c, fmt.Sprintf("Opus %d kHz stereo", audio.SampleRate/1000)).TextColor(theme.RoleText) })
		row("Buffer", func() { capText(c, fmt.Sprintf("%.0f ms", st.AudioBufMs)).TextColor(theme.RoleText) })
		row("Microphone", func() {
			mui.Row(c).Gap(6).AlignItems(mui.Center).Shrink(1).Children(func() {
				live := !st.MicMuted && st.MicDevice != ""
				mui.Box(c).Size(6, 6).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
					if live {
						p.Fill(r, theme.RoleDirect, 3)
						return
					}
					p.Stroke(r, theme.RoleText3, 3, 1.5)
				})
				v := "muted"
				switch {
				case st.MicDevice == "" && st.MicHint != "":
					v = st.MicHint
				case live:
					v = st.MicDevice + " · live"
				}
				capText(c, v).TextColor(theme.RoleText).Shrink(1)
			})
		})
	})
}

// diagnosticsLine is the route's facts, which Copy diagnostics copies too.
func diagnosticsLine(st SessionStats) string {
	parts := []string{pathLabel(st.PathKind)}
	if st.DatagramBytes > 0 {
		parts = append(parts, fmt.Sprintf("%d B datagrams", st.DatagramBytes))
	}
	parts = append(parts, "AES-128-GCM")
	if p := st.Profile; p != nil {
		if p.Slices > 0 {
			parts = append(parts, fmt.Sprintf("slices %d", p.Slices))
		}
		if p.Split {
			parts = append(parts, "NVENC split on")
		}
		vs := "VSync off"
		if p.VSync {
			vs = "VSync on"
		}
		parts = append(parts, vs)
	}
	return strings.Join(parts, " · ")
}

// diagnosticsText is everything the page shows, as text to paste.
func diagnosticsText(st SessionStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Planck session: %s\n%s\n", st.HostName, diagnosticsLine(st))
	fmt.Fprintf(&b, "format %s %dx%d @ %d, encoder %s %s\n", formatFormat(st), st.Width, st.Height, st.TargetFPS, st.EncoderGPU, st.EncoderAPI)
	for _, s := range liveStages(st) {
		fmt.Fprintf(&b, "%s p50 %.2f ms\n", strings.ToLower(s.title), s.ms)
	}
	fmt.Fprintf(&b, "glass to glass p50 %.2f ms p99 %.2f ms, rtt %.2f ms\n", st.Total.P50, st.Total.P99, st.RTTMs)
	fmt.Fprintf(&b, "fps %.1f of %d, received %d, dropped %d, lost %d, keyframes %d\n", st.FPS, st.TargetFPS, st.FramesReceived, st.FramesDropped, st.FramesLost, st.Keyframes)
	fmt.Fprintf(&b, "bitrate %.1f Mbps cap %.0f, loss %.2f %%, parity %.0f %%, repaired %d, audio %.0f ms\n", st.BitrateMbps, st.CeilingMbps, st.LossPct, st.FECParityPct, st.FECRepaired, st.AudioBufMs)
	return b.String()
}

func (a *App) diagnostics(c *mui.Context, st SessionStats) {
	mui.Column(c).FillWidth().Children(func() {
		divider(c)
		mui.Row(c).FillWidth().AlignItems(mui.Center).Padding(theme.Space4, 0, 0).Gap(theme.Space4).Children(func() {
			mono(c, diagnosticsLine(st)).FontSize(theme.TextCaption).TextColor(theme.RoleText3).Shrink(1)
			mui.Spacer(c)
			if button(c, buttonSecondary, "Copy diagnostics", false).Height(28).Clicked() {
				c.Services().WriteClipboard(diagnosticsText(st))
			}
		})
	})
}

func (a *App) idleStats(c *mui.Context) {
	pageHeader(c, "Statistics", "Nothing is streaming right now.", nil)
	mui.Column(c).FillWidth().MinHeight(320).Padding(theme.Space6).AlignItems(mui.Center).Justify(mui.Center).
		Gap(theme.Space4).Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).Children(func() {
		mui.Box(c).Size(420, 112).Draw(func(p *mui.Painter, r mui.Rect) {
			for i, alpha := range []float32{0.09, 0.12, 0.15} {
				p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*28), W: r.W, H: 20}, theme.RoleText3.Alpha(alpha), 2)
			}
			p.Fill(mui.Rect{X: r.X, Y: r.Y + 84, W: 96, H: 20}, theme.RoleText3.Alpha(0.30), 2)
		})
		heading(c, "No slices in flight", theme.TextDisplay)
		label(c, "Start a session and this page fills with glass-to-glass latency, frame pacing, bitrate, loss and audio — made to sit on a second monitor while you play on the first.").
			TextColor(theme.RoleText3).FixedLineHeight(theme.LeadingBody).TextAlign(mui.Center).MaxWidth(440)
		if button(c, buttonPrimary, "Go to Hosts", false).Width(132).Height(theme.SizeControlLg).Clicked() {
			a.screen = ScreenHosts
		}
	})
	if last, ok := a.backend.LastSession(); ok {
		lastSession(c, last)
	}
	if h := a.backend.Hosting(); h.On {
		a.hostingStatus(c, h)
	}
}

// sessionSpan reads when a session ran: "today 18:04 – 18:46".
func sessionSpan(st SessionStats, now time.Time) string {
	if st.Started.IsZero() {
		return ""
	}
	day := st.Started.Format("Jan 2")
	if y, m, d := now.Date(); st.Started.Year() == y && st.Started.Month() == m && st.Started.Day() == d {
		day = "today"
	}
	s := day + " " + st.Started.Format("15:04")
	if !st.Ended.IsZero() {
		s += " – " + st.Ended.Format("15:04")
	}
	return s
}

func lastSession(c *mui.Context, st SessionStats) {
	mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space4, 20).Gap(theme.Space3).Label("Last session").Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Center).Children(func() {
			sectionLabel(c, "Last session")
			who := st.HostName
			if s := sessionSpan(st, c.Now()); s != "" {
				who += " · " + s
			}
			mono(c, who).FontSize(12).TextColor(theme.RoleText2)
		})
		lane := func(name, value string) {
			mui.Column(c).Shrink(0).Gap(2).Children(func() {
				caption(c, name)
				mono(c, value).FontSize(theme.TextTitle).FixedLineHeight(22)
			})
		}
		mui.Row(c).FillWidth().Gap(theme.Space5).Justify(mui.SpaceBetween).Wrap().Children(func() {
			if !st.Started.IsZero() && !st.Ended.IsZero() {
				lane("Duration", fmt.Sprintf("%.0f min", st.Ended.Sub(st.Started).Minutes()))
			}
			lane("Latency p50 / p99", fmt.Sprintf("%s / %s ms", formatMs(st.Total.P50), formatMs(st.Total.P99)))
			lane("Frames", fmt.Sprintf("%.1f fps", st.FPS))
			lane("Bitrate", formatMbps(st.BitrateMbps)+" Mbps")
			lane("Loss", fmt.Sprintf("%.2f %%", st.LossPct))
			lane("Path", pathLabel(st.PathKind))
			if f := formatFormat(st); f != "" {
				lane("Format", f)
			}
		})
	})
}

// hostingStatus is the bar that says this machine is hosting, while it is.
func (a *App) hostingStatus(c *mui.Context, h HostingState) {
	hs := a.backend.HostSettings()
	name := cmp.Or(hs.Name, a.machineName)
	mui.Row(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space4, 20).Gap(theme.Space5).AlignItems(mui.Center).Children(func() {
		mui.Icon(c, theme.IconHosting).Size(theme.SizeRailIcon, theme.SizeRailIcon).TextColor(theme.RoleAccent)
		mui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			label(c, "This "+machineWord(a.goos)+" is hosting").FontSize(14).FontWeight(600)
			who := "waiting for a client"
			if h.Client != "" {
				who = "streaming to " + h.Client
			}
			parts := []string{who, name}
			if h.Using.Control > 0 {
				parts = append(parts, fmt.Sprintf("%d / %d", h.Using.Control, h.Using.Media))
			}
			mono(c, strings.Join(parts, " · ")).FontSize(theme.TextCaption).TextColor(theme.RoleText3).Ellipsis("…")
		})
		mui.Box(c).Width(1).Height(32).Background(theme.RoleBorder)
		group := func(k, v string) {
			mui.Column(c).Shrink(0).Gap(2).Children(func() {
				caption(c, k)
				mono(c, v).FontSize(12)
			})
		}
		if h.Encoder != "" {
			group("Encoder", h.Encoder)
		}
		if d, ok := displayName(a.backend.Displays(), hs.Display); ok {
			group("Capture", d)
		}
		n := len(a.backend.PairedClients())
		group("Paired", fmt.Sprintf("%d %s", n, plural(n, "device", "devices")))
		if button(c, buttonSecondary, "Stop hosting", false).Clicked() {
			a.setHosting(false)
		}
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
