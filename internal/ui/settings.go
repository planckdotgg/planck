package ui

import (
	"fmt"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

const minBitrateMbps = 5

func (a *App) settingsScreen(c *mui.Context) {
	before := a.backend.Settings()
	s := before
	scope := a.settingsScope()
	e := a.editOverrides(scope)
	a.mu.Lock()
	saveErr := a.settingsErr
	a.mu.Unlock()
	if a.drafts.err != "" {
		saveErr = a.drafts.err
	}

	pageHeader(c, "Settings", "", nil)
	if saveErr != "" {
		mui.Row(c).FillWidth().Padding(theme.Space3, theme.Space4).Radius(theme.RadiusSm).
			Border(1, theme.RoleFault).Children(func() {
			label(c, "Could not save settings: "+saveErr).TextColor(theme.RoleFault)
		})
	}
	a.scopeBar(c, e)
	mui.Row(c).FillWidth().Gap(theme.Space5).AlignItems(mui.Start).Wrap().Children(func() {
		mui.Column(c).Grow(1).Basis(0).MinWidth(440).Gap(theme.Space5).Children(func() {
			a.videoOverridePanel(c, e)
			a.hostOverridePanel(c, e)
		})
		mui.Column(c).Grow(1).Basis(0).MinWidth(440).Gap(theme.Space5).Children(func() {
			a.presentOverridePanel(c, e)
			a.streamWindowPanel(c, &s)
			a.hostingPanel(c, &s)
			a.encoderPanel(c, &s)
			a.networkPanel(c)
			audioInputPanel(c, &s)
		})
	})
	a.saveSettings(before, s)
	a.saveOverrides(e)
}

// settingsScope is the scope Settings edits: every host, or a paired host
// picked in the scope bar or from its card.
func (a *App) settingsScope() overrideScope {
	if a.ovScope.all() {
		return overrideScope{name: "All hosts"}
	}
	for _, h := range a.backend.Hosts() {
		if h.ID == a.ovScope.hostID {
			a.ovScope.name = h.Name
		}
	}
	return a.ovScope
}

// scopeBar picks whose stream format Settings edits, and resets it.
func (a *App) scopeBar(c *mui.Context, e *overrideEdit) {
	scopes := []overrideScope{{name: "All hosts"}}
	for _, h := range a.backend.Hosts() {
		if h.Paired || h.ID == a.ovScope.hostID {
			scopes = append(scopes, overrideScope{hostID: h.ID, name: h.Name})
		}
	}
	names := make([]string, len(scopes))
	sel := 0
	for i, sc := range scopes {
		names[i] = sc.name
		if sc.hostID == e.scope.hostID {
			sel = i
		}
	}
	mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space4).Wrap().Children(func() {
		mui.Column(c).Gap(2).Children(func() {
			label(c, "Stream format for").FontWeight(500)
			desc := "Every knob on Auto lets the host and this client choose."
			if !e.scope.all() {
				desc = "Auto here follows all hosts."
			}
			secondary(c, desc)
		})
		c.Key("override-scope")
		if segmented(c, "Stream format for", &sel, names...) {
			a.ovScope = scopes[sel]
			a.drafts.err = ""
		}
		mui.Spacer(c)
		if button(c, buttonSecondary, "Reset to Auto", false).Clicked() {
			e.resetToAuto()
			a.drafts.err = ""
		}
	})
}

// streamWindowPanel says how a stream's own window opens.
func (a *App) streamWindowPanel(c *mui.Context, s *Settings) {
	panel(c, "Stream window", "", func() {
		switchRow(c, "Open streams in full screen", "Each stream opens in its own window. "+a.shortcut(false, "F")+" switches full screen while streaming.", &s.OpenFullscreen, true)
	})
}

// hostingPanel names this machine for the clients that find it. The field
// edits a draft, so a name is saved once, on Enter or when the focus
// leaves, not at every key.
func (a *App) hostingPanel(c *mui.Context, s *Settings) {
	panel(c, "Hosting", "", func() {
		settingRow(c, "Host name", "What other devices see this "+machineWord(a.goos)+" as.", func() {
			if !a.hostNameFocused {
				a.hostNameDraft = s.HostName
			}
			in := mui.TextInput(c.Key("host-name"), &a.hostNameDraft).Width(220).Height(theme.SizeControl).
				Placeholder(a.machineName).Label("Host name").Radius(theme.RadiusSm).
				Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
			focused := in.Focused()
			if in.Submitted() || a.hostNameFocused && !focused {
				s.HostName = strings.TrimSpace(a.hostNameDraft)
			}
			a.hostNameFocused = focused
		})
	})
}

func (a *App) encoderPanel(c *mui.Context, s *Settings) {
	encoders := a.backend.Encoders()
	panel(c, "Encoder and GPU", "When this "+machineWord(a.goos)+" hosts", func() {
		encoderOption(c, s, "", "Automatic", "", "")
		measured := false
		for _, e := range encoders {
			divider(c)
			reading := ""
			if e.EncodeMs > 0 {
				reading = formatMs(e.EncodeMs) + " ms"
				measured = true
			}
			encoderOption(c, s, e.GPU, e.GPU, e.API, reading)
		}
		if measured {
			mui.Box(c).Padding(0, theme.Space5, theme.Space3).Children(func() {
				caption(c, "Encode times are p50, measured on this machine.")
			})
		}
	})
}

func encoderOption(c *mui.Context, s *Settings, value, name, detail, reading string) {
	r := mui.RadioBase(c.Key("encoder:"+value), &s.Encoder, value).FillWidth().Padding(theme.Space3, theme.Space5).
		AlignItems(mui.Center).Gap(theme.Space4).Label(name).Cursor(mui.CursorPointer)
	chosen := s.Encoder == value
	if r.Hovered() {
		r.Background(theme.RoleRaised)
	}
	r.Children(func() {
		mui.Box(c).Size(18, 18).Draw(func(p *mui.Painter, b mui.Rect) {
			if chosen {
				p.Stroke(b, theme.RoleAccent, 9, 1.5)
				p.Fill(mui.Rect{X: b.X + 5, Y: b.Y + 5, W: 8, H: 8}, theme.RoleAccent, 4)
				return
			}
			p.Stroke(b, theme.RoleControlEdge, 9, 1.5)
		})
		mui.Column(c).Grow(1).Gap(2).Children(func() {
			label(c, name).FontWeight(600)
			if detail != "" {
				secondary(c, detail)
			}
		})
		if reading != "" {
			mono(c, reading)
		}
	})
}

func (a *App) networkPanel(c *mui.Context) {
	ts := a.backend.Tailnet()
	panel(c, "Network and Tailscale", "", func() {
		settingRow(c, "Tailscale", "Planck reads the local Tailscale client. Nothing else to install.", func() {
			if ts.Running {
				msg := "Connected"
				if ts.User != "" {
					msg += " as " + ts.User
				}
				statusPill(c, 3, theme.RoleDirect, msg)
			} else {
				statusPill(c, 0, theme.RoleText3, "Not running")
			}
		})
		divider(c)
		settingRow(c, "Relay bitrate cap", "Over a DERP relay the stream stays under this until a direct path opens.", func() {
			mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
				sliceMeter(c, 1, 3, theme.RoleRelay)
				hudReading(c, fmt.Sprint(derpCapMbps()), "Mbps")
			})
		})
	})
}

func statusPill(c *mui.Context, bars int, col mui.Color, text string) {
	mui.Row(c).Height(28).PaddingX(theme.Space3).Gap(theme.Space2).AlignItems(mui.Center).
		Radius(theme.RadiusPill).Border(1, theme.RoleBorderStrong).Background(theme.RoleRaised).Children(func() {
		sliceMeter(c, bars, 3, col)
		label(c, text).SingleLine()
	})
}

func audioInputPanel(c *mui.Context, s *Settings) {
	panel(c, "Audio and input", "", func() {
		switchRow(c, "Stream audio", "", &s.AudioEnabled, true)
		divider(c)
		switchRow(c, "Share this device's clipboard", "Text and images up to 16 MiB go to the other end of a stream. Copies a password manager marks never do.", &s.ClipboardSend, true)
		divider(c)
		switchRow(c, "Accept the other end's clipboard", "", &s.ClipboardReceive, true)
		divider(c)
		switchRow(c, "Send system keys to the host", "Windows key, Alt+Tab and Cmd shortcuts go to the stream.", &s.SystemKeysCapture, true)
		divider(c)
		switchRow(c, "Lock the mouse to the stream", "Relative motion, for games that turn the camera with the mouse.", &s.RelativeMouse, true)
	})
}
