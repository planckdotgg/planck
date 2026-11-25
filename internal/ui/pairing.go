package ui

import (
	"context"
	"slices"
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

// shortFingerprint is a fingerprint's first and last four, as "7B4D…C219".
func shortFingerprint(fp string) string {
	fp = strings.ToUpper(strings.ReplaceAll(fp, ":", ""))
	if len(fp) <= 8 {
		return fp
	}
	return fp[:4] + "…" + fp[len(fp)-4:]
}

// pairView is where a frame of Hosts shows the pairing request: the panel
// under Quick connect for a PIN, or the host's card for a tailnet peer.
type pairView struct {
	p    PairState
	busy bool
	// hostID is the card of the host being paired, if one shows.
	hostID string
}

// open: a request still waiting on the user or the host. A paired one has
// started its stream, and the panel collapses.
func (v pairView) open() bool {
	switch v.p.Status {
	case PairPending, PairVerifying, PairFailed:
		return true
	}
	return false
}

// panel: the request shows under Quick connect, for a PIN, or for a
// tailnet peer whose card is not on the page.
func (v pairView) panel() bool { return v.open() && (!v.p.TailscaleSameUser || v.hostID == "") }

// onCard: a tailnet peer's request swaps its card's body.
func (v pairView) onCard(id string) bool {
	return v.open() && v.p.TailscaleSameUser && v.hostID == id
}

// waiting: the card of a host whose PIN the panel asks for.
func (v pairView) waiting(id string) bool {
	return v.open() && !v.p.TailscaleSameUser && v.hostID == id
}

func (a *App) pairingView(hosts []HostInfo) pairView {
	a.mu.Lock()
	busy := a.pairBusy
	a.mu.Unlock()
	v := pairView{p: a.backend.Pairing(), busy: busy}
	if !v.open() {
		return v
	}
	if i := slices.IndexFunc(hosts, func(h HostInfo) bool { return pairsWith(h, v.p) }); i >= 0 {
		v.hostID = hosts[i].ID
	}
	// A new failure clears the cells once.
	failed := v.p.Status == PairFailed
	if failed && !a.pairFailed {
		a.pin = ""
	}
	a.pairFailed = failed
	return v
}

// pairsWith: h is the host the request is for, by address or name.
func pairsWith(h HostInfo, p PairState) bool {
	if p.Address != "" {
		for _, addr := range []string{h.Address, h.LANAddress, h.TailscaleAddress} {
			if addr == p.Address {
				return true
			}
		}
	}
	return p.HostName != "" && (h.Name == p.HostName || h.AdvertisedName == p.HostName)
}

func (a *App) cancelPairing() {
	a.pin, a.fpMatch = "", false
	a.backend.CancelPairing()
}

// sectionLabel is a panel's head: mono 10 in caps, tracked out.
func sectionLabel(c *mui.Context, s string) mui.Element {
	return mui.Text(c, strings.ToUpper(s)).Font(theme.FontMono).FontSize(theme.TextMini).
		LetterSpacing(0.08 * theme.TextMini).TextColor(theme.RoleText3).SingleLine()
}

// pairingPanel opens under Quick connect while a host waits for its PIN.
func (a *App) pairingPanel(c *mui.Context, v pairView) {
	p := v.p
	mui.Row(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorderStrong).
		Radius(theme.RadiusMd).Clip().AlignItems(mui.Stretch).Label("Pairing with " + p.HostName).Children(func() {
		mui.Box(c).Absolute().Left(theme.Space5).Top(0).Size(28, 2).Background(theme.RoleAccent)
		if p.TailscaleSameUser {
			a.tailnetPairColumn(c, v)
			return
		}
		a.pinColumn(c, v)
		if lines := fingerprintLines(p.Fingerprint); len(lines) > 0 {
			mui.Box(c).Width(1).Background(theme.RoleBorder)
			a.fingerprintColumn(c, p, lines)
		}
	})
}

func (a *App) pinColumn(c *mui.Context, v pairView) {
	p := v.p
	mui.Column(c).Grow(1).Basis(0).Padding(20, theme.Space5).Gap(theme.Space4).Children(func() {
		a.pairStatusRow(c, p)
		mui.Column(c).Gap(2).Children(func() {
			label(c, "Enter the six digits on "+p.HostName+"’s screen").FontSize(theme.TextTitle).
				FixedLineHeight(theme.LeadingTitle).FontWeight(600)
			caption(c, p.HostName+" shows the code in Settings → Host while it hosts.").FontSize(12)
		})
		mui.Row(c).FillWidth().AlignItems(mui.End).Gap(theme.Space4).Children(func() {
			a.pinCells(c)
		})
		switch {
		case v.busy || p.Status == PairVerifying:
			caption(c, "Checking with "+p.HostName+"…").TextColor(theme.RoleText2)
		case p.Status == PairFailed && p.Error != "":
			caption(c, p.Error).TextColor(theme.RoleFault)
		}
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			ready := len(a.pin) == pinLength && (p.Fingerprint == "" || a.fpMatch) && !v.busy
			b := button(c, buttonPrimary, "Pair & connect", false).Width(168).Height(theme.SizeControlLg).Disabled(!ready)
			if b.Clicked() && ready {
				a.submitPIN()
			}
			if button(c, buttonSecondary, "Cancel", false).Width(92).Height(theme.SizeControlLg).Clicked() {
				a.cancelPairing()
			}
			mui.Spacer(c)
			paste := "Ctrl+V"
			if a.goos == "darwin" {
				paste = "⌘V"
			}
			caption(c, "Esc cancels. Paste a code with "+paste+" to fill all six cells.").MaxWidth(188)
		})
	})
}

func (a *App) submitPIN() {
	pin := a.pin
	a.pairing(func(ctx context.Context) error { return a.backend.SubmitPIN(ctx, pin) })
}

// pairStatusRow names the host and the route to it.
func (a *App) pairStatusRow(c *mui.Context, p PairState) {
	mui.Row(c).FillWidth().Height(20).Gap(theme.Space3).AlignItems(mui.Center).Children(func() {
		mui.Box(c).PaddingX(6).Height(18).AlignItems(mui.Center).Justify(mui.Center).
			Radius(theme.RadiusXs).Border(1, theme.RoleBorderStrong).Children(func() {
			mono(c, "PAIRING").FontSize(theme.TextMini).LetterSpacing(0.08 * theme.TextMini).TextColor(theme.RoleText2)
		})
		who := p.HostName
		if p.Address != "" && p.Address != p.HostName {
			who += " · " + p.Address
		}
		mono(c, who).FontSize(12).TextColor(theme.RoleText2).Shrink(1)
		mui.Spacer(c)
		// Discovery measured the route only when it timed the round trip.
		if p.RTTMs > 0 {
			col := signalColor(p.PathKind)
			sliceMeter(c, pathBars(p.PathKind), 3, col)
			mono(c, pathLabel(p.PathKind)+" · "+formatMs(p.RTTMs)+" ms").FontSize(12).TextColor(col)
		}
	})
}

// pinCells draws six cells over one field that takes the digits, typed or
// pasted.
func (a *App) pinCells(c *mui.Context) {
	const cellW, cellH, gap = 56, 60, 10
	mui.Box(c).Size(6*cellW+5*gap, cellH).Shrink(0).Children(func() {
		in := mui.TextInputBase(c.Key("pin"), &a.pin).Absolute().Left(0).Top(0).Size(6*cellW+5*gap, cellH).
			Label("PIN").TextColor(mui.Color{}).CaretColor(mui.Color{}).
			OnShortcut(0, mui.KeyEscape, a.cancelPairing)
		a.pin = cleanPIN(a.pin)
		if in.Submitted() && len(a.pin) == pinLength && (a.backend.Pairing().Fingerprint == "" || a.fpMatch) {
			a.submitPIN()
		}
		focused := in.Focused()
		mui.Row(c).Gap(gap).IgnoreInput(true).Children(func() {
			for i := range pinLength {
				cell := mui.Box(c).Size(cellW, cellH).Radius(theme.RadiusSm).Background(theme.RoleWell).
					AlignItems(mui.Center).Justify(mui.Center)
				edge := theme.RoleBorder
				caret := focused && i == len(a.pin)
				if caret {
					edge = theme.RoleBorderStrong
				}
				cell.Border(1, edge).Children(func() {
					if i < len(a.pin) {
						mui.Text(c, a.pin[i:i+1]).Font(theme.FontMono).FontSize(28).FixedLineHeight(32).
							FontWeight(500).TextColor(theme.RoleText)
					} else if caret {
						mui.Box(c).Size(2, 30).Background(theme.RoleAccent)
					}
				})
			}
		})
	})
}

func (a *App) fingerprintColumn(c *mui.Context, p PairState, lines []string) {
	mui.Column(c).Width(404).Shrink(0).Padding(20, theme.Space5).Gap(theme.Space3).Children(func() {
		sectionLabel(c, "Host fingerprint · SHA-256")
		mui.Column(c).FillWidth().Padding(10, theme.Space3).Background(theme.RoleWell).Border(1, theme.RoleBorder).
			Radius(theme.RadiusSm).Gap(0).Label("Host fingerprint").Children(func() {
			for _, l := range lines {
				mono(c, l).FontSize(theme.TextCaption + 1).FixedLineHeight(theme.LeadingBody).LetterSpacing(0.04 * 12)
			}
		})
		caption(c, p.HostName+" shows the same characters. If they differ, cancel — something is sitting between you.").
			FontSize(12).FixedLineHeight(18)
		a.matchCheck(c)
		divider(c)
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Start).Children(func() {
			sliceMeter(c, 3, 3, theme.RoleDirect).Margin(3, 0, 0)
			caption(c, "Devices on your own tailnet skip the code: Planck checks the Tailscale identity instead.").
				Grow(1).Shrink(1)
		})
	})
}

// matchCheck is the tick that the fingerprints match, which a PIN pairing
// needs before it can go.
func (a *App) matchCheck(c *mui.Context) {
	cb := mui.CheckboxBase(c.Key("fp-match"), &a.fpMatch).Label("They match on both screens").
		Gap(10).AlignItems(mui.Center).Cursor(mui.CursorPointer)
	on := a.fpMatch
	cb.Children(func() {
		mui.Box(c).Size(18, 18).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
			p.Stroke(r, theme.RoleDirect, theme.RadiusXs, 1.5)
			if on {
				p.StrokePolyline([][2]float32{{r.X + 4.5, r.Y + 9.5}, {r.X + 7.5, r.Y + 12.5}, {r.X + 13.5, r.Y + 5.5}}, 1.5, theme.RoleDirect)
			}
		})
		label(c, "They match on both screens").FontWeight(500)
	})
}

// tailnetPairColumn is the panel's body for a tailnet peer with no card.
func (a *App) tailnetPairColumn(c *mui.Context, v pairView) {
	p := v.p
	mui.Column(c).Grow(1).Padding(20, theme.Space5).Gap(theme.Space3).Children(func() {
		a.pairStatusRow(c, p)
		trustLine(c)
		a.trustMeta(c, p)
		a.trustStatus(c, v)
		mui.Row(c).Gap(theme.Space2).Children(func() {
			a.trustActions(c, v, func(e mui.Element) mui.Element { return e.Width(168) })
		})
	})
}

// trustBody swaps a tailnet peer's card body: its name, the identity that
// lets it pair with no code, and the choice.
func (a *App) trustBody(c *mui.Context, h HostInfo, v pairView) {
	mui.Column(c).FillWidth().Height(cardSize - plateHeight).Shrink(0).Padding(theme.Space3).Gap(2).Children(func() {
		heading(c, h.Name, theme.TextHeading).FixedLineHeight(theme.LeadingHeading).SingleLine().FillWidth()
		trustLine(c)
		a.trustMeta(c, v.p)
		mui.Spacer(c)
		a.trustStatus(c, v)
		mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
			a.trustActions(c, v, func(e mui.Element) mui.Element { return e.Grow(1) })
		})
	})
}

func trustLine(c *mui.Context) {
	mui.Row(c).Gap(6).AlignItems(mui.Center).Children(func() {
		mui.Icon(c, theme.IconShieldCheck).Size(18, 18).TextColor(theme.RoleDirect)
		mono(c, "your tailnet · no code").FontSize(theme.TextCaption).FontWeight(500).TextColor(theme.RoleDirect)
	})
}

func (a *App) trustMeta(c *mui.Context, p PairState) {
	var parts []string
	if u := a.backend.Tailnet().User; u != "" {
		parts = append(parts, u)
	}
	if p.Fingerprint != "" {
		parts = append(parts, "fp "+shortFingerprint(p.Fingerprint))
	}
	if len(parts) > 0 {
		cardMeta(c, strings.Join(parts, " · "), theme.RoleText3)
	}
}

func (a *App) trustStatus(c *mui.Context, v pairView) {
	switch {
	case v.busy || v.p.Status == PairVerifying:
		caption(c, "Checking with "+v.p.HostName+"…").TextColor(theme.RoleText2)
	case v.p.Status == PairFailed && v.p.Error != "":
		caption(c, v.p.Error).TextColor(theme.RoleFault)
	}
}

// trustActions are Trust & connect, the page's one ember while no PIN
// panel is open, and Not now.
func (a *App) trustActions(c *mui.Context, v pairView, size func(mui.Element) mui.Element) {
	name := v.p.HostName
	if size(cardButton(c, ctaAccent, "Trust & connect", "Trust "+name, false)).Clicked() && !v.busy {
		a.pairing(a.backend.TrustTailscalePeer)
	}
	if cardButton(c, ctaOutline, "Not now", "Not now", false).Width(72).Clicked() {
		a.cancelPairing()
	}
}
