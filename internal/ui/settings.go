package ui

import (
	"fmt"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

var (
	fpsChoices = []int{0, 60, 120, 240}
	fpsLabels  = []string{"Match display", "60", "120", "240"}
	// sliceChoices: 1 encodes whole frames.
	sliceChoices = []int{1, 2, 4, 8}
	sliceLabels  = []string{"Off", "2", "4", "8"}
)

const minBitrateMbps = 5

func (a *App) settingsScreen(c *mui.Context) {
	before := a.backend.Settings()
	s := before
	a.mu.Lock()
	saveErr := a.settingsErr
	a.mu.Unlock()

	pageHeader(c, "Settings", "", nil)
	if saveErr != "" {
		mui.Row(c).FillWidth().Padding(theme.Space3, theme.Space4).Radius(theme.RadiusSm).
			Border(1, theme.RoleFault).Children(func() {
			label(c, "Could not save settings: "+saveErr).TextColor(theme.RoleFault)
		})
	}
	mui.Row(c).FillWidth().Gap(theme.Space5).AlignItems(mui.Start).Wrap().Children(func() {
		mui.Column(c).Grow(1).Basis(0).MinWidth(440).Gap(theme.Space5).Children(func() {
			videoPanel(c, &s)
			audioInputPanel(c, &s)
		})
		mui.Column(c).Grow(1).Basis(0).MinWidth(440).Gap(theme.Space5).Children(func() {
			a.encoderPanel(c, &s)
			a.networkPanel(c, &s)
		})
	})
	a.saveSettings(before, s)
}

func videoPanel(c *mui.Context, s *Settings) {
	panel(c, "Video", "", func() {
		settingRow(c, "Codec", "", func() { pickFrom(c, "Codec", codecChoices, codecLabels, &s.Codec) })
		divider(c)
		settingRow(c, "Chroma", "4:4:4 keeps text and UI sharp. NVENC and oneVPL encode it; AMF and Apple Silicon encode 4:2:0.",
			func() { pickFrom(c, "Chroma", chromaChoices, chromaLabels, &s.Chroma) })
		divider(c)
		settingRow(c, "Frame rate", "", func() { pickFrom(c, "Frame rate", fpsChoices, fpsLabels, &s.TargetFPS) })
		divider(c)
		mui.Column(c).FillWidth().Padding(theme.Space4, theme.Space5).Gap(theme.Space3).Children(func() {
			mui.Row(c).FillWidth().AlignItems(mui.Baseline).Children(func() {
				label(c, "Maximum bitrate").FontWeight(500).Grow(1)
				hudReading(c, fmt.Sprint(s.MaxBitrateMbps), "Mbps")
			})
			v := float64(s.MaxBitrateMbps)
			if mui.StepSlider(c, &v, minBitrateMbps, float64(lanCapMbps()), 1).Label("Maximum bitrate").FillWidth().Changed() {
				s.MaxBitrateMbps = int(v)
			}
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
		divider(c)
		settingRow(c, "Sub-frame slices", "Encode each frame in parts so the first part leaves sooner.",
			func() { pickFrom(c, "Sub-frame slices", sliceChoices, sliceLabels, &s.Slices) })
		divider(c)
		switchRow(c, "Send slices as they finish", "The client starts decoding before the whole frame arrives.", &s.SubFrame, true)
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

func (a *App) networkPanel(c *mui.Context, s *Settings) {
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
		switchRow(c, "Prefer LAN on the same network", "Use the local network when it reaches the host, even if Tailscale does too.", &s.PreferLAN, true)
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
		switchRow(c, "Send system keys to the host", "Windows key, Alt+Tab and Cmd shortcuts go to the stream.", &s.SystemKeysCapture, true)
		divider(c)
		switchRow(c, "Lock the mouse to the stream", "Relative motion, for games that turn the camera with the mouse.", &s.RelativeMouse, true)
	})
}
