package ui

import (
	"fmt"
	"slices"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

func (a *App) audioPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Audio", "What this "+machineWord(a.goos)+" plays from a host, its microphone, and the sound it sends while hosting.", nil)
	columns(c, 1, 1, func() {
		setPanel(c, "Stream audio", nil, func() { a.entries(c, f, "audio.stream", "audio.volume", "audio.mute", "audio.output") })
		setPanel(c, "Microphone", nil, func() { a.entries(c, f, "audio.mic", "audio.micDevice") })
	}, func() {
		setPanel(c, "Audio when hosting", nil, func() { a.entries(c, f, "audio.hostCapture", "audio.virtualMic", "audio.exclude") })
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

// deviceChoices are a device picker's values and labels: first for "",
// then each device, then cur when no device has it any more.
func deviceChoices(devs []AudioDevice, cur, first, gone string) (values, labels []string) {
	values, labels = []string{""}, []string{first}
	for _, d := range devs {
		values = append(values, d.ID)
		labels = append(labels, d.Name)
	}
	if cur != "" && !slices.Contains(values, cur) {
		values = append(values, cur)
		labels = append(labels, gone)
	}
	return values, labels
}

func (a *App) outputDeviceRow(c *mui.Context, f *settingsFrame) {
	values, labels := deviceChoices(a.backend.AudioDevices().Outputs, f.s.OutputDevice, "System default", "Unplugged, plays on the default")
	f.laneRow(c, "Output device", 124, func() {
		dropdown(c, "Output device", &f.s.OutputDevice, values, labels, 0)
	})
	caption(c, "A change applies from the next stream.")
}

func (a *App) micDeviceRow(c *mui.Context, f *settingsFrame) {
	values, labels := deviceChoices(a.backend.AudioDevices().Inputs, f.s.MicDevice, "System default", "Unplugged, records from the default")
	f.laneRow(c, "Microphone device", 124, func() {
		dropdown(c, "Microphone device", &f.s.MicDevice, values, labels, 0)
	})
}

// hostCaptureRow is the sound a host streams: on macOS, ScreenCaptureKit's
// mix of everything but Planck; elsewhere, one output device or all of
// them.
func (a *App) hostCaptureRow(c *mui.Context, f *settingsFrame) {
	if a.goos == "darwin" {
		f.laneRow(c, "Capture device", 124, func() {
			mui.Row(c).Grow(1).Height(theme.SizeControl).PaddingX(10).AlignItems(mui.Center).Radius(theme.RadiusSm).
				Background(theme.RoleWell).Border(1, theme.RoleBorder).Label("Capture device: System mix").Children(func() {
				mono(c, "System mix · ScreenCaptureKit").FontSize(12).TextColor(theme.RoleText2)
			})
		})
		return
	}
	values, labels := deviceChoices(a.backend.AudioDevices().Outputs, f.hs.AudioDevice, "Everything this PC plays", "Unplugged, streams everything")
	f.laneRow(c, "Capture device", 124, func() {
		dropdown(c, "Capture device", &f.hs.AudioDevice, values, labels, 0)
	})
}

// virtualMicRow picks where a client's microphone plays on this machine.
func (a *App) virtualMicRow(c *mui.Context, f *settingsFrame) {
	values, labels := []string{""}, []string{"Auto (find a virtual mic)"}
	for _, d := range a.backend.AudioDevices().Outputs {
		values = append(values, d.ID)
		labels = append(labels, d.Name)
	}
	values, labels = append(values, VirtualMicOff), append(labels, "Off")
	if cur := f.hs.VirtualMic; !slices.Contains(values, cur) {
		values, labels = append(values, cur), append(labels, cur)
	}
	f.laneRow(c, "Virtual mic sink", 124, func() {
		dropdown(c, "Virtual mic sink", &f.hs.VirtualMic, values, labels, 0)
	})
}

// quickApps are the apps people most often keep out of a stream's sound.
var quickApps = []string{"Discord", "Microsoft Teams", "Slack"}

// excludeAppsRow is the apps left out of the sound a host streams: a chip
// each with its remove, and + App, which opens a field and quick picks.
func (a *App) excludeAppsRow(c *mui.Context, f *settingsFrame) {
	names := f.hs.ExcludeApps.Names()
	add := func(n string) {
		f.hs.ExcludeApps = NewAppList(append(slices.Clone(names), n)...)
		names = f.hs.ExcludeApps.Names()
	}
	mui.Column(c).FillWidth().Gap(theme.Space2).Label("Leave out apps").Children(func() {
		f.laneRow(c, "Leave out", 124, func() {
			mui.Row(c).Grow(1).Shrink(1).Wrap().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
				for _, n := range names {
					mui.Row(c).Height(26).PaddingStart(10).PaddingEnd(4).Gap(4).AlignItems(mui.Center).Radius(theme.RadiusXs).
						Border(1, theme.RoleBorderStrong).Children(func() {
						mono(c, n).FontSize(theme.TextCaption)
						if closeButton(c, "Stop leaving out "+n) {
							f.hs.ExcludeApps = NewAppList(slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == n })...)
						}
					})
				}
				if !a.set.appAdding {
					b := mui.ButtonBase(c).Height(26).PaddingX(10).AlignItems(mui.Center).Radius(theme.RadiusXs).
						Label("Add an app to leave out").Cursor(mui.CursorPointer)
					b.Draw(func(p *mui.Painter, r mui.Rect) { p.StrokeDashed(r, theme.RoleBorderStrong, theme.RadiusXs, 1) })
					b.Children(func() { caption(c, "+ App").TextColor(theme.RoleText2) })
					if b.Clicked() {
						a.set.appAdding, a.set.appDraft = true, ""
					}
				}
			})
		})
		if a.set.appAdding {
			a.appPicker(c, names, add)
		}
		if a.goos != "darwin" {
			caption(c, "Leaving apps out needs a macOS host for now. This PC streams everything but Planck.").FixedLineHeight(theme.LeadingCaption)
		}
	})
}

// appPicker adds an app by name, typed or picked from the common and the
// running ones.
func (a *App) appPicker(c *mui.Context, have []string, add func(string)) {
	in := mui.TextInput(c.Key("exclude-app"), &a.set.appDraft).FillWidth().Height(theme.SizeControl).
		Placeholder("App name, as Discord").Label("App name").Radius(theme.RadiusSm).Font(theme.FontMono).FontSize(12).
		Background(theme.RoleRaised).Border(1, theme.RoleControlEdge).
		OnShortcut(0, mui.KeyEscape, func() { a.set.appAdding = false })
	if in.Submitted() {
		if n := strings.TrimSpace(a.set.appDraft); n != "" {
			add(n)
		}
		a.set.appDraft = ""
	}
	var picks []string
	for _, n := range append(slices.Clone(quickApps), a.backend.RunningApps()...) {
		if len(picks) == 12 {
			break
		}
		taken := func(s string) bool { return strings.EqualFold(s, n) }
		if strings.TrimSpace(n) != "" && !slices.ContainsFunc(have, taken) && !slices.ContainsFunc(picks, taken) {
			picks = append(picks, n)
		}
	}
	mui.Row(c).FillWidth().Wrap().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
		for _, n := range picks {
			if chip(c, "Leave out "+n, "+ "+n, false, 26) {
				add(n)
			}
		}
		mui.Spacer(c)
		if textLink(c, "Done", theme.RoleText2) {
			a.set.appAdding, a.set.appDraft = false, ""
		}
	})
}
