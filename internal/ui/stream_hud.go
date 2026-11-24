package ui

import (
	"fmt"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

func indexOf[T comparable](list []T, v T) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// pickFrom binds a segmented control's index to *v: a choice the list
// lacks shows no segment chosen.
func pickFrom[T comparable](c *mui.Context, name string, list []T, labels []string, v *T) {
	i := indexOf(list, *v)
	if segmented(c, name, &i, labels...) && i >= 0 {
		*v = list[i]
	}
}

// stage is one part of glass-to-glass, colored along the blackbody
// spectrum in pipeline order.
type stage struct {
	name string
	ms   float64
	col  mui.Color
}

func stages(t Telemetry) []stage {
	return []stage{
		{"encode", t.EncodeMs, theme.SpectrumK3000},
		{"network", t.NetMs, theme.SpectrumK4000},
		{"decode", t.DecodeMs, theme.SpectrumK6500},
		{"present", t.PresentMs, theme.SpectrumK10000},
	}
}

func (a *App) streamScreen(c *mui.Context, tel Telemetry, connecting string) {
	well := mui.Box(c).Fill()
	if !tel.Connected || a.streamView == nil {
		well.Background(theme.RoleWell)
	}
	well.Children(func() {
		if !tel.Connected {
			mui.Column(c).Fill().AlignItems(mui.Center).Justify(mui.Center).Gap(theme.Space4).Children(func() {
				sliceMeter(c, 1, 4, theme.RoleAccent)
				heading(c, "Connecting to "+connecting, theme.TextDisplay)
				if button(c, buttonGhost, "Cancel", false).Clicked() {
					a.cancelConnect()
				}
			})
			return
		}
		if a.streamView != nil {
			mui.Box(c).Fill().Children(func() { a.streamView(c) })
		}
		mode := a.hudMode()
		shown := a.hudShown(c, well, mode)
		a.mu.Lock()
		a.hudVisible = shown
		a.mu.Unlock()
		if !shown {
			a.hudRects = a.hudRects[:0]
			return
		}
		mui.Column(c).Absolute().Top(theme.Space4).Left(0).Right(0).AlignItems(mui.Center).Gap(theme.Space2).
			PassThrough().Children(func() {
			a.hudRects = a.hudRects[:0]
			a.hudRects = append(a.hudRects, a.hudPill(c, tel, mode).Bounds())
			if mode == HUDDrawer {
				a.hudRects = append(a.hudRects, a.hudDrawer(c, tel).Bounds())
			}
		})
	})
}

// hudShown reports whether the HUD shows this frame, asking for the frame
// that hides it. The pointer is only read, never caught: while the HUD is
// hidden nothing is drawn over the stream, so every press reaches it.
func (a *App) hudShown(c *mui.Context, well mui.Element, mode HUDMode) bool {
	if mode == HUDHidden {
		a.hudHeld = false
		return false
	}
	now := c.Now()
	held := mode == HUDDrawer || a.hudPinned || a.pointerOnHUD(well)
	if a.hudHeld && !held {
		a.hudUntil = now.Add(hudLinger)
	}
	a.hudHeld = held
	if held {
		return true
	}
	if left := a.hudUntil.Sub(now); left > 0 {
		c.After(left)
		return true
	}
	return false
}

// pointerOnHUD reports whether the pointer is in the top edge band or over
// the pill or drawer as last drawn.
func (a *App) pointerOnHUD(well mui.Element) bool {
	x, y, over := well.PointerPosition()
	if !over {
		return false
	}
	if y < hudEdge {
		return true
	}
	b := well.Bounds()
	wx, wy := x+b.X, y+b.Y
	for _, r := range a.hudRects {
		if wx >= r.X && wx < r.X+r.W && wy >= r.Y && wy < r.Y+r.H {
			return true
		}
	}
	return false
}

func hudDivider(c *mui.Context) {
	mui.Box(c).Width(1).Height(20).Background(theme.HUDEdge)
}

// hudPill is the glanceable reading: glass to glass in ember, then fps,
// bitrate, path and codec.
func (a *App) hudPill(c *mui.Context, t Telemetry, mode HUDMode) mui.Element {
	return mui.Row(c).Height(44).AlignItems(mui.Center).Gap(theme.Space3).Padding(0, theme.Space1+2, 0, theme.Space4).
		Radius(theme.RadiusPill).Background(theme.HUDGlass).Border(1, theme.HUDEdge).
		Shadow(0, 16, 40, 0, theme.FloatShadow).Label(hudSummary(t)).Children(func() {
		mui.Image(c, theme.Mark).Size(11, 17)
		mui.Row(c).AlignItems(mui.Baseline).Gap(3).Children(func() {
			mono(c, formatMs(t.TotalMs)).FontSize(theme.TextHeading).FontWeight(500).TextColor(theme.RoleAccent)
			mono(c, "ms").FontSize(theme.TextCaption).TextColor(theme.RoleText3)
		})
		hudDivider(c)
		hudReading(c, fmt.Sprintf("%.0f", t.FPS), "FPS")
		hudReading(c, formatMbps(t.BitrateMbps), "Mbps")
		hudDivider(c)
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			sliceMeter(c, pathBars(t.PathKind), 3, signalColor(t.PathKind))
			label(c, pathLabel(t.PathKind)).SingleLine()
		})
		if s := codecLabel(t.Codec, t.Chroma); s != "" {
			hudDivider(c)
			mono(c, s).FontSize(theme.TextCaption + 1).TextColor(theme.RoleText2)
		}
		name := "Show stream controls"
		if mode == HUDDrawer {
			name = "Hide stream controls"
		}
		b := mui.ButtonBase(c).Size(32, 32).Radius(theme.RadiusPill).Label(name).
			Expanded(mode == HUDDrawer).Cursor(mui.CursorPointer).Background(theme.RoleRaised)
		if b.Hovered() {
			b.Background(theme.RoleHover)
		}
		b.Children(func() {
			ic := mui.Icon(c, theme.IconChevron).Size(16, 16).TextColor(theme.RoleText)
			if mode == HUDDrawer {
				ic.Rotate(180)
			}
		})
		if b.Clicked() {
			if mode == HUDDrawer {
				a.setHUD(HUDPill)
			} else {
				a.setHUD(HUDDrawer)
			}
		}
	})
}

func hudReading(c *mui.Context, v, unit string) {
	mui.Row(c).AlignItems(mui.Baseline).Gap(3).Children(func() {
		mono(c, v).FontWeight(500)
		mono(c, unit).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
	})
}

// hudDrawer holds the stream's controls: the stage times, the resolved
// stream with quick overrides for this host, and the stream's actions.
func (a *App) hudDrawer(c *mui.Context, t Telemetry) mui.Element {
	before := a.backend.Settings()
	s := before
	scope := overrideScope{hostID: t.HostID, name: t.HostName}
	if scope.all() {
		scope.name = "All hosts"
	}
	e := a.editOverrides(scope)
	d := mui.Column(c).Width(600).Padding(theme.Space5).Gap(theme.Space4).Radius(theme.RadiusMd).
		Background(theme.HUDGlass).Border(1, theme.HUDEdge).Shadow(0, 16, 40, 0, theme.FloatShadow).
		Label("Stream controls").Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Center).Children(func() {
			label(c, "Glass to glass, by stage").FontWeight(600).Grow(1)
			mono(c, streamFormat(t)).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
		})
		stageBar(c, t)
		divider(c)
		if line := profileLine(t.Profile); line != "" {
			mui.Column(c).FillWidth().Gap(theme.Space1).Children(func() {
				mono(c, line).FontSize(theme.TextCaption + 1)
				if t.Profile != nil {
					for _, w := range t.Profile.Warnings {
						caption(c, w).TextColor(theme.RoleRelay)
					}
				}
			})
		}
		caption(c, "Changes here are for "+e.scope.name+" and restart the stream. Settings has every option.")
		for _, k := range []knob{knobCodec, knobChroma, knobBitDepth, knobFPS} {
			drawerRow(c, k.title, e.inherited(k), func() { e.knobControl(c, k) })
		}
		drawerRow(c, "Bitrate", e.inheritedBitrate(), func() {
			c.Key("knob:" + e.scope.hostID + ":drawerBitrate")
			pickFrom(c, "Bitrate", drawerBitrates, mbpsLabels(drawerBitrates), &e.o.BitrateMbps)
		})
		for _, k := range []knob{knobVDD, knobDirect} {
			drawerRow(c, k.title, e.inherited(k), func() { e.knobControl(c, k) })
		}
		mui.Row(c).FillWidth().Gap(theme.Space6).Children(func() {
			mui.Box(c).Grow(1).Basis(0).Children(func() { switchRow(c, "Audio", "", &s.AudioEnabled, false) })
			mui.Box(c).Grow(1).Basis(0).Children(func() { switchRow(c, "System keys", "", &s.SystemKeysCapture, false) })
			mui.Box(c).Grow(1).Basis(0).Children(func() { switchRow(c, "Pin HUD", "", &a.hudPinned, false) })
		})
		divider(c)
		mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
			caption(c, "Cycle HUD: Ctrl Alt H").Grow(1)
			if button(c, buttonGhost, "Reset to Auto", false).Clicked() {
				e.resetToAuto()
			}
			if button(c, buttonSecondary, "Force keyframe", false).Clicked() {
				a.backend.RequestKeyframe()
			}
			if button(c, buttonDestructive, "Disconnect", false).Clicked() {
				a.disconnect()
			}
		})
	})
	a.saveSettings(before, s)
	a.saveOverrides(e)
	return d
}

// drawerRow is a control with its name and, when set, a hint under it.
func drawerRow(c *mui.Context, name, hint string, control func()) {
	mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space4).Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			label(c, name)
			if hint != "" {
				caption(c, hint)
			}
		})
		control()
	})
}

// mbpsLabels names bitrates, 0 being auto.
func mbpsLabels(v []int) []string {
	out := make([]string, len(v))
	for i, m := range v {
		out[i] = fmt.Sprintf("%d Mbps", m)
		if m == 0 {
			out[i] = "Auto"
		}
	}
	return out
}

// streamFormat is the drawer's caption, as "1920 x 1080, 240 FPS, HEVC
// 10-bit, 1152 B datagrams".
func streamFormat(t Telemetry) string {
	s := resolutionLabel(t.Width, t.Height)
	if t.TargetFPS > 0 {
		s += fmt.Sprintf(", %d FPS", t.TargetFPS)
	}
	if cl := codecLabel(t.Codec, t.Chroma); cl != "" {
		s += ", " + cl
	}
	if t.DatagramBytes > 0 {
		s += fmt.Sprintf(", %d B datagrams", t.DatagramBytes)
	}
	return s
}

// stageBar draws each stage as a slice as wide as its share of the stages'
// sum, with its reading underneath.
func stageBar(c *mui.Context, t Telemetry) {
	st := stages(t)
	var sum float64
	for _, s := range st {
		sum += s.ms
	}
	mui.Column(c).FillWidth().Gap(theme.Space3).Children(func() {
		mui.Box(c).FillWidth().Height(8).Label("Stage times").Draw(func(p *mui.Painter, r mui.Rect) {
			if sum <= 0 {
				p.Fill(r, theme.RoleBorder, theme.RadiusXs)
				return
			}
			const gap = 3
			avail := r.W - gap*float32(len(st)-1)
			x := r.X
			for _, s := range st {
				w := avail * float32(s.ms/sum)
				p.Fill(mui.Rect{X: x, Y: r.Y, W: w, H: r.H}, s.col, 2)
				x += w + gap
			}
		})
		mui.Row(c).FillWidth().Children(func() {
			for _, s := range st {
				mui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					mono(c, s.name).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
					mono(c, formatMs(s.ms)+" ms").FontSize(theme.TextCaption + 1)
				})
			}
		})
	})
}
