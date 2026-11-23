package ui

import (
	"fmt"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// featuredHost picks the host the hub leads with: the first online one in
// the backend's order, else the first.
func featuredHost(hosts []HostInfo) int {
	for i, h := range hosts {
		if h.Online {
			return i
		}
	}
	if len(hosts) > 0 {
		return 0
	}
	return -1
}

func hostsSubtitle(hosts []HostInfo) string {
	online := 0
	for _, h := range hosts {
		if h.Online {
			online++
		}
	}
	switch {
	case len(hosts) == 0:
		return "Searching your network and tailnet"
	case online == 1:
		return "1 online on your network and tailnet"
	}
	return fmt.Sprintf("%d online on your network and tailnet", online)
}

func (a *App) hostsScreen(c *mui.Context) {
	hosts := a.backend.Hosts()
	a.mu.Lock()
	connectErr := a.connectErr
	a.mu.Unlock()
	featured := featuredHost(hosts)

	pageHeader(c, "Hosts", hostsSubtitle(hosts), func() {
		if button(c, buttonSecondary, "Refresh", false).Clicked() {
			a.backend.RefreshHosts()
		}
	})
	a.quickConnectBar(c, featured < 0)
	if connectErr != "" {
		mui.Row(c).FillWidth().Padding(theme.Space3, theme.Space4).Radius(theme.RadiusSm).
			Border(1, theme.RoleFault).Children(func() {
			label(c, "Could not connect: "+connectErr).TextColor(theme.RoleFault)
		})
	}
	if featured < 0 {
		hostsEmpty(c)
	} else {
		a.featuredCard(c, hosts[featured])
		if len(hosts) > 1 {
			mui.Column(c).FillWidth().Children(func() {
				label(c, "Also available").FontWeight(600).Margin(0, 0, theme.Space2)
				divider(c)
				for i, h := range hosts {
					if i == featured {
						continue
					}
					mui.Column(c).FillWidth().Key(h.ID).Children(func() {
						a.hostRow(c, h)
						divider(c)
					})
				}
			})
		}
	}
	a.tailnetFooter(c)
}

// quickConnectBar takes an address or name typed by hand. It holds the
// view's primary action when no host has been found.
func (a *App) quickConnectBar(c *mui.Context, primary bool) {
	mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
		in := mui.TextInput(c, &a.quickConnect).Grow(1).Height(theme.SizeControl).
			Placeholder("Host address or name").Label("Quick connect").Font(theme.FontMono).
			Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
		kind := buttonSecondary
		if primary {
			kind = buttonPrimary
		}
		b := button(c, kind, "Connect", false)
		target := strings.TrimSpace(a.quickConnect)
		if (b.Clicked() || in.Submitted()) && target != "" {
			a.connect(target, target)
		}
	})
}

func hostsEmpty(c *mui.Context) {
	mui.Column(c).FillWidth().Padding(theme.SpaceSection, theme.Space6).AlignItems(mui.Center).
		Gap(theme.Space3).Background(theme.RoleSurface).Border(1, theme.RoleBorder).
		Radius(theme.RadiusMd).Children(func() {
		sliceMeter(c, 0, 4, theme.RoleAccent)
		heading(c, "No hosts found yet", theme.TextDisplay)
		secondary(c, "Start Planck on the computer you want to play from, or type its address above.").
			TextAlign(mui.Center).MaxWidth(420)
	})
}

func (a *App) featuredCard(c *mui.Context, h HostInfo) {
	mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).
		Radius(theme.RadiusMd).Padding(theme.Space6, theme.Space6).Gap(theme.Space5).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space5).Children(func() {
			mui.Column(c).Grow(1).Gap(theme.Space2).Children(func() {
				if h.OS != "" {
					secondary(c, h.OS)
				}
				heading(c, h.Name, theme.TextPin)
				hostBadges(c, h)
			})
			if h.Online {
				b := button(c, buttonPrimary, "Connect", true).Label("Connect to " + h.Name)
				if b.Clicked() {
					a.connect(h.ID, h.Name)
				}
			} else {
				caption(c, "Offline")
			}
		})
		details := hostDetails(h)
		if len(details) > 0 {
			divider(c)
			mui.Row(c).Gap(theme.Space6 + theme.Space4).Children(func() {
				for _, d := range details {
					mui.Column(c).Gap(theme.Space1).Children(func() {
						secondary(c, d[0])
						mono(c, d[1]).FontSize(theme.TextTitle - 1)
					})
				}
			})
		}
	})
}

// hostDetails lists the readings a host reported, leaving out the rest.
func hostDetails(h HostInfo) [][2]string {
	var d [][2]string
	if r := resolutionLabel(h.Width, h.Height); r != "" {
		d = append(d, [2]string{"Resolution", r})
	}
	if h.RefreshHz > 0 {
		d = append(d, [2]string{"Refresh", fmt.Sprintf("%d Hz", h.RefreshHz)})
	}
	if h.Address != "" {
		d = append(d, [2]string{"Address", h.Address})
	}
	return d
}

func hostBadges(c *mui.Context, h HostInfo) {
	mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Wrap().Children(func() {
		if h.Online {
			pathBadge(c, h.PathKind, h.RTTMs)
		}
		if h.GPU != "" {
			gpuBadge(c, h.GPU)
		}
		if !h.Paired {
			capPill(c, "not paired")
		}
	})
}

func (a *App) hostRow(c *mui.Context, h HostInfo) {
	mui.Row(c).FillWidth().MinHeight(theme.SizeRow + 8).AlignItems(mui.Center).Gap(theme.Space4).
		PaddingX(theme.Space1).Children(func() {
		mui.Column(c).Width(220).Gap(2).Children(func() {
			label(c, h.Name).FontSize(theme.TextTitle).FontWeight(600)
			if h.OS != "" {
				secondary(c, h.OS)
			}
		})
		mui.Box(c).Grow(1).Children(func() { hostBadges(c, h) })
		if h.Online {
			b := button(c, buttonSecondary, "Connect", false).Label("Connect to " + h.Name)
			if b.Clicked() {
				a.connect(h.ID, h.Name)
			}
		} else {
			caption(c, "Offline")
		}
	})
}

func (a *App) tailnetFooter(c *mui.Context) {
	ts := a.backend.Tailnet()
	mui.Column(c).FillWidth().Children(func() {
		divider(c)
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Padding(theme.Space4, 0, 0).Children(func() {
			if ts.Running {
				sliceMeter(c, 3, 3, theme.RoleDirect)
				msg := "Tailscale connected"
				if ts.User != "" {
					msg += " as " + ts.User
				}
				secondary(c, msg)
			} else {
				sliceMeter(c, 0, 3, theme.RoleText3)
				secondary(c, "Tailscale is not running. LAN hosts still show here.")
			}
		})
	})
}
