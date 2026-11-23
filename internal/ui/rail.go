package ui

import (
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

var railItems = []struct {
	screen Screen
	name   string
	icon   **mui.SVG
}{
	{ScreenHosts, "Hosts", &theme.IconHosts},
	{ScreenPairing, "Pairing", &theme.IconPairing},
	{ScreenSettings, "Settings", &theme.IconSetting},
}

// rail is the 88 px icon rail: the mark, the three destinations, and the
// hosting toggle at the foot.
func (a *App) rail(c *mui.Context) {
	mui.Column(c).Width(theme.SizeRail).FillHeight().Background(theme.RoleSurface).
		BorderEndWidth(1).BorderColor(theme.RoleBorder).AlignItems(mui.Center).
		Padding(theme.Space6, 0, theme.Space5).Gap(theme.Space2).Children(func() {
		mui.Image(c, theme.Mark).Size(22, 34).Label("Planck").Margin(0, 0, theme.Space6)
		for _, it := range railItems {
			b := railButton(c, *it.icon, it.name, a.screen == it.screen, false)
			if b.Clicked() {
				a.screen = it.screen
			}
		}
		mui.Spacer(c)
		a.hostingButton(c)
	})
}

func railButton(c *mui.Context, icon *mui.SVG, name string, active, ember bool) mui.Element {
	b := mui.ButtonBase(c).Size(theme.SizeRailButton, theme.SizeRailButton).Radius(theme.RadiusMd).
		Label(name).Cursor(mui.CursorPointer).Selected(active)
	fg := theme.RoleText3
	switch {
	case active:
		b.Background(theme.RoleRaised)
		fg = theme.RoleText
	case b.Hovered():
		b.Background(theme.RoleHover)
		fg = theme.RoleText2
	}
	if ember {
		fg = theme.RoleAccent
	}
	b.Children(func() {
		mui.Icon(c, icon).Size(theme.SizeRailIcon, theme.SizeRailIcon).TextColor(fg)
		if active {
			// The marker sits on the rail's left edge, (88-56)/2 px out.
			mui.Box(c).Absolute().Left(-(theme.SizeRail-theme.SizeRailButton)/2).
				Top((theme.SizeRailButton-24)/2).Size(3, 24).Background(theme.RoleAccent)
		}
	})
	tooltip(c, b, func() { label(c, name).FontSize(12) })
	return b
}

func (a *App) hostingButton(c *mui.Context) {
	h := a.backend.Hosting()
	a.mu.Lock()
	busy := a.hostingBusy
	a.mu.Unlock()
	state := "off"
	switch {
	case busy:
		state = "starting"
		if h.On {
			state = "stopping"
		}
	case h.On:
		state = "on"
	}
	name := "Host this " + machineWord(a.goos)
	b := railButton(c, theme.IconHosting, name, false, h.On).Description(name + ", " + state)
	if h.On {
		b.Border(1, theme.RoleAccent)
	}
	mui.TooltipBase(c, b, func(tip mui.Element) {
		tip.AttachTo(b, mui.AnchorEnd, mui.AnchorStart).MarginStart(theme.Space2).
			Padding(theme.Space2, theme.Space3).Radius(theme.RadiusSm).Background(theme.RoleRaised).
			Border(1, theme.RoleBorderStrong).Shadow(0, 16, 40, 0, theme.FloatShadow).Children(func() {
			mui.Column(c).Gap(2).Children(func() {
				mui.Row(c).Gap(theme.Space2).Children(func() {
					label(c, name).FontWeight(600).FontSize(12)
					mono(c, state).FontSize(12).TextColor(theme.RoleText3)
				})
				if h.Encoder != "" {
					mono(c, h.Encoder).FontSize(12).TextColor(theme.RoleText2)
				}
				if h.Err != "" {
					label(c, h.Err).FontSize(12).TextColor(theme.RoleFault)
				}
			})
		})
	})
	if b.Clicked() && !busy {
		a.setHosting(!h.On)
	}
}
