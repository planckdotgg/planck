package ui

import (
	"fmt"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/discovery"
)

func (a *App) generalPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "General", "How this "+machineWord(a.goos)+" opens streams and what it shares with them.", nil)
	columns(c, 1, 1, func() {
		setPanel(c, "This device", nil, func() { a.entries(c, f, "general.name") })
		setPanel(c, "Stream window", nil, func() { a.entries(c, f, "general.fullscreen", "general.metrics") })
	}, func() {
		setPanel(c, "Shared clipboard", nil, func() { a.entries(c, f, "general.clipSend", "general.clipRecv") })
	})
}

// clientNameRow names this device for the hosts it pairs with. The field
// edits a draft, saved on Enter or when the focus leaves.
func (a *App) clientNameRow(c *mui.Context, f *settingsFrame) {
	f.row(c, "Device name", "What hosts list this device as when it pairs.", func() {
		if !a.set.clientNameFocused {
			a.set.clientNameDraft = a.backend.ClientName()
		}
		in := mui.TextInput(c.Key("client-name"), &a.set.clientNameDraft).Width(200).Height(theme.SizeControl).
			Placeholder(a.machineName).Label("Device name").Radius(theme.RadiusSm).Font(theme.FontMono).FontSize(12).
			Background(theme.RoleRaised).Border(1, theme.RoleControlEdge)
		focused := in.Focused()
		if in.Submitted() || a.set.clientNameFocused && !focused {
			if name := strings.TrimSpace(a.set.clientNameDraft); name != a.backend.ClientName() {
				a.setSettingsErr(a.backend.SetClientName(name))
			}
		}
		a.set.clientNameFocused = focused
	})
}

// metricsRow picks what the in-stream metrics window shows as a stream
// starts: nothing, the compact readout, or the full window.
func (a *App) metricsRow(c *mui.Context, f *settingsFrame) {
	l := &f.layout
	f.row(c, "Metrics at stream start", "The overlay remembers where you left it. "+a.keyText(HotkeyCycleHUD)+" cycles it while streaming.", func() {
		sel := 0
		switch {
		case l.Metrics && l.MetricsCompact:
			sel = 1
		case l.Metrics:
			sel = 2
		}
		c.Key("metrics-default")
		if segmented(c, "Metrics at stream start", &sel, "Off", "Compact", "Full") {
			l.Metrics, l.MetricsCompact = sel > 0, sel == 1
		}
	})
}

func (a *App) audioPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Audio", "What this "+machineWord(a.goos)+" plays from a host, and its microphone.", nil)
	columns(c, 1, 1, func() {
		setPanel(c, "Stream audio", nil, func() { a.entries(c, f, "audio.stream", "audio.volume", "audio.mute") })
	}, func() {
		setPanel(c, "Microphone", nil, func() { a.entries(c, f, "audio.mic") })
	})
}

func (a *App) volumeRow(c *mui.Context, f *settingsFrame) {
	f.row(c, "Volume", "", func() {
		v := f.s.AudioVolume
		c.Key("audio-volume")
		if mui.Slider(c, &v, 0, 1).Label("Volume").Width(200).Changed() {
			f.s.AudioVolume = v
		}
		mono(c, fmt.Sprintf("%.0f %%", v*100)).FontSize(theme.TextCaption).TextColor(theme.RoleText2).Width(44)
	})
}

func (a *App) inputPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Input & Hotkeys", "Planck keeps its chords on "+a.chordWord()+" so "+a.hostKeysWord()+" keys reach the host.", nil)
	columns(c, 568, 320, func() {
		setPanel(c, "Hotkeys", nil, func() { a.entries(c, f, "input.hotkeys") })
	}, func() {
		setPanel(c, "Input", nil, func() { a.entries(c, f, "input.syskeys", "input.mouse") })
	})
}

func (a *App) chordWord() string {
	if a.goos == "darwin" {
		return "⌃⌥"
	}
	return "Ctrl+Alt"
}

func (a *App) hostKeysWord() string {
	if a.goos == "darwin" {
		return "⌘"
	}
	return "Windows"
}

func (a *App) hotkeyTable(c *mui.Context, f *settingsFrame) {
	mui.Column(c).FillWidth().Label("Hotkeys").Children(func() {
		mui.Row(c).FillWidth().Height(28).AlignItems(mui.Center).Children(func() {
			sectionLabel(c, "Action").Grow(1)
			sectionLabel(c, "Shortcut").Width(150)
		})
		for _, h := range a.backend.Hotkeys() {
			name := hotkeyNames[h.Action]
			if f.searching && !strings.Contains(strings.ToLower(name), f.query) && !strings.Contains("hotkeys shortcut", f.query) {
				continue
			}
			divider(c)
			mui.Row(c).FillWidth().Height(32).AlignItems(mui.Center).Children(func() {
				f.title(c, name, theme.RoleText)
				mui.Spacer(c)
				label := chordLabel(a.goos, h.Chord)
				if label == "" {
					label = "Not set"
				}
				mui.Box(c).Size(150, 24).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).
					Background(theme.RoleWell).Border(1, theme.RoleBorder).Children(func() {
					mono(c, label).FontSize(theme.TextCaption).LetterSpacing(0.06 * theme.TextCaption)
				})
			})
		}
		divider(c)
		caption(c, "Changing them here comes in a later build.").Margin(theme.Space2, 0, 0)
	})
}

func (a *App) gamepadPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Gamepad", "Controllers on this device, and what a host sees them as.", nil)
	a.entries(c, f, "gamepad.status")
}

func (a *App) gamepadStatus(c *mui.Context, f *settingsFrame) {
	mui.Column(c).FillWidth().MinHeight(200).Padding(theme.Space6).AlignItems(mui.Center).Justify(mui.Center).Gap(theme.Space3).
		Background(theme.RoleSurface).Border(1, theme.RoleBorder).Radius(theme.RadiusMd).Children(func() {
		sliceMeter(c, 0, 3, theme.RoleText3)
		f.title(c, "Gamepads are not forwarded yet", theme.RoleText)
		caption(c, "Keyboard and mouse reach the host today. Controller forwarding, mapping and the virtual controller driver come in a later build.").
			TextAlign(mui.Center).MaxWidth(420)
	})
}

func (a *App) networkPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Network", "How this "+machineWord(a.goos)+" finds hosts and the route a stream takes.", nil)
	columns(c, 1, 1, func() {
		setPanel(c, "Tailscale", nil, func() { a.entries(c, f, "net.tailscale", "net.relay") })
		setPanel(c, "Route", overriddenTag(c, f.e, false, knobRoute), func() { a.entries(c, f, "net.route") })
	}, func() {
		setPanel(c, "LAN discovery", nil, func() { a.entries(c, f, "net.discovery") })
	})
}

func (a *App) tailscaleRow(c *mui.Context, f *settingsFrame) {
	ts := a.backend.Tailnet()
	f.row(c, "Tailscale", "Planck reads the local Tailscale client. Nothing else to install.", func() {
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
}

func statusPill(c *mui.Context, bars int, col mui.Color, text string) {
	mui.Row(c).Height(28).PaddingX(theme.Space3).Gap(theme.Space2).AlignItems(mui.Center).
		Radius(theme.RadiusPill).Border(1, theme.RoleBorderStrong).Background(theme.RoleRaised).Children(func() {
		sliceMeter(c, bars, 3, col)
		label(c, text).SingleLine()
	})
}

func (a *App) relayRow(c *mui.Context, f *settingsFrame) {
	f.row(c, "Relay bitrate cap", "Over a DERP relay the stream stays under this until a direct path opens.", func() {
		mui.Row(c).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			sliceMeter(c, 1, 3, theme.RoleRelay)
			hudReading(c, fmt.Sprint(derpCapMbps()), "Mbps")
		})
	})
}

func (a *App) routeRow(c *mui.Context, f *settingsFrame) {
	f.knobRow(c, knobRoute, "Route", knobRoute.desc+" For "+f.e.scope.name+"; pick a host under Video & Display.")
}

func (a *App) discoveryRow(c *mui.Context, f *settingsFrame) {
	on := f.hs.Ports.Discovery != 0
	if f.switchRow(c, "Answer LAN discovery while hosting", "Clients on this network find this "+machineWord(a.goos)+" without its address.", &on) {
		f.hs.Ports.Discovery = 0
		if on {
			f.hs.Ports.Discovery = discovery.DefaultPort
		}
	}
}

// settingEntries are every searchable setting, in each category's order.
var settingEntries = []settingEntry{
	{id: "general.name", cat: catGeneral, title: "Device name", help: "What hosts list this device as when it pairs.", aliases: "client name rename",
		build: (*App).clientNameRow},
	{id: "general.fullscreen", cat: catGeneral, title: "Open streams in full screen", aliases: "fullscreen window",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Open streams in full screen", "Each stream opens in its own window. "+a.keyText(HotkeyFullscreen)+" switches full screen while streaming.", &f.s.OpenFullscreen)
		}},
	{id: "general.metrics", cat: catGeneral, title: "Metrics at stream start", help: "The overlay remembers where you left it.", aliases: "hud overlay compact statistics",
		build: (*App).metricsRow},
	{id: "general.clipSend", cat: catGeneral, title: "Share this device’s clipboard", aliases: "clipboard copy paste send",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Share this device’s clipboard", "Text and images up to 16 MiB go to the other end. Copies a password manager marks never do.", &f.s.ClipboardSend)
		}},
	{id: "general.clipRecv", cat: catGeneral, title: "Accept the other end’s clipboard", aliases: "clipboard copy paste receive",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Accept the other end’s clipboard", "", &f.s.ClipboardReceive)
		}},

	{id: "host.name", cat: catHost, title: "Host name", help: "What other devices see this machine as.", aliases: "hostname rename",
		build: (*App).hostNameRow},
	{id: "host.display", cat: catHost, title: "Capture display", aliases: "monitor screen",
		build: (*App).captureDisplayRow},
	{id: "host.encoder", cat: catHost, title: "Encoder GPU", help: "Measured encode p50.", aliases: "gpu nvenc amf onevpl videotoolbox measure adapter",
		build: (*App).encoderRows},
	{id: "host.vddPrimary", cat: catHost, title: "Make the virtual display the primary one", aliases: "vdd virtual display primary",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Make the virtual display the primary one", "On a Windows host, games open on the session’s virtual display.", &f.hs.VDDPrimary)
		}},
	{id: "host.policy", cat: catHost, title: "Host policy", help: "What every client gets.", aliases: "policy forced codec resolution fps frame rate bitrate chroma 4:4:4 444 10-bit colour color",
		build: (*App).policyRows},
	{id: "host.ports", cat: catHost, title: "Ports", aliases: "port control media discovery udp firewall",
		build: (*App).portsRow},
	{id: "host.pinMode", cat: catHost, title: "Pairing code", help: "Rotating or fixed.", aliases: "pin code fixed rotating pair",
		build: (*App).pinModeRow},
	{id: "host.pinLAN", cat: catHost, title: "Require a code on the LAN", aliases: "pin code pair lan",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Require a code on the LAN", "A device on this network pairs only with the code.", &f.hs.RequirePINForLAN)
		}},
	{id: "host.tailnet", cat: catHost, title: "Trust my own Tailscale devices with no code", aliases: "pin code tailscale tailnet pair zero",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Trust my own Tailscale devices with no code", "Planck checks the Tailscale identity instead.", &f.hs.AllowTailscaleSameUser)
		}},
	{id: "host.paired", cat: catHost, title: "Paired devices", aliases: "revoke device fingerprint trusted clients",
		build: (*App).pairedTable},
	{id: "host.allowInput", cat: catHost, title: "Let clients control this machine", aliases: "input keyboard mouse gamepad permission",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Let clients control this machine", "Keyboard, mouse and gamepad from the device streaming.", &f.hs.AllowInput)
		}},
	{id: "host.clipboard", cat: catHost, title: "Clipboard when hosting", aliases: "clipboard copy paste direction",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.row(c, "Clipboard when hosting", "", func() {
				c.Key("host-clipboard")
				pickFrom(c, "Clipboard when hosting", clipboardValues, clipboardLabels, &f.hs.Clipboard)
			})
		}},

	{id: "video.codec", cat: catVideo, title: "Codec", aliases: "hevc av1 h264 h.264 pyrowave encoder",
		build: (*App).codecRow},
	{id: "video.chroma", cat: catVideo, title: "Chroma", aliases: "4:4:4 444 4:2:0 420 colour color",
		build: func(a *App, c *mui.Context, f *settingsFrame) { f.knobRow(c, knobChroma, "Chroma", "") }},
	{id: "video.bitDepth", cat: catVideo, title: "Bit depth", aliases: "10-bit 8-bit banding hdr colour color",
		build: func(a *App, c *mui.Context, f *settingsFrame) { f.knobRow(c, knobBitDepth, "Bit depth", "") }},
	{id: "video.resolution", cat: catVideo, title: "Resolution", aliases: "size 1080p 1440p 4k 8k custom",
		build: (*App).resolutionRow},
	{id: "video.fps", cat: catVideo, title: "Frame rate", aliases: "fps hz refresh",
		build: func(a *App, c *mui.Context, f *settingsFrame) { f.knobRow(c, knobFPS, "Frame rate", "") }},
	{id: "video.bitrate", cat: catVideo, title: "Bitrate", aliases: "mbps bandwidth quality",
		build: (*App).bitrateRow},
	{id: "video.direct", cat: catVideo, title: "Direct to display (skip the compositor)", aliases: "compositor macos metal",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.knobRow(c, knobDirect, "Direct to display (skip the compositor)", "")
		}},
	{id: "video.vsync", cat: catVideo, title: "VSync — smoother, one frame slower", aliases: "tearing",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.knobRow(c, knobVSync, "VSync — smoother, one frame slower", "")
		}},
	{id: "video.hdr", cat: catVideo, title: "HDR10 out", aliases: "hdr",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.unavailableRow(c, "HDR10 out", "Comes with HDR output in a later build.")
		}},
	{id: "video.multi", cat: catVideo, title: "Open every host display", aliases: "monitor multi-monitor displays",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.unavailableRow(c, "Open every host display", "One window per host display comes in a later build.")
		}},
	{id: "video.slices", cat: catVideo, title: "Sub-frame slices", aliases: "slice latency advanced",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.knobRow(c, knobSlices, "Sub-frame slices", "The first slice leaves the host sooner.")
		}},
	{id: "video.split", cat: catVideo, title: "NVENC two-engine split", aliases: "nvenc split advanced",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.knobRow(c, knobSplit, "NVENC two-engine split", "NVIDIA hosts with two engines, at 1440p and up.")
		}},
	{id: "video.vdd", cat: catVideo, title: "Ask the host for a virtual display", aliases: "vdd sudovda advanced",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.knobRow(c, knobVDD, "Ask the host for a virtual display", "Sized to this window, on the GPU that encodes.")
		}},
	{id: "video.vddGPU", cat: catVideo, title: "Virtual display GPU", aliases: "vdd gpu adapter advanced",
		build: (*App).gpuRow},
	{id: "video.presentation", cat: catVideo, title: "Scaling", help: "Decoder, constant frame rate.", aliases: "scaling fit fill decoder software constant",
		build: func(a *App, c *mui.Context, f *settingsFrame) { f.presentationRows(c) }},

	{id: "audio.stream", cat: catAudio, title: "Stream audio", aliases: "sound speakers",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Stream audio", "Play the host’s sound on this device.", &f.s.AudioEnabled)
		}},
	{id: "audio.volume", cat: catAudio, title: "Volume", aliases: "sound level",
		build: (*App).volumeRow},
	{id: "audio.mute", cat: catAudio, title: "Mute", aliases: "sound silence",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Mute", "Silences the stream and keeps its audio running.", &f.s.AudioMuted)
		}},
	{id: "audio.mic", cat: catAudio, title: "Microphone", aliases: "mic voice",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.row(c, "Microphone", "Every stream starts with it muted. Turn it on from the stream menu or with "+a.keyText(HotkeyMicrophone)+".", func() {})
		}},

	{id: "input.syskeys", cat: catInput, title: "Send system keys to the host", aliases: "immersive keyboard alt tab windows key",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Send system keys to the host", "Immersive mode: Alt+Tab, the Windows key and "+a.hostKeysWord()+" shortcuts go to the stream.", &f.s.SystemKeysCapture)
		}},
	{id: "input.mouse", cat: catInput, title: "Lock the mouse to the stream", aliases: "mouse cursor relative",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Lock the mouse to the stream", "Relative motion, for games that turn the camera with the mouse.", &f.s.RelativeMouse)
		}},
	{id: "input.hotkeys", cat: catInput, title: "Hotkeys", aliases: "shortcut chord keys stream menu metrics hud microphone full screen immersive cursor disconnect keyframe ctrl alt del display kick stop hosting",
		build: (*App).hotkeyTable},

	{id: "gamepad.status", cat: catGamepad, title: "Gamepads", aliases: "controller gamepad joystick xbox dualsense",
		build: (*App).gamepadStatus},

	{id: "net.tailscale", cat: catNetwork, title: "Tailscale", aliases: "tailnet vpn",
		build: (*App).tailscaleRow},
	{id: "net.relay", cat: catNetwork, title: "Relay bitrate cap", aliases: "derp relay tailscale bitrate",
		build: (*App).relayRow},
	{id: "net.route", cat: catNetwork, title: "Route", aliases: "lan tailscale path preference",
		build: (*App).routeRow},
	{id: "net.discovery", cat: catNetwork, title: "Answer LAN discovery while hosting", aliases: "lan discovery find broadcast",
		build: (*App).discoveryRow},
}
