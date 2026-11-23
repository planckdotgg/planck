package ui

import (
	"context"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

const pinLength = 6

// cleanPIN keeps the first six digits of what was typed or pasted.
func cleanPIN(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' && b.Len() < pinLength {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fingerprintLines splits a hex SHA-256 into upper-case groups of four,
// eight groups a line, for reading against the host's screen.
func fingerprintLines(fp string) []string {
	fp = strings.ToUpper(strings.ReplaceAll(fp, ":", ""))
	var groups []string
	for len(fp) > 0 {
		n := min(4, len(fp))
		groups = append(groups, fp[:n])
		fp = fp[n:]
	}
	var lines []string
	for len(groups) > 0 {
		n := min(8, len(groups))
		lines = append(lines, strings.Join(groups[:n], " "))
		groups = groups[n:]
	}
	return lines
}

func (a *App) pairingScreen(c *mui.Context) {
	p := a.backend.Pairing()
	a.mu.Lock()
	busy := a.pairBusy
	a.mu.Unlock()

	pageHeader(c, "Pairing", "Your own devices pair through Tailscale. Anyone else uses the PIN the host shows.", nil)
	if p.Status == PairNone {
		mui.Column(c).FillWidth().Padding(theme.Space7, theme.Space6).AlignItems(mui.Center).Gap(theme.Space3).
			Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).Children(func() {
			heading(c, "No pairing request", theme.TextDisplay)
			secondary(c, "Connect to a host you have not paired with, and Planck asks for it here.")
		})
		return
	}
	mui.Row(c).FillWidth().Gap(theme.Space5).AlignItems(mui.Stretch).Wrap().Children(func() {
		mui.Box(c).Grow(1).Basis(0).MinWidth(400).Children(func() { a.tailnetCard(c, p, busy) })
		mui.Box(c).Grow(1).Basis(0).MinWidth(400).Children(func() { a.pinCard(c, p, busy) })
	})
}

func pairStatusLine(c *mui.Context, p PairState, busy bool) {
	switch {
	case busy || p.Status == PairVerifying:
		label(c, "Checking with "+p.HostName).TextColor(theme.RoleText2)
	case p.Status == PairPaired:
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			sliceMeter(c, 3, 3, theme.RoleDirect)
			label(c, "Paired with "+p.HostName)
		})
	case p.Status == PairFailed && p.Error != "":
		label(c, p.Error).TextColor(theme.RoleFault)
	}
}

func (a *App) tailnetCard(c *mui.Context, p PairState, busy bool) {
	edge := theme.RoleBorder
	if p.TailscaleSameUser && p.Status == PairPending {
		edge = theme.RoleAccent
	}
	mui.Column(c).FillWidth().FillHeight().Padding(theme.Space6).Gap(theme.Space4).Background(theme.RoleSurface).
		Border(1, edge).Radius(theme.RadiusMd).Children(func() {
		heading(c, "On your tailnet", theme.TextHeading)
		if !p.TailscaleSameUser {
			secondary(c, p.HostName+" is not signed in to Tailscale as you, so it needs a PIN.")
			return
		}
		secondary(c, "Tailscale has confirmed "+p.HostName+" belongs to your account. No PIN needed.")
		heading(c, p.HostName, theme.TextDisplay)
		pairStatusLine(c, p, busy)
		if p.Status == PairPending || p.Status == PairFailed {
			mui.Row(c).Gap(theme.Space2).Children(func() {
				if button(c, buttonPrimary, "Trust "+p.HostName, false).Clicked() && !busy {
					a.pairing(a.backend.TrustTailscalePeer)
				}
				if button(c, buttonGhost, "Cancel", false).Clicked() {
					a.backend.CancelPairing()
				}
			})
		}
		if p.Status == PairPaired {
			a.pairedActions(c)
		}
	})
}

func (a *App) pinCard(c *mui.Context, p PairState, busy bool) {
	mui.Column(c).FillWidth().FillHeight().Padding(theme.Space6).Gap(theme.Space4).Background(theme.RoleSurface).
		Border(1, theme.RoleBorder).Radius(theme.RadiusMd).Children(func() {
		heading(c, "With a PIN", theme.TextHeading)
		if p.TailscaleSameUser {
			secondary(c, "Not needed for "+p.HostName+".")
			return
		}
		secondary(c, "Type the six digits "+p.HostName+" shows under Pairing.")
		in := mui.TextInput(c, &a.pin).Label("PIN").Placeholder("000000").Font(theme.FontMono).
			FontSize(theme.TextPin).LetterSpacing(12).Height(72).PaddingX(theme.Space5).
			Background(theme.RoleWell).Border(1, theme.RoleControlEdge).Radius(theme.RadiusMd).FillWidth()
		a.pin = cleanPIN(a.pin)
		if lines := fingerprintLines(p.Fingerprint); len(lines) > 0 {
			mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
				label(c, "Host fingerprint, SHA-256").FontWeight(600)
				secondary(c, "Check it matches the one "+p.HostName+" shows before you pair.")
				mui.Column(c).FillWidth().Padding(theme.Space3, theme.Space4).Background(theme.RoleWell).
					Radius(theme.RadiusSm).Gap(2).Label("Host fingerprint").Children(func() {
					for _, l := range lines {
						mono(c, l).FontSize(theme.TextCaption + 1).TextColor(theme.RoleText2)
					}
				})
			})
		}
		pairStatusLine(c, p, busy)
		if p.Status == PairPaired {
			a.pairedActions(c)
			return
		}
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			submit := button(c, buttonPrimary, "Pair", false).Clicked() || in.Submitted()
			if button(c, buttonGhost, "Cancel", false).Clicked() {
				a.pin = ""
				a.backend.CancelPairing()
			}
			if len(a.pin) < pinLength {
				caption(c, "Enter all six digits")
				return
			}
			if submit && !busy {
				pin := a.pin
				a.pairing(func(ctx context.Context) error { return a.backend.SubmitPIN(ctx, pin) })
			}
		})
	})
}

func (a *App) pairedActions(c *mui.Context) {
	if button(c, buttonSecondary, "Back to hosts", false).Clicked() {
		a.pin = ""
		a.screen = ScreenHosts
	}
}
