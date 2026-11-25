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
	{id: "host.stayAwake", cat: catHost, title: "Stay awake while hosting", aliases: "sleep power display awake caffeinate session",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Stay awake while hosting", "", &f.hs.StayAwake)
		}},
	{id: "host.lock", cat: catHost, title: "Lock this machine when the session ends", aliases: "lock screen mac pc computer session end disconnect privacy",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Lock this "+machineWord(a.goos)+" when the session ends", "", &f.hs.LockOnDisconnect)
		}},
	{id: "host.mute", cat: catHost, title: "Mute this computer’s speakers while streaming", aliases: "mute speakers sound audio silence",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Mute this computer’s speakers while streaming", "The stream still carries the sound.", &f.hs.MuteSpeakers)
		}},
	{id: "host.privacy", cat: catHost, title: "Privacy mode — blank the physical displays", aliases: "privacy blank displays screen off virtual display vdd",
		build: (*App).privacyRow},
	{id: "host.idle", cat: catHost, title: "Disconnect idle clients after", aliases: "idle timeout auto-disconnect minutes inactive afk",
		build: (*App).idleRow},
	{id: "host.kick", cat: catHost, title: "Connected client", help: "Kick the device streaming now.", aliases: "kick disconnect client boot",
		build: (*App).kickRow},
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
	{id: "video.hdr", cat: catVideo, title: "HDR10 output", aliases: "hdr hdr10 pq bt.2020 2020 edr",
		build: (*App).hdrRow},
	{id: "video.multi", cat: catVideo, title: "Open all host displays", aliases: "monitor multi-monitor displays every windows",
		build: (*App).multiDisplayRows},
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
	{id: "audio.output", cat: catAudio, title: "Output device", aliases: "speakers headphones playback device sound",
		build: (*App).outputDeviceRow},
	{id: "audio.micDevice", cat: catAudio, title: "Microphone device", aliases: "mic input device headset",
		build: (*App).micDeviceRow},
	{id: "audio.hostCapture", cat: catAudio, title: "Capture device", help: "The sound this machine streams while hosting.", aliases: "host loopback wasapi screencapturekit system mix",
		build: (*App).hostCaptureRow},
	{id: "audio.virtualMic", cat: catAudio, title: "Virtual mic sink", help: "Where a client’s microphone plays while hosting.", aliases: "microphone host virtual cable vb-audio sink",
		build: (*App).virtualMicRow},
	{id: "audio.exclude", cat: catAudio, title: "Leave out apps", help: "Apps kept out of the sound while hosting.", aliases: "exclude app discord teams slack process mute host",
		build: (*App).excludeAppsRow},
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
	{id: "input.swap", cat: catInput, title: "Swap ⌘ and Ctrl", aliases: "cmd command ctrl control swap mac windows modifier",
		build: (*App).swapRow},
	{id: "input.neverSend", cat: catInput, title: "Never send these keys", help: "Kept on this device even in immersive mode.", aliases: "blocklist pass-through passthrough push-to-talk local block chord keys",
		build: (*App).neverSendList},
	{id: "input.rules", cat: catInput, title: "What the capture field accepts", aliases: "chord rules modifier reserved conflict record",
		build: (*App).captureRules},
	{id: "input.hotkeys", cat: catInput, title: "Hotkeys", aliases: "shortcut chord keys stream menu hide overlay button metrics hud microphone full screen immersive cursor disconnect keyframe ctrl alt del display switch kick stop hosting reset",
		build: (*App).hotkeyTable},

	{id: "gamepad.controllers", cat: catGamepad, title: "Connected controllers", aliases: "controller gamepad joystick xbox dualsense dualshock bluetooth usb battery",
		build: (*App).controllerList},
	{id: "gamepad.preview", cat: catGamepad, title: "Live state", help: "Buttons, sticks and triggers as a stream sends them.", aliases: "preview test buttons sticks triggers test rumble vibration",
		build: (*App).livePreview},
	{id: "gamepad.mapping", cat: catGamepad, title: "Button mapping", aliases: "map unmap remap reset default sdl gamecontrollerdb copy paste mapping string",
		build: (*App).mappingRows},
	{id: "gamepad.leftDeadzone", cat: catGamepad, title: "Left stick deadzone", aliases: "deadzone dead zone drift stick calibration",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.percentRow(c, "Left stick deadzone", "Radial: the stick still reaches full tilt.", &f.gp.LeftDeadzone, gamepadMaxDeadzone)
		}},
	{id: "gamepad.rightDeadzone", cat: catGamepad, title: "Right stick deadzone", aliases: "deadzone dead zone drift stick calibration",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.percentRow(c, "Right stick deadzone", "", &f.gp.RightDeadzone, gamepadMaxDeadzone)
		}},
	{id: "gamepad.trigger", cat: catGamepad, title: "Trigger threshold", aliases: "trigger deadzone lt rt calibration",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.percentRow(c, "Trigger threshold", "Travel ignored before a trigger counts.", &f.gp.TriggerThreshold, gamepadMaxTrigger)
		}},
	{id: "gamepad.invertLeft", cat: catGamepad, title: "Invert left stick Y", aliases: "invert y axis flip look",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Invert left stick Y", "", &f.gp.InvertLeftY)
		}},
	{id: "gamepad.invertRight", cat: catGamepad, title: "Invert right stick Y", aliases: "invert y axis flip look camera",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Invert right stick Y", "", &f.gp.InvertRightY)
		}},
	{id: "gamepad.rumble", cat: catGamepad, title: "Rumble", aliases: "vibration force feedback haptics",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Rumble", "Play a game’s rumble on this device’s controllers.", &f.gp.RumbleEnabled)
		}},
	{id: "gamepad.rumbleStrength", cat: catGamepad, title: "Rumble strength", aliases: "vibration intensity force feedback",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			if f.gp.RumbleEnabled {
				f.percentRow(c, "Rumble strength", "", &f.gp.RumbleStrength, gamepadMaxRumble)
			}
		}},
	{id: "gamepad.allowGuests", cat: catGamepad, title: "Allow guest gamepads", aliases: "host guest client controller permission",
		build: func(a *App, c *mui.Context, f *settingsFrame) {
			f.switchRow(c, "Allow guest gamepads", "Controllers of the device streaming from this "+machineWord(a.goos)+" plug in as virtual ones.", &f.gp.AllowGuestGamepads)
		}},
	{id: "gamepad.virtualType", cat: catGamepad, title: "Virtual controller type", aliases: "xbox 360 dualshock ds4 dualsense ps4 ps5 emulate virtual controller",
		build: (*App).virtualTypeRow},
	{id: "gamepad.driver", cat: catGamepad, title: "Virtual gamepad driver", aliases: "vigembus vigem driver install driverkit",
		build: (*App).virtualPadStatusLine},
	{id: "gamepad.slots", cat: catGamepad, title: "Controller slots", aliases: "slot player order reorder reset move",
		build: (*App).slotRows},

	{id: "net.tailscale", cat: catNetwork, title: "Tailscale", aliases: "tailnet vpn",
		build: (*App).tailscaleRow},
	{id: "net.relay", cat: catNetwork, title: "Relay bitrate cap", aliases: "derp relay tailscale bitrate",
		build: (*App).relayRow},
	{id: "net.route", cat: catNetwork, title: "Route", aliases: "lan tailscale path preference",
		build: (*App).routeRow},
	{id: "net.discovery", cat: catNetwork, title: "Answer LAN discovery while hosting", aliases: "lan discovery find broadcast",
		build: (*App).discoveryRow},
}
