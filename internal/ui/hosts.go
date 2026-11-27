package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/discovery"
)

const (
	cardSize float32 = 256
	// hostsColumn fits four cards and their gaps.
	hostsColumn = 4*cardSize + 3*theme.Space4
	cardInner   = cardSize - 2*theme.Space3 - 2
	plateHeight = 120
)

// cardState is which of the design's states a host card is drawn in.
type cardState int

const (
	cardOnline cardState = iota
	cardUnpaired
	cardConnected
	cardOffline
)

func hostCardState(h HostInfo) cardState {
	switch {
	case h.Connected:
		return cardConnected
	case !h.Online:
		return cardOffline
	case !h.Paired:
		return cardUnpaired
	}
	return cardOnline
}

// sortHosts orders the grid: the active session, online paired hosts by
// round trip (unmeasured last), online unpaired, then offline.
func sortHosts(hosts []HostInfo) {
	rank := func(h HostInfo) int {
		switch hostCardState(h) {
		case cardConnected:
			return 0
		case cardOnline:
			return 1
		case cardUnpaired:
			return 2
		}
		return 3
	}
	rtt := func(h HostInfo) float64 {
		if h.RTTMs <= 0 {
			return 1e9
		}
		return h.RTTMs
	}
	slices.SortStableFunc(hosts, func(a, b HostInfo) int {
		if r := cmp.Compare(rank(a), rank(b)); r != 0 || rank(a) != 1 {
			return r
		}
		return cmp.Compare(rtt(a), rtt(b))
	})
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
	sortHosts(hosts)
	pv := a.pairingView(hosts)
	a.mu.Lock()
	connectErr := a.connectErr
	a.mu.Unlock()
	if a.renaming != "" && !slices.ContainsFunc(hosts, func(h HostInfo) bool { return h.ID == a.renaming }) {
		a.cancelRename()
	}
	if a.removing != "" && !slices.ContainsFunc(hosts, func(h HostInfo) bool { return h.ID == a.removing && canRemove(h) }) {
		a.cancelRemove()
	}

	mui.Column(c).FillWidth().MaxWidth(hostsColumn).AlignSelf(mui.Center).Gap(theme.Space5).Children(func() {
		sub := hostsSubtitle(hosts)
		if pv.open() {
			sub += " · 1 pairing now"
		}
		pageHeader(c, "Hosts", sub, func() {
			if button(c, buttonSecondary, "Refresh", false).Clicked() {
				a.backend.RefreshHosts()
			}
		})
		a.quickConnectBar(c, !pv.panel())
		if pv.panel() {
			a.pairingPanel(c, pv)
		}
		if connectErr != "" {
			mui.Row(c).FillWidth().Padding(theme.Space3, theme.Space4).Radius(theme.RadiusSm).
				Border(1, theme.RoleFault).Children(func() {
				label(c, "Could not connect: "+connectErr).TextColor(theme.RoleFault)
			})
		}
		if len(hosts) == 0 {
			a.hostsEmpty(c)
		} else {
			a.hostsGrid(c, hosts, pv)
		}
		a.tailnetFooter(c, hosts)
	})
}

// quickConnectHint says what the field takes.
const quickConnectHint = "address · name · peer ID · planck.gg/j/… share link"

// quickConnectBar takes an address, a name, a peer fingerprint or a share
// link typed by hand. Its Connect is the page's ember unless the pairing
// panel holds it.
func (a *App) quickConnectBar(c *mui.Context, primary bool) {
	mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
		field := mui.Row(c).Grow(1).Height(pageFirstRowH).PaddingX(14).Gap(10).AlignItems(mui.Center).
			Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
		var in mui.Element
		field.Children(func() {
			mui.Icon(c, theme.IconPrompt).Size(theme.SizeIcon, theme.SizeIcon).TextColor(theme.RoleText3)
			in = mui.TextInputBase(c.Key("quick-connect"), &a.quickConnect).Grow(1).Shrink(1).FillHeight().
				Label("Quick connect").Font(theme.FontMono).FontSize(14).FontWeight(500).
				TextColor(theme.RoleText).CaretColor(theme.RoleAccent)
			caption(c, quickConnectHint).SingleLine().Shrink(0)
		})
		if field.FocusWithin() {
			field.Border(1, theme.RoleFocus)
		}
		if a.focusQuick {
			a.focusQuick = false
			in.Focus()
		}
		kind := buttonSecondary
		if primary {
			kind = buttonPrimary
		}
		b := button(c, kind, "Connect", false).Width(132).Height(pageFirstRowH).Shrink(0)
		target := strings.TrimSpace(a.quickConnect)
		if (b.Clicked() || in.Submitted()) && target != "" {
			a.connect(target, target)
		}
	})
}

func (a *App) hostsEmpty(c *mui.Context) {
	mui.Column(c).FillWidth().Height(cardSize).Padding(theme.Space6).AlignItems(mui.Center).Justify(mui.Center).
		Gap(theme.Space4).Background(theme.RoleSurface).Border(1, theme.RoleBorder).
		Radius(theme.RadiusMd).Children(func() {
		sliceStack(c, 120, sliceStyle{
			landed:   [3]mui.Color{theme.RoleText3.Alpha(0.12), theme.RoleText3.Alpha(0.12), theme.RoleText3.Alpha(0.12)},
			flight:   44,
			flightOf: theme.RoleAccent,
			animate:  true,
		})
		heading(c, "Looking for hosts", theme.TextHeading)
		label(c, "Start Planck on the computer you want to play from. It shows up here on its own, over your LAN or your tailnet.").
			TextColor(theme.RoleText3).FixedLineHeight(theme.LeadingBody).TextAlign(mui.Center).MaxWidth(360)
		mui.Row(c).Gap(theme.Space1).AlignItems(mui.Baseline).Children(func() {
			caption(c, "Know the address?")
			b := mui.ButtonBase(c).Label("Use quick connect").Cursor(mui.CursorPointer)
			b.Children(func() {
				caption(c, "Use quick connect").TextColor(theme.RoleAccent).FontWeight(600).Underline()
			})
			if b.Clicked() {
				a.focusQuick = true
				a.Notify()
			}
		})
	})
}

func (a *App) hostsGrid(c *mui.Context, hosts []HostInfo, pv pairView) {
	mui.Row(c).FillWidth().Wrap().Gap(theme.Space4).AlignContent(mui.Start).Children(func() {
		for _, h := range hosts {
			a.hostCard(c, h, pv)
		}
	})
}

// canConnect: a host found now, or a paired one at its last address,
// which discovery may never find (a host typed in by address).
func canConnect(h HostInfo) bool { return h.Online || h.Paired && h.Address != "" }

func (a *App) hostCard(c *mui.Context, h HostInfo, pv pairView) {
	st := hostCardState(h)
	card := mui.Column(c).Key(h.ID).Size(cardSize, cardSize).Shrink(0).Radius(theme.RadiusMd).Clip()
	hover := card.Hovered()
	bg, edge := theme.RoleSurface, theme.RoleBorder
	switch {
	case st == cardConnected:
		edge = theme.RoleBorder.Mix(theme.RoleAccent, 0.55)
	case hover && st != cardOffline:
		bg, edge = theme.RoleRaised, theme.RoleBorderStrong
	}
	card.Background(bg).Border(1, edge)
	if card.FocusWithin() && a.renaming != h.ID {
		card.DrawOver(func(p *mui.Painter, r mui.Rect) {
			p.Stroke(r, theme.RoleFocus, theme.RadiusMd, 2)
		})
	}
	card.Children(func() {
		a.slicePlate(c, h, st, hover, pv.waiting(h.ID))
		switch {
		case pv.onCard(h.ID):
			a.trustBody(c, h, pv)
		case a.renaming == h.ID:
			a.renameBody(c, h)
		case a.removing == h.ID:
			a.removeBody(c, h)
		default:
			a.cardBody(c, h, st, hover, pv.waiting(h.ID))
		}
	})
}

// slicePlate is the card's top: status, the frame arriving in slices, and
// the route pill.
func (a *App) slicePlate(c *mui.Context, h HostInfo, st cardState, hover, pairing bool) {
	plateBg := theme.RoleWell
	if st == cardOffline {
		plateBg = theme.RoleSurface.Mix(theme.RoleWell, 0.6)
	}
	plate := mui.Column(c).FillWidth().Height(plateHeight - 1).Padding(theme.Space3).Justify(mui.SpaceBetween).
		Background(plateBg)
	if st == cardConnected {
		plate.Gradient(theme.RoleWell.Mix(theme.RoleAccent, 0.14), theme.RoleWell, 0)
	}
	plate.Children(func() {
		mui.Row(c).FillWidth().Height(16).Gap(7).AlignItems(mui.Center).Children(func() {
			statusDot(c, st, pairing)
			text, col, weight := "Online", theme.RoleText2, 500
			switch {
			case pairing:
				text = "Pairing"
			case st == cardConnected:
				text, col, weight = "Connected", theme.RoleAccent, 600
			case st == cardUnpaired:
				text = "Not paired"
			case st == cardOffline:
				text, col = "Offline", theme.RoleText3
			}
			caption(c, text).TextColor(col).FontWeight(weight).SingleLine()
			mui.Spacer(c)
			if h.Paired {
				a.streamSettingsButton(c, h, hover)
			}
			a.pencil(c, h, hover)
			if canRemove(h) {
				a.removeButton(c, h, hover)
			}
		})
		sliceStack(c, cardInner, plateSlices(h, st))
		a.routePill(c, h, st)
	})
	bottom := theme.RoleBorder
	if st == cardConnected {
		bottom = theme.RoleBorder.Mix(theme.RoleAccent, 0.4)
	}
	mui.Box(c).FillWidth().Height(1).Shrink(0).Background(bottom)
}

func statusDot(c *mui.Context, st cardState, pairing bool) {
	mui.Box(c).Size(6, 6).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		switch {
		case pairing:
			p.Stroke(r, theme.RoleText2, 3, 1.5)
		case st == cardOffline:
			p.Stroke(r, theme.RoleText3, 3, 1.5)
		case st == cardConnected:
			p.Fill(r, theme.RoleAccent, 3)
		default:
			p.Fill(r, theme.RoleDirect, 3)
		}
	})
}

func (a *App) pencil(c *mui.Context, h HostInfo, hover bool) {
	b := mui.ButtonBase(c).Size(theme.SizeTargetMin, theme.SizeTargetMin).Margin(-4).Shrink(0).
		Radius(theme.RadiusXs).Label("Rename " + h.Name).Cursor(mui.CursorPointer)
	fg := theme.RoleText3
	if hover || b.Hovered() || a.renaming == h.ID {
		fg = theme.RoleText
	}
	b.Children(func() {
		mui.Icon(c, theme.IconEdit).Size(theme.SizeIcon, theme.SizeIcon).TextColor(fg)
	})
	if b.Clicked() {
		if a.renaming == h.ID {
			a.cancelRename()
		} else {
			a.cancelRemove()
			a.renaming, a.renameText, a.renameOpen, a.renameErr = h.ID, h.Name, true, ""
		}
	}
}

// canRemove: a paired host, or one no longer found. An unpaired host that
// still announces itself would come straight back.
func canRemove(h HostInfo) bool { return !h.Connected && (h.Paired || !h.Online) }

// removeButton forgets h: at once for a host that is not paired, after a
// confirmation in the card for one that is, which needs its PIN again.
func (a *App) removeButton(c *mui.Context, h HostInfo, hover bool) {
	b := mui.ButtonBase(c).Size(theme.SizeTargetMin, theme.SizeTargetMin).Margin(-4).Shrink(0).
		Radius(theme.RadiusXs).Label("Remove " + h.Name).Cursor(mui.CursorPointer)
	fg := theme.RoleText3
	switch {
	case b.Hovered() || a.removing == h.ID:
		fg = theme.RoleFault
	case hover:
		fg = theme.RoleText
	}
	b.Children(func() {
		mui.Icon(c, theme.IconClose).Size(theme.SizeIcon, theme.SizeIcon).TextColor(fg)
	})
	if !b.Clicked() {
		return
	}
	switch {
	case a.removing == h.ID:
		a.cancelRemove()
	case h.Paired:
		a.cancelRename()
		a.removing, a.removeErr = h.ID, ""
	default:
		a.removeHost(h)
	}
}

// removeBody replaces a card's body while the user confirms forgetting a
// paired host.
func (a *App) removeBody(c *mui.Context, h HostInfo) {
	mui.Column(c).FillWidth().Height(cardSize - plateHeight).Shrink(0).Padding(theme.Space3).Gap(theme.Space1).Children(func() {
		heading(c, "Remove "+h.Name+"?", theme.TextBody).FixedLineHeight(theme.LeadingHeading).SingleLine().FillWidth()
		text, col := "Connecting again needs its PIN.", theme.RoleText3
		if a.removeErr != "" {
			text, col = a.removeErr, theme.RoleFault
		}
		caption(c, text).TextColor(col).FixedLineHeight(14).SingleLine().FillWidth()
		mui.Spacer(c)
		mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
			if cardButton(c, ctaDisconnect, "Remove", "Confirm remove "+h.Name, false).Grow(1).Clicked() {
				a.removeHost(h)
			}
			if cardButton(c, ctaOutline, "Cancel", "Keep "+h.Name, false).Clicked() {
				a.cancelRemove()
			}
		})
	})
}

func (a *App) removeHost(h HostInfo) {
	if err := a.backend.RemoveHost(h.ID); err != nil {
		a.removing, a.removeErr = h.ID, err.Error()
		return
	}
	a.cancelRemove()
}

func (a *App) cancelRemove() { a.removing, a.removeErr = "", "" }

// streamSettingsButton opens Settings on the host's own stream format.
func (a *App) streamSettingsButton(c *mui.Context, h HostInfo, hover bool) {
	b := mui.ButtonBase(c).Size(theme.SizeTargetMin, theme.SizeTargetMin).Margin(-4).Shrink(0).
		Radius(theme.RadiusXs).Label("Stream settings for " + h.Name).Cursor(mui.CursorPointer)
	fg := theme.RoleText3
	if hover || b.Hovered() {
		fg = theme.RoleText
	}
	b.Children(func() {
		mui.Icon(c, theme.IconSetting).Size(theme.SizeIcon, theme.SizeIcon).TextColor(fg)
	})
	if b.Clicked() {
		a.openHostSettings(h)
	}
}

// sliceStyle is one look of the slice stack: three landed slices and the
// slice in flight, flight wide; a flight of 0 draws none.
type sliceStyle struct {
	landed   [3]mui.Color
	flight   float32
	flightOf mui.Color
	animate  bool
}

func plateSlices(h HostInfo, st cardState) sliceStyle {
	t3 := theme.RoleText3
	s := sliceStyle{landed: [3]mui.Color{t3.Alpha(0.16), t3.Alpha(0.26), t3.Alpha(0.40)}}
	switch {
	case st == cardOffline:
		s.landed = [3]mui.Color{t3.Alpha(0.10), t3.Alpha(0.10), t3.Alpha(0.10)}
	case st == cardConnected:
		s.landed = [3]mui.Color{theme.SpectrumK4000.Alpha(0.30), theme.SpectrumK3000.Alpha(0.45), theme.SpectrumK2000.Alpha(0.60)}
		s.flight, s.flightOf, s.animate = 0.82*cardInner, theme.RoleAccent, true
	case h.PathKind == discovery.PathTailscaleDERP:
		s.flight, s.flightOf = 0.44*cardInner, theme.RoleRelay
	default:
		s.flight, s.flightOf = 0.68*cardInner, theme.RoleDirect
	}
	return s
}

// sliceLoop is how often the slice in flight arrives again; it grows over
// theme.DurBase and then holds.
const sliceLoop = time.Second

// sliceStack draws the frame arriving in slices: four 6 px bars, the last
// one the slice still in flight.
func sliceStack(c *mui.Context, w float32, s sliceStyle) mui.Element {
	const bar, gap = 6, 4
	return mui.Box(c).Size(w, 4*bar+3*gap).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		for i, col := range s.landed {
			p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*(bar+gap)), W: r.W, H: bar}, col, 1)
		}
		if s.flight <= 0 {
			return
		}
		fw := s.flight
		if s.animate && !p.Preferences().ReduceMotion {
			grow := float32(time.Duration(theme.DurBase) * time.Millisecond)
			t := float32(p.Now().UnixNano() % int64(sliceLoop))
			if t < grow {
				k := t / grow
				fw *= 1 - (1-k)*(1-k)
			}
			p.AnimationFrame()
		}
		p.Fill(mui.Rect{X: r.X, Y: r.Y + 3*(bar+gap), W: fw, H: bar}, s.flightOf, 1)
	})
}

// routeShort is the route's name on a card's pill.
func routeShort(k discovery.PathKind) string {
	switch k {
	case discovery.PathLANDirect:
		return "LAN"
	case discovery.PathTailscaleDirect:
		return "TS"
	case discovery.PathTailscaleDERP:
		return "Relay"
	}
	return "Unknown"
}

// routePill is the card's split pill: route, round trip, and +TS when the
// host answers on both LAN and Tailscale. A relay drops the round trip; a
// streaming card shows its frame rate; an offline card keeps only its last
// route, greyed.
func (a *App) routePill(c *mui.Context, h HostInfo, st cardState) {
	col := signalColor(h.PathKind)
	relay := h.PathKind == discovery.PathTailscaleDERP
	offline := st == cardOffline
	edge := theme.RoleBorder
	if relay && !offline {
		edge = theme.RoleRelay
	}
	labelCol := theme.RoleText
	switch {
	case offline:
		labelCol = theme.RoleText3
	case relay:
		labelCol = theme.RoleRelay
	}
	var parts []func()
	if !offline && !relay && h.RTTMs > 0 {
		parts = append(parts, func() {
			mui.Box(c).PaddingX(9).Children(func() {
				mono(c, formatMs(h.RTTMs)+" ms").FontSize(theme.TextCaption).TextColor(col)
			})
		})
	}
	if !offline && h.LANReachable && h.TailscaleReachable {
		parts = append(parts, func() {
			mui.Box(c).PaddingX(8).Children(func() {
				mono(c, "+TS").FontSize(theme.TextCaption).TextColor(theme.RoleText3)
			})
		})
	}
	if st == cardConnected {
		if tel := a.backend.Telemetry(); tel.FPS > 0 {
			parts = append(parts, func() {
				mui.Box(c).PaddingX(8).Children(func() {
					mono(c, fmt.Sprintf("%.0f fps", tel.FPS)).FontSize(theme.TextCaption).TextColor(theme.RoleAccent)
				})
			})
		}
	}
	mui.Row(c).Height(22).AlignSelf(mui.Start).AlignItems(mui.Center).Radius(theme.RadiusPill).
		Background(theme.RoleRaised).Border(1, edge).Clip().Children(func() {
		mui.Row(c).Gap(6).AlignItems(mui.Center).PaddingStart(10).PaddingEnd(8).Children(func() {
			bars := pathBars(h.PathKind)
			if offline {
				bars = 0
			}
			routeMeter(c, bars, col)
			mui.Text(c, routeShort(h.PathKind)).Font(theme.FontUI).FontSize(theme.TextCaption).FontWeight(500).
				TextColor(labelCol).SingleLine()
		})
		for _, part := range parts {
			mui.Box(c).Width(1).FillHeight().Background(theme.RoleBorder)
			part()
		}
	})
}

// routeMeter is the pill's 10 x 8 meter: three 2 px bars, filled from the
// top.
func routeMeter(c *mui.Context, filled int, col mui.Color) {
	const barH, gap, w = 2, 1, 10
	mui.Box(c).Size(w, 3*barH+2*gap).Shrink(0).Draw(func(p *mui.Painter, r mui.Rect) {
		for i := range 3 {
			bc := theme.RoleBorderStrong
			if i < filled {
				bc = col
			}
			p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*(barH+gap)), W: w, H: barH}, bc, 0)
		}
	})
}

// identityLine is the card's second line: the advertised name under a
// local alias, else the OS, else the last address.
func identityLine(h HostInfo) string {
	switch {
	case h.Alias != "":
		return "advertised: " + h.AdvertisedName
	case h.OS != "":
		return h.OS
	case h.Address != h.Name:
		return h.Address
	}
	return ""
}

// hardwareLine reads as "RTX 4080 · 1080p 60".
func hardwareLine(h HostInfo) string {
	var parts []string
	if h.GPU != "" {
		parts = append(parts, h.GPU)
	}
	switch {
	case h.Height > 0 && h.RefreshHz > 0:
		parts = append(parts, fmt.Sprintf("%dp %d", h.Height, h.RefreshHz))
	case h.Height > 0:
		parts = append(parts, fmt.Sprintf("%dp", h.Height))
	}
	return strings.Join(parts, " · ")
}

func (a *App) cardBody(c *mui.Context, h HostInfo, st cardState, hover, pairing bool) {
	mui.Column(c).FillWidth().Height(cardSize - plateHeight).Shrink(0).Padding(theme.Space3).Children(func() {
		nameCol := theme.RoleText
		if st == cardOffline {
			nameCol = theme.RoleText2
		}
		heading(c, h.Name, theme.TextHeading).FixedLineHeight(theme.LeadingHeading).LetterSpacing(-0.005 * theme.TextHeading).
			TextColor(nameCol).SingleLine().FillWidth()
		if s := identityLine(h); s != "" {
			cardMeta(c, s, theme.RoleText3).Margin(2, 0, 0)
		}
		if s := hardwareLine(h); s != "" {
			cardMeta(c, s, theme.RoleText2)
		}
		mui.Spacer(c)
		if !pairing && h.Paired {
			a.calStatusLine(c, h, st)
			mui.Box(c).Height(theme.Space2).Shrink(0)
		}
		if pairing {
			waitingRow(c)
			return
		}
		a.cardActions(c, h, st, hover)
	})
}

// waitingRow stands in for a card's button while its host's PIN is asked
// for in the panel above.
func waitingRow(c *mui.Context) {
	mui.Row(c).FillWidth().Height(theme.SizeControl).Gap(theme.Space2).Radius(theme.RadiusSm).Border(1, theme.RoleBorder).
		AlignItems(mui.Center).Justify(mui.Center).Children(func() {
		mui.Box(c).Size(22, 10).Draw(func(p *mui.Painter, r mui.Rect) {
			for i := range 3 {
				col := theme.RoleBorderStrong
				if i < 2 {
					col = theme.RoleText2
				}
				p.Fill(mui.Rect{X: r.X, Y: r.Y + float32(i*4), W: r.W, H: 2}, col, 0)
			}
		})
		label(c, "Waiting for the code").FontWeight(500).TextColor(theme.RoleText2).SingleLine()
	})
}

func cardMeta(c *mui.Context, s string, col mui.Color) mui.Element {
	return mui.Text(c, s).Font(theme.FontMono).FontSize(theme.TextCaption).FixedLineHeight(14).TextColor(col).
		SingleLine().FillWidth()
}

func (a *App) cardActions(c *mui.Context, h HostInfo, st cardState, hover bool) {
	switch {
	case st == cardConnected:
		mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
			if cardButton(c, ctaAccent, "Resume", "Resume "+h.Name, false).Grow(1).Clicked() {
				a.resume(h)
			}
			if cardButton(c, ctaDisconnect, "Disconnect", "Disconnect from "+h.Name, false).Clicked() {
				a.disconnect()
			}
		})
	case st == cardUnpaired:
		if cardButton(c, ctaOutline, "Pair & Connect", "Connect to "+h.Name, false).FillWidth().Clicked() {
			a.startConnect(h.ID, h.Name, false)
		}
	case st == cardOffline && canConnect(h):
		if cardButton(c, ctaMuted, "Reconnect", "Connect to "+h.Name, false).FillWidth().Clicked() {
			a.connect(h.ID, h.Name)
		}
	case st == cardOffline:
		mui.Box(c).FillWidth().Height(theme.SizeControl).Radius(theme.RadiusSm).Border(1, theme.RoleBorder).
			AlignItems(mui.Center).Justify(mui.Center).Children(func() {
			label(c, "Offline").FontWeight(600).TextColor(theme.RoleText3).SingleLine()
		})
	default:
		if cardButton(c, ctaConnect, "Connect", "Connect to "+h.Name, hover).FillWidth().Clicked() {
			a.connect(h.ID, h.Name)
		}
	}
}

// ctaKind is a card button's look.
type ctaKind int

const (
	// ctaConnect is raised, and takes the ember while its card is hovered.
	ctaConnect ctaKind = iota
	ctaAccent
	// ctaOutline has no fill: Pair & Connect and Cancel.
	ctaOutline
	ctaMuted
	ctaDisconnect
)

func cardButton(c *mui.Context, kind ctaKind, text, name string, cardHover bool) mui.Element {
	b := mui.ButtonBase(c).Label(name).Height(theme.SizeControl).PaddingX(theme.Space3).Radius(theme.RadiusSm).
		Cursor(mui.CursorPointer)
	hover := b.Hovered()
	fg := theme.RoleText2
	switch kind {
	case ctaConnect:
		fg = theme.RoleText
		b.Background(theme.RoleRaised).Border(1, theme.RoleBorderStrong)
		if cardHover || hover {
			b.Background(theme.RoleAccent).Border(1, theme.RoleAccent)
			fg = theme.RoleOnAccent
		}
	case ctaAccent:
		bg := theme.RoleAccent
		if b.Pressed() {
			bg = theme.RoleAccentPressed
		} else if hover {
			bg = theme.RoleAccentHover
		}
		b.Background(bg)
		fg = theme.RoleOnAccent
	case ctaOutline:
		b.Border(1, theme.RoleBorderStrong)
		if hover {
			b.Background(theme.RoleHover)
		}
	case ctaMuted:
		b.Border(1, theme.RoleBorder)
		if hover {
			b.Background(theme.RoleHover)
		}
	case ctaDisconnect:
		b.Border(1, theme.RoleBorderStrong)
		if hover {
			fg = theme.RoleFault
		}
	}
	b.Children(func() {
		mui.Text(c, text).Font(theme.FontUI).FontSize(theme.TextBody).FontWeight(600).TextColor(fg).SingleLine()
	})
	return b
}

// renameBody replaces a card's body while its host is renamed: a local
// alias that never leaves this client.
func (a *App) renameBody(c *mui.Context, h HostInfo) {
	mui.Column(c).FillWidth().Height(cardSize - plateHeight).Shrink(0).Padding(theme.Space3).Gap(theme.Space1).Children(func() {
		caption(c, "Local name").FixedLineHeight(14).FontWeight(500)
		in := mui.TextInput(c.Key("rename:"+h.ID), &a.renameText).Width(cardInner).Height(theme.SizeControl).
			PaddingX(10).Radius(theme.RadiusSm).Background(theme.RoleRaised).Border(1, theme.RoleFocus).
			FontSize(theme.TextBody).FontWeight(500).CaretColor(theme.RoleAccent).Label("Local name for "+h.AdvertisedName).
			OnShortcut(0, mui.KeyEscape, a.cancelRename)
		if a.renameOpen {
			a.renameOpen = false
			in.Focus().SetTextSelection(0, utf8.RuneCountInString(a.renameText))
		}
		if in.Submitted() {
			a.saveRename(h, a.renameText)
		}
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Padding(2, 0, 0).Children(func() {
			if a.renameErr != "" {
				caption(c, a.renameErr).TextColor(theme.RoleFault).SingleLine().Grow(1).Shrink(1)
			} else {
				cardMeta(c, "advertised: "+h.AdvertisedName, theme.RoleText3).Grow(1).Shrink(1)
			}
			if h.Alias != "" {
				b := mui.ButtonBase(c).Label("Reset").Cursor(mui.CursorPointer).Shrink(0)
				b.Children(func() {
					caption(c, "Reset").FontWeight(500).TextColor(theme.RoleText2).Underline()
				})
				if b.Clicked() {
					a.saveRename(h, "")
				}
			}
		})
		mui.Spacer(c)
		mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
			if cardButton(c, ctaAccent, "Save", "Save", false).Grow(1).Clicked() {
				a.saveRename(h, a.renameText)
			}
			if cardButton(c, ctaOutline, "Cancel", "Cancel", false).Clicked() {
				a.cancelRename()
			}
		})
	})
}

// saveRename stores alias for h; empty goes back to the advertised name.
// An unchanged name closes the editor without storing anything.
func (a *App) saveRename(h HostInfo, alias string) {
	alias = strings.TrimSpace(alias)
	if alias != "" && alias == h.Name {
		a.cancelRename()
		return
	}
	if err := a.backend.RenameHost(h.ID, alias); err != nil {
		a.renameErr = err.Error()
		return
	}
	a.cancelRename()
}

func (a *App) cancelRename() {
	a.renaming, a.renameText, a.renameOpen, a.renameErr = "", "", false, ""
}

func (a *App) tailnetFooter(c *mui.Context, hosts []HostInfo) {
	ts := a.backend.Tailnet()
	offline := 0
	for _, h := range hosts {
		if !h.Online {
			offline++
		}
	}
	mui.Column(c).FillWidth().Children(func() {
		divider(c)
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Padding(theme.Space4, 0, 0).Children(func() {
			if ts.Running {
				sliceMeter(c, 3, 3, theme.RoleDirect)
				secondary(c, "Tailscale connected")
				if ts.User != "" {
					secondary(c, "as")
					mono(c, ts.User).FontSize(theme.TextCaption + 1)
				}
			} else {
				sliceMeter(c, 0, 3, theme.RoleText3)
				secondary(c, "Tailscale is not running. LAN hosts still show here.")
			}
			mui.Spacer(c)
			switch offline {
			case 0:
			case 1:
				caption(c, "1 remembered host offline")
			default:
				caption(c, fmt.Sprintf("%d remembered hosts offline", offline))
			}
		})
	})
}
