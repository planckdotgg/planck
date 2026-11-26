package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
)

func (a *App) hostPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Host", "This "+machineWord(a.goos)+", when other devices connect to it.", func() {
		a.hostingCapsule(c, f)
	})
	columns(c, 1, 1, func() {
		setPanel(c, "This machine", nil, func() { a.entries(c, f, "host.name", "host.display", "host.encoder", "host.vddPrimary") })
		setPanel(c, "Host policy", func() { caption(c, "beats every client’s choice") }, func() { a.entries(c, f, "host.policy") })
		a.entries(c, f, "host.ports")
	}, func() {
		setPanel(c, "Session & privacy", nil, func() {
			a.entries(c, f, "host.stayAwake", "host.lock", "host.mute", "host.privacy", "host.idle")
		})
		setPanel(c, "Security & pairing", nil, func() {
			a.entries(c, f, "host.pinMode", "host.pinLAN", "host.tailnet", "host.paired")
		})
		setPanel(c, "Clients", nil, func() { a.entries(c, f, "host.kick", "host.allowInput", "host.clipboard") })
	})
}

// hostingCapsule is the Hosting switch, the same state as the rail's foot.
func (a *App) hostingCapsule(c *mui.Context, f *settingsFrame) {
	on := f.hosting.On
	state, busy := a.hostingState(f.hosting)
	r := mui.SwitchBase(c.Key("hosting-capsule"), &on).Height(theme.SizeControlLg).PaddingX(theme.Space4).Gap(theme.Space3).
		AlignItems(mui.Center).Radius(theme.RadiusMd).Background(theme.RoleRaised).
		Border(1, theme.RoleBorder.Mix(theme.RoleAccent, 0.40)).Label("Hosting").Description("Hosting, " + state).
		Cursor(mui.CursorPointer)
	r.Children(func() {
		label(c, "Hosting").FontWeight(600)
		settingsSwitch(c, f.hosting.On, false)
	})
	if r.Changed() && !busy {
		a.setHosting(on)
	}
}

func (a *App) hostNameRow(c *mui.Context, f *settingsFrame) {
	f.laneRow(c, "Host name", 124, func() {
		if !a.hostNameFocused {
			a.hostNameDraft = f.s.HostName
		}
		in := mui.TextInput(c.Key("host-name"), &a.hostNameDraft).Grow(1).Height(theme.SizeControl).
			Placeholder(a.machineName).Label("Host name").Radius(theme.RadiusSm).Font(theme.FontMono).FontSize(12).
			Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
		focused := in.Focused()
		if in.Submitted() || a.hostNameFocused && !focused {
			f.s.HostName = strings.TrimSpace(a.hostNameDraft)
		}
		a.hostNameFocused = focused
	})
}

// displayName is how the capture picker names display id: its number,
// size and rate. "" is the primary display.
func displayName(ds []DisplayInfo, id string) (string, bool) {
	for i, d := range ds {
		if d.ID == id || id == "" && d.Primary {
			return displayLabel(i, d), true
		}
	}
	return "", false
}

func displayLabel(i int, d DisplayInfo) string {
	s := fmt.Sprintf("Display %d · %d×%d", i+1, d.Width, d.Height)
	if d.RefreshHz > 0 {
		s += fmt.Sprintf(" @ %d", d.RefreshHz)
	}
	return s
}

func (a *App) captureDisplayRow(c *mui.Context, f *settingsFrame) {
	ds := a.backend.Displays()
	values, labels := []string{""}, []string{"Primary display"}
	for i, d := range ds {
		values = append(values, d.ID)
		labels = append(labels, displayLabel(i, d))
	}
	f.laneRow(c, "Capture display", 124, func() {
		dropdown(c, "Capture display", &f.hs.Display, values, labels, 0)
	})
}

// measuring reports whether Measure again is under way.
func (a *App) measuring() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.measureBusy
}

// measureEncoders times every encoder, one after another, off the main
// thread; Encoders reports the readings.
func (a *App) measureEncoders(gpus []string) {
	a.mu.Lock()
	if a.measureBusy {
		a.mu.Unlock()
		return
	}
	a.measureBusy, a.measureErr = true, ""
	a.mu.Unlock()
	go func() {
		msg := ""
		for _, g := range gpus {
			if _, err := a.backend.MeasureEncoder(context.Background(), g); err != nil {
				msg = err.Error()
			}
		}
		a.mu.Lock()
		a.measureBusy, a.measureErr = false, msg
		a.mu.Unlock()
		a.Notify()
	}()
}

func (a *App) encoderRows(c *mui.Context, f *settingsFrame) {
	encoders := a.backend.Encoders()
	mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
		mui.Row(c).FillWidth().MinHeight(28).AlignItems(mui.Center).Children(func() {
			f.title(c, "Encoder GPU", theme.RoleText)
			mui.Spacer(c)
			switch {
			case a.measuring():
				caption(c, "Measuring…")
			case len(encoders) > 0:
				if textLink(c, "Measure again", theme.RoleText2) {
					gpus := make([]string, len(encoders))
					for i, e := range encoders {
						gpus[i] = e.GPU
					}
					a.measureEncoders(gpus)
				}
			}
		})
		mui.Column(c).FillWidth().Radius(theme.RadiusSm).Border(1, theme.RoleBorder).Clip().Children(func() {
			encoderOption(c, &f.s, "", "Automatic", "the host picks", "")
			for _, e := range encoders {
				divider(c)
				reading := ""
				if e.EncodeMs > 0 {
					reading = formatMs(e.EncodeMs) + " ms"
				}
				encoderOption(c, &f.s, e.GPU, e.GPU, e.API, reading)
			}
		})
		a.mu.Lock()
		merr := a.measureErr
		a.mu.Unlock()
		if merr != "" {
			warningLine(c, merr)
		}
		note := "Planck encodes on the GPU that drew the frame; measured here, same-GPU encode beats sending frames across GPUs."
		if len(encoders) == 1 {
			note = "Planck encodes on the GPU that drew the frame. This " + machineWord(a.goos) + " has one."
		}
		caption(c, note)
	})
}

func encoderOption(c *mui.Context, s *Settings, value, name, detail, reading string) {
	r := mui.RadioBase(c.Key("encoder:"+value), &s.Encoder, value).FillWidth().Height(theme.SizeControlLg).PaddingX(theme.Space3).
		AlignItems(mui.Center).Gap(theme.Space3).Label(name).Cursor(mui.CursorPointer)
	chosen := s.Encoder == value
	if r.Hovered() {
		r.Background(theme.RoleRaised)
	}
	r.Children(func() {
		mui.Box(c).Size(16, 16).Shrink(0).Draw(func(p *mui.Painter, b mui.Rect) {
			if chosen {
				p.Stroke(b, theme.RoleAccent, 8, 1.5)
				p.Fill(mui.Rect{X: b.X + 4, Y: b.Y + 4, W: 8, H: 8}, theme.RoleAccent, 4)
				return
			}
			p.Stroke(b, theme.RoleControlEdge, 8, 1.5)
		})
		label(c, name).FontWeight(500).SingleLine().Shrink(1)
		if detail != "" {
			mono(c, detail).FontSize(theme.TextCaption).TextColor(theme.RoleText3).Shrink(1)
		}
		mui.Spacer(c)
		if reading != "" {
			mui.Row(c).Height(22).PaddingX(theme.Space2).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusXs).
				Background(theme.RoleWell).Children(func() {
				mono(c, "encode p50").FontSize(theme.TextCaption).TextColor(theme.RoleText2)
				mono(c, reading).FontSize(theme.TextCaption)
			})
		}
	})
}

// policyCodecs are the codecs a host may force.
var (
	policyCodecValues = []defaults.CodecPref{"auto", "hevc", "av1", "h264", "pyrowave"}
	policyCodecLabels = []string{"Auto", "HEVC", "AV1", "H.264", "PyroWave"}
)

// policyRows edit what this host forces on every client.
func (a *App) policyRows(c *mui.Context, f *settingsFrame) {
	pol := &f.hs.Policy
	field := func(name string, grow float32, fn func()) {
		mui.Column(c).Grow(grow).Basis(0).MinWidth(0).Gap(4).Children(func() {
			caption(c, name).FontWeight(500).TextColor(theme.RoleText2)
			fn()
		})
	}
	mui.Column(c).FillWidth().Gap(theme.Space3).Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space2).Children(func() {
			field("Codec", 1, func() {
				dropdown(c, "Policy codec", &pol.Codec, policyCodecValues, policyCodecLabels, 0)
			})
			field("Resolution", 1.4, func() {
				vals := knobResolution.values[:4]
				dropdown(c, "Policy resolution", (*string)(&pol.Resolution), vals, resolutionLabels[:4], 0)
			})
			field("Frame rate", 0.8, func() {
				dropdown(c, "Policy frame rate", (*string)(&pol.FPS), knobFPS.values, knobFPS.labels, 0)
			})
			field("Bitrate", 1, func() {
				v := ""
				if pol.BitrateMbps > 0 {
					v = strconv.Itoa(pol.BitrateMbps)
				}
				mui.Row(c).FillWidth().Gap(4).AlignItems(mui.Center).Children(func() {
					if s, ok := a.drafts.field(c, "policy:bitrate", "Policy bitrate", v, "Auto", 0); ok {
						pol.BitrateMbps = a.drafts.bitrate(s, pol.BitrateMbps)
					}
					mono(c, "Mbps").FontSize(theme.TextCaption).TextColor(theme.RoleText3).Shrink(0)
				})
			})
		})
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			caption(c, "Allowed colour").FontSize(12).TextColor(theme.RoleText).Width(100)
			chipFixed(c, "4:2:0 8-bit")
			if chip(c, "Allow 10-bit", "10-bit", pol.BitDepth != "8", 26) {
				pol.BitDepth = flip[defaults.BitDepthPref](pol.BitDepth == "8", defaults.Auto, "8")
			}
			if chip(c, "Allow 4:4:4", "4:4:4", pol.Chroma != "420", 26) {
				pol.Chroma = flip[defaults.ChromaPref](pol.Chroma == "420", defaults.Auto, "420")
			}
		})
		caption(c, "Anything not on Auto is what every client gets, shown there as Set by host.")
	})
}

// flip returns on when cond, else off, in the type of a preference.
func flip[T ~string](cond bool, on, off string) T {
	if cond {
		return T(on)
	}
	return T(off)
}

// chipFixed is a checked chip that cannot be turned off.
func chipFixed(c *mui.Context, text string) {
	mui.Row(c).Height(26).PaddingX(10).Gap(6).AlignItems(mui.Center).Radius(theme.RadiusXs).Background(theme.RoleHover).
		Border(1, theme.RoleBorderStrong).Label(text + ", always allowed").Children(func() {
		checkMark(c, 11, theme.RoleText)
		mono(c, text).FontSize(theme.TextCaption)
	})
}

// bitrate parses a policy bitrate draft: empty or 0 is Auto, else 5 to
// 1000 Mbps, or old with the reason kept in err.
func (d *drafts) bitrate(s string, old int) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || strings.EqualFold(s, "auto") {
		d.err = ""
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minBitrateMbps || n > maxManualBitrateMbps {
		d.err = fmt.Sprintf("%q is not a bitrate from %d to %d Mbps", s, minBitrateMbps, maxManualBitrateMbps)
		return old
	}
	d.err = ""
	return n
}

// portText reads a port setting: its number, or what 0 means.
func portText(port int, zero string) string {
	if port == 0 {
		return zero
	}
	return strconv.Itoa(port)
}

// portsRow is the ports' disclosure: what they are set to, folded, and
// their fields with what hosting holds now and what is wrong, open.
func (a *App) portsRow(c *mui.Context, f *settingsFrame) {
	p := &f.hs.Ports
	open := a.set.portsOpen || f.searching
	if open {
		if a.set.portsTested != *p || a.set.portsShownAt == 0 || a.set.portsShownAt != a.set.frame-1 {
			a.set.ports, a.set.portsTested = a.backend.TestPorts(*p), *p
		}
		a.set.portsShownAt = a.set.frame
	}
	bad := open && (a.set.ports != PortStatus{})
	mui.Column(c).FillWidth().Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).
		Padding(theme.Space3, theme.Space4).Gap(theme.Space3).Children(func() {
		head := mui.ButtonBase(c).FillWidth().MinHeight(22).Gap(theme.Space3).AlignItems(mui.Center).Label("Ports").
			Checked(open).Cursor(mui.CursorPointer)
		head.Children(func() {
			rot := float32(0)
			if open {
				rot = 90
			}
			mui.Icon(c, theme.IconChevronRight).Size(12, 12).TextColor(theme.RoleText3).Rotate(rot)
			f.title(c, "Ports", theme.RoleText)
			mui.Spacer(c)
			if bad {
				mui.Icon(c, theme.IconWarning).Size(12, 12).TextColor(theme.RoleFault)
			}
			mono(c, fmt.Sprintf("control %s · media %s · discovery %s", portText(p.Control, "auto"), portText(p.Media, "auto"),
				portText(p.Discovery, "off"))).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
		})
		if head.Clicked() && !f.searching {
			a.set.portsOpen = !a.set.portsOpen
		}
		if !open {
			return
		}
		using := f.hosting.Using
		mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Start).Children(func() {
			a.portField(c, "Control port", "port:control", &p.Control, using.Control, f.hosting.On, "0 = pick one")
			a.portField(c, "Media port", "port:media", &p.Media, using.Media, f.hosting.On, "0 = pick one")
			a.portField(c, "Discovery port", "port:discovery", &p.Discovery, using.Discovery, f.hosting.On, "0 = off")
		})
		for _, w := range []struct{ name, why string }{
			{"Control", a.set.ports.Control}, {"Media", a.set.ports.Media}, {"Discovery", a.set.ports.Discovery},
		} {
			if w.why != "" {
				warningLine(c, w.name+" port: "+w.why)
			}
		}
	})
}

func (a *App) portField(c *mui.Context, name, key string, port *int, using int, hosting bool, zero string) {
	mui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(4).Children(func() {
		caption(c, name).FontWeight(500).TextColor(theme.RoleText2)
		if v, ok := a.drafts.field(c, key, name, strconv.Itoa(*port), "0", 0); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > 65535 {
				a.drafts.err = fmt.Sprintf("%q is not a port from 0 to 65535", v)
			} else {
				a.drafts.err, *port = "", n
			}
		}
		hint := zero
		if hosting {
			hint = "Currently using " + portText(using, "none")
		}
		mono(c, hint).FontSize(theme.TextMini).TextColor(theme.RoleText3)
	})
}

// pinModeRow picks a rotating code or a fixed one, and shows the code.
func (a *App) pinModeRow(c *mui.Context, f *settingsFrame) {
	hs := &f.hs
	fixed := hs.FixedPIN != "" || a.set.wantFixed
	f.row(c, "Pairing code", "", func() {
		mode := 0
		if fixed {
			mode = 1
		}
		c.Key("pin-mode")
		if segmented(c, "Pairing code", &mode, "Rotating", "Fixed") {
			a.set.wantFixed = mode == 1
			if mode == 0 {
				hs.FixedPIN = ""
			}
		}
		code := hs.FixedPIN
		if !fixed && f.hosting.On {
			code = f.hosting.PIN
		}
		if code != "" {
			mui.Box(c).Height(theme.SizeControl).PaddingX(theme.Space3).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusSm).
				Background(theme.RoleWell).Border(1, theme.RoleBorder).Label("Current code").Children(func() {
				mono(c, spacedPIN(code)).FontSize(theme.TextBody).LetterSpacing(0.08 * theme.TextBody)
			})
		}
	})
	if fixed {
		f.row(c, "Fixed code", "Six digits clients type to pair. It stays until you change it.", func() {
			if v, ok := a.drafts.field(c, "fixed-pin", "Fixed code", hs.FixedPIN, "000000", 110); ok {
				if p := cleanPIN(v); len(p) == pinLength && p == strings.TrimSpace(v) {
					hs.FixedPIN, a.drafts.err = p, ""
				} else {
					a.drafts.err = "a fixed code is six digits"
				}
			}
		})
	} else if !f.hosting.On {
		caption(c, "A new code shows here while this "+machineWord(a.goos)+" hosts, and changes after each pairing.")
	}
}

// spacedPIN reads a six-digit code as "418 207".
func spacedPIN(p string) string {
	if len(p) != pinLength {
		return p
	}
	return p[:3] + " " + p[3:]
}

// ago reads how long ago t was: "2 h ago", "yesterday".
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

// pairedTable lists the devices this machine trusts, each with Revoke,
// which asks first.
func (a *App) pairedTable(c *mui.Context, f *settingsFrame) {
	list := a.backend.PairedClients()
	now := c.Now()
	mui.Column(c).FillWidth().Gap(0).Label("Paired devices").Children(func() {
		mui.Row(c).FillWidth().Height(28).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
			sectionLabel(c, "Paired device").Width(118)
			sectionLabel(c, "Fingerprint").Width(104)
			sectionLabel(c, "Last seen").Grow(1)
		})
		if len(list) == 0 {
			divider(c)
			caption(c, "No device has paired with this "+machineWord(a.goos)+" yet.").Margin(theme.Space2, 0, 0)
			return
		}
		for _, p := range list {
			divider(c)
			a.pairedRow(c, p, now)
		}
		divider(c)
		mui.Row(c).FillWidth().MinHeight(32).AlignItems(mui.Center).Gap(6).Children(func() {
			if a.set.revoking == "*" {
				caption(c, "Revoke every device? Each pairs again with the code.").TextColor(theme.RoleText2).Grow(1).Shrink(1)
				if cardButton(c, ctaDisconnect, "Revoke all", "Confirm revoke all", false).Height(24).Clicked() {
					a.revoke("*")
				}
				if cardButton(c, ctaMuted, "Cancel", "Keep every device", false).Height(24).Clicked() {
					a.set.revoking = ""
				}
				return
			}
			mui.Icon(c, theme.IconShieldCheck).Size(12, 12).TextColor(theme.RoleDirect)
			caption(c, "marks a device trusted through your tailnet.").Grow(1).Shrink(1)
			if textLink(c, "Revoke all", theme.RoleFault) {
				a.set.revoking, a.set.revokeErr = "*", ""
			}
		})
		if a.set.revokeErr != "" {
			warningLine(c, a.set.revokeErr)
		}
	})
}

func (a *App) pairedRow(c *mui.Context, p PairedClient, now time.Time) {
	mui.Row(c).FillWidth().Height(34).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
		mui.Row(c).Width(118).Gap(6).AlignItems(mui.Center).Children(func() {
			label(c, p.Name).SingleLine().Shrink(1)
			if p.TrustedViaTailnet {
				mui.Icon(c, theme.IconShieldCheck).Size(12, 12).TextColor(theme.RoleDirect).Label("trusted through your tailnet")
			}
		})
		mono(c, shortFingerprint(p.Fingerprint)).FontSize(12).TextColor(theme.RoleText2).Width(104)
		seen := ago(p.LastSeen, now)
		if p.Connected {
			seen = "streaming now"
		}
		mono(c, seen).FontSize(12).TextColor(theme.RoleText2).Grow(1).Shrink(1)
		if a.set.revoking == p.Fingerprint {
			if cardButton(c, ctaDisconnect, "Revoke", "Confirm revoke "+p.Name, false).Height(24).Clicked() {
				a.revoke(p.Fingerprint)
			}
			if cardButton(c, ctaMuted, "Keep", "Keep "+p.Name, false).Height(24).Clicked() {
				a.set.revoking = ""
			}
			return
		}
		b := mui.ButtonBase(c).Size(60, 24).Radius(theme.RadiusXs).AlignItems(mui.Center).Justify(mui.Center).
			Label("Revoke " + p.Name).Cursor(mui.CursorPointer)
		col := theme.RoleText2
		edge := theme.RoleBorder
		if b.Hovered() {
			col, edge = theme.RoleFault, theme.RoleFault
		}
		b.Border(1, edge).Children(func() { label(c, "Revoke").FontSize(theme.TextCaption).TextColor(col) })
		if b.Clicked() {
			a.set.revoking, a.set.revokeErr = p.Fingerprint, ""
		}
	})
}

// revoke forgets fp, or every device for "*".
func (a *App) revoke(fp string) {
	var err error
	if fp == "*" {
		err = a.backend.RevokeAllClients()
	} else {
		err = a.backend.RevokeClient(fp)
	}
	a.set.revoking, a.set.revokeErr = "", ""
	if err != nil {
		a.set.revokeErr = err.Error()
	}
}

// clipboardDirections are what this machine may share while hosting.
var (
	clipboardValues = []ClipboardDirection{ClipboardOff, ClipboardBoth, ClipboardHostToClient, ClipboardClientToHost}
	clipboardLabels = []string{"Off", "Both ways", "To clients", "From clients"}
)

// privacyRow is privacy mode's switch, or why this machine cannot blank
// its displays. One left on where it cannot work stays switchable off.
func (a *App) privacyRow(c *mui.Context, f *settingsFrame) {
	const title = "Privacy mode — blank the physical displays"
	problem := a.backend.PrivacyModeProblem()
	if problem != "" && !f.hs.PrivacyMode {
		f.unavailableRow(c, title, problem)
		return
	}
	help := "The displays stay off while a client streams from a virtual display. Applies from the next stream."
	if problem != "" {
		help = problem
	}
	f.switchRow(c, title, help, &f.hs.PrivacyMode)
}

// idleChoices are the idle disconnects on offer, in minutes; 0 is never.
var (
	idleValues = []int{0, 15, 30, 60}
	idleLabels = []string{"Off", "15 min", "30 min", "60 min"}
)

func (a *App) idleRow(c *mui.Context, f *settingsFrame) {
	values, labels := idleValues, idleLabels
	if m := f.hs.IdleTimeoutMinutes; !slices.Contains(values, m) {
		values, labels = append(slices.Clone(values), m), append(slices.Clone(labels), strconv.Itoa(m)+" min")
	}
	f.row(c, "Disconnect idle clients after", "A client that sends no input for this long is disconnected.", func() {
		dropdown(c, "Disconnect idle clients after", &f.hs.IdleTimeoutMinutes, values, labels, 110)
	})
}

// kickRow names the device streaming from this machine, with Kick, which
// disconnects it; it may connect again.
func (a *App) kickRow(c *mui.Context, f *settingsFrame) {
	client := f.hosting.Client
	help := "No device is streaming from this " + machineWord(a.goos) + "."
	if client != "" {
		help = client + " is streaming now. It may connect again."
	}
	f.row(c, "Connected client", help, func() {
		if client == "" {
			return
		}
		if cardButton(c, ctaDisconnect, "Kick", "Kick "+client, false).Height(28).Clicked() {
			a.setSettingsErr(a.backend.KickClient())
		}
	})
}
