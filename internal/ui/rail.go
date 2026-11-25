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
	{ScreenStatistics, "Statistics", &theme.IconStatisticsNav},
	{ScreenSettings, "Settings", &theme.IconSetting},
}

// The rail's vertical rhythm: the mark's centre sits at 21 + 20 = 41, on the
// middle of the cap height of every page title (content starts 24 down), and
// the nav stack starts at 21 + 40 + 55 = 116, so the active marker sits at
// 132, 196 and 260.
const (
	railPadTop    = 21
	railPadBottom = 20
	railLogoBox   = 40
	railNavGap    = 55
)

// rail is the 88 px icon rail: the mark, the three destinations, and the
// hosting toggle at the foot.
func (a *App) rail(c *mui.Context) {
	mui.Column(c).Width(theme.SizeRail).FillHeight().Shrink(0).Background(theme.RoleSurface).
		BorderEndWidth(1).BorderColor(theme.RoleBorder).AlignItems(mui.Center).
		Padding(railPadTop, 0, railPadBottom).Children(func() {
		mui.Box(c).Size(railLogoBox, railLogoBox).AlignItems(mui.Center).Justify(mui.Center).Children(func() {
			mui.Image(c, theme.Mark).Size(22, 34).Label("Planck")
		})
		mui.Column(c).Margin(railNavGap, 0, 0).Gap(theme.Space2).Children(func() {
			for _, it := range railItems {
				b := railButton(c, *it.icon, it.name, a.screen == it.screen)
				tooltip(c, b, func() { label(c, it.name).FontSize(12) })
				if b.Clicked() {
					a.screen = it.screen
				}
			}
		})
		mui.Spacer(c)
		a.hostingButton(c)
	})
}

func railButton(c *mui.Context, icon *mui.SVG, name string, active bool) mui.Element {
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
	b.Children(func() {
		mui.Icon(c, icon).Size(theme.SizeRailIcon, theme.SizeRailIcon).TextColor(fg)
		if active {
			// The marker sits on the rail's left edge, (88-56)/2 px out.
			mui.Box(c).Absolute().Left(-(theme.SizeRail-theme.SizeRailButton)/2).
				Top((theme.SizeRailButton-24)/2).Size(3, 24).Background(theme.RoleAccent).
				Radius(0, 2, 2, 0)
		}
	})
	return b
}

// hostingTipWidth wraps a long hosting error rather than stretching the
// tooltip across the window.
const hostingTipWidth = 280

// hostingOnFill and hostingOnEdge are the hosting toggle's ember wash, the
// same on the rail and in Settings → Host.
var (
	hostingOnFill = theme.RoleSurface.Mix(theme.RoleAccent, 0.14)
	hostingOnEdge = theme.RoleBorder.Mix(theme.RoleAccent, 0.45)
)

// hostingState is the hosting toggle's state word, and whether a change is
// under way.
func (a *App) hostingState(h HostingState) (string, bool) {
	a.mu.Lock()
	busy := a.hostingBusy
	a.mu.Unlock()
	switch {
	case busy && h.On:
		return "stopping", true
	case busy:
		return "starting", true
	case h.On:
		return "on", false
	}
	return "off", false
}

func (a *App) hostingButton(c *mui.Context) {
	h := a.backend.Hosting()
	state, busy := a.hostingState(h)
	name := "Host this " + machineWord(a.goos)
	b := mui.ButtonBase(c).Size(theme.SizeRailButton, theme.SizeRailButton).Radius(theme.RadiusMd).
		Label(name).Description(name + ", " + state).Cursor(mui.CursorPointer).Checked(h.On)
	fg := theme.RoleText3
	switch {
	case h.On:
		b.Background(hostingOnFill).Border(1, hostingOnEdge)
		fg = theme.RoleAccent
	case b.Hovered():
		b.Background(theme.RoleHover)
		fg = theme.RoleText2
	}
	b.Children(func() {
		mui.Icon(c, theme.IconHosting).Size(theme.SizeRailIcon, theme.SizeRailIcon).TextColor(fg)
	})
	stateColor := theme.RoleText3
	if h.On {
		stateColor = theme.RoleAccent
	}
	tooltip(c, b, func() {
		mui.Column(c).Gap(2).MaxWidth(hostingTipWidth).Children(func() {
			mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
				label(c, name).FontWeight(600).FontSize(12)
				mono(c, state).FontSize(theme.TextMini).TextColor(stateColor)
			})
			if h.Encoder != "" {
				label(c, h.Encoder).FontSize(theme.TextCaption).TextColor(theme.RoleText2)
			}
			if h.Err != "" {
				label(c, h.Err).FontSize(theme.TextCaption).TextColor(theme.RoleFault)
			}
		})
	})
	if b.Clicked() && !busy {
		a.setHosting(!h.On)
	}
}
