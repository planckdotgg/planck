package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/gamepad"
)

// gamepadEdit is the Gamepad page's own state across frames.
type gamepadEdit struct {
	// sel is the controller the preview and mapping show, by ID; target
	// is the input the mapping controls act on, by SDL name.
	sel    string
	target string
	// err is the last mapping action's failure, note its success.
	err, note string
}

func (a *App) gamepadPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Gamepad", "Controllers on this "+machineWord(a.goos)+", and what clients’ controllers become.", nil)
	columns(c, 1, 1, func() {
		setPanel(c, "Connected controllers", nil, func() {
			if len(f.gamepads(a)) == 0 {
				a.entries(c, f, "gamepad.controllers")
				return
			}
			a.entries(c, f, "gamepad.controllers", "gamepad.preview", "gamepad.mapping")
		})
		setPanel(c, "Calibration and feel", nil, func() {
			a.entries(c, f, "gamepad.leftDeadzone", "gamepad.rightDeadzone", "gamepad.trigger", "gamepad.invertLeft", "gamepad.invertRight",
				"gamepad.rumble", "gamepad.rumbleStrength")
		})
	}, func() {
		setPanel(c, "When hosting", nil, func() { a.entries(c, f, "gamepad.allowGuests", "gamepad.virtualType", "gamepad.driver") })
		setPanel(c, "Controller slots", nil, func() { a.entries(c, f, "gamepad.slots") })
	})
}

// gamepads are the backend's controllers, asked once a frame and only by
// a row that shows them: asking keeps the controllers being read.
func (f *settingsFrame) gamepads(a *App) []GamepadInfo {
	if f.pads == nil {
		pads := a.backend.Gamepads()
		f.pads = &pads
	}
	return *f.pads
}

// selectedPad is the controller the preview and mapping act on: the one
// picked, else the first.
func (a *App) selectedPad(f *settingsFrame) (GamepadInfo, bool) {
	pads := f.gamepads(a)
	for _, p := range pads {
		if p.ID == a.set.pad.sel {
			return p, true
		}
	}
	if len(pads) > 0 {
		return pads[0], true
	}
	return GamepadInfo{}, false
}

func (a *App) controllerList(c *mui.Context, f *settingsFrame) {
	pads := f.gamepads(a)
	if len(pads) == 0 {
		mui.Column(c).FillWidth().Padding(theme.Space4, 0).Gap(theme.Space1).Label("No controllers").Children(func() {
			f.title(c, "No controllers attached", theme.RoleText)
			caption(c, "Plug one in, or pair it over Bluetooth. It shows here as soon as this "+machineWord(a.goos)+" sees it.").
				FixedLineHeight(theme.LeadingCaption)
		})
		return
	}
	sel, _ := a.selectedPad(f)
	mui.Column(c).FillWidth().Gap(2).Children(func() {
		for _, p := range pads {
			a.controllerRow(c, p, p.ID == sel.ID)
		}
	})
}

func (a *App) controllerRow(c *mui.Context, p GamepadInfo, selected bool) {
	b := mui.ButtonBase(c).FillWidth().MinHeight(40).PaddingX(10).Gap(theme.Space3).AlignItems(mui.Center).Radius(theme.RadiusSm).
		Label(p.Name).Selected(selected).Cursor(mui.CursorPointer)
	switch {
	case selected:
		b.Background(theme.RoleRaised)
	case b.Hovered():
		b.Background(theme.RoleHover)
	}
	b.Children(func() {
		mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			label(c, p.Name).FontWeight(500).SingleLine()
			caption(c, "Pad "+strconv.Itoa(p.Slot+1)+" in a stream")
		})
		if p.BatteryPct >= 0 {
			mui.Row(c).Gap(4).AlignItems(mui.Center).Label("Battery " + strconv.Itoa(p.BatteryPct) + " %").Children(func() {
				col := theme.RoleDirect
				if p.BatteryPct <= 25 {
					col = theme.RoleFault
				}
				sliceMeter(c, (p.BatteryPct+24)/25, 4, col)
				mono(c, strconv.Itoa(p.BatteryPct)+" %").FontSize(theme.TextCaption).TextColor(theme.RoleText2)
			})
		}
		if p.Transport != "" {
			mui.Box(c).Height(20).PaddingX(6).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).Border(1, theme.RoleBorderStrong).Children(func() {
				mono(c, p.Transport).FontSize(theme.TextMini).TextColor(theme.RoleText2)
			})
		}
	})
	if b.Clicked() {
		a.set.pad.sel = p.ID
		a.set.pad.err, a.set.pad.note = "", ""
	}
}

// padButtons are the buttons the live state shows, in XInput order.
var padButtons = []struct {
	bit  uint32
	name string
}{
	{gamepad.ButtonA, "A"}, {gamepad.ButtonB, "B"}, {gamepad.ButtonX, "X"}, {gamepad.ButtonY, "Y"},
	{gamepad.ButtonLeftShoulder, "LB"}, {gamepad.ButtonRightShoulder, "RB"},
	{gamepad.ButtonBack, "Back"}, {gamepad.ButtonStart, "Start"}, {gamepad.ButtonGuide, "Guide"},
	{gamepad.ButtonLeftThumb, "LS"}, {gamepad.ButtonRightThumb, "RS"},
	{gamepad.ButtonDPadUp, "↑"}, {gamepad.ButtonDPadDown, "↓"}, {gamepad.ButtonDPadLeft, "←"}, {gamepad.ButtonDPadRight, "→"},
}

// livePreview is the selected controller's state as a stream sends it,
// and Test rumble.
func (a *App) livePreview(c *mui.Context, f *settingsFrame) {
	p, ok := a.selectedPad(f)
	if !ok {
		return
	}
	mui.Column(c).FillWidth().Gap(theme.Space2).Label("Live state").Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Center).Children(func() {
			f.title(c, "Live state", theme.RoleText)
			mui.Spacer(c)
			if button(c, buttonSecondary, "Test rumble", false).Clicked() {
				a.set.pad.err, a.set.pad.note = "", ""
				if err := a.backend.TestGamepadRumble(p.ID); err != nil {
					a.set.pad.err = "Could not rumble: " + err.Error()
				}
			}
		})
		mui.Row(c).FillWidth().Wrap().Gap(4).Children(func() {
			for _, pb := range padButtons {
				padButton(c, pb.name, p.State.Buttons&pb.bit != 0)
			}
		})
		s := p.State
		axisRow(c, "Left stick", stickNorm(s.LX), stickNorm(s.LY))
		axisRow(c, "Right stick", stickNorm(s.RX), stickNorm(s.RY))
		mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Center).Children(func() {
			triggerBar(c, "LT", s.LT)
			triggerBar(c, "RT", s.RT)
		})
	})
}

func stickNorm(v int16) float64 { return float64(v) / 32767 }

func padButton(c *mui.Context, name string, down bool) {
	state := " released"
	if down {
		state = " pressed"
	}
	box := mui.Box(c).Height(22).MinWidth(26).PaddingX(6).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).
		Label(name + state)
	col := theme.RoleText3
	if down {
		box.Background(theme.RoleAccent)
		col = theme.RoleOnAccent
	} else {
		box.Border(1, theme.RoleBorder)
	}
	box.Children(func() { mono(c, name).FontSize(theme.TextMini).TextColor(col) })
}

// axisRow shows a stick's X and Y, each a bar from the centre and its
// value in percent.
func axisRow(c *mui.Context, name string, x, y float64) {
	mui.Row(c).FillWidth().Gap(theme.Space3).AlignItems(mui.Center).Label(fmt.Sprintf("%s X %+.0f %%, Y %+.0f %%", name, x*100, y*100)).Children(func() {
		caption(c, name).Width(84)
		for _, v := range [2]float64{x, y} {
			mui.Box(c).Grow(1).Basis(0).Height(6).Draw(func(p *mui.Painter, r mui.Rect) {
				p.Fill(r, theme.RoleWell, 3)
				mid := r.X + r.W/2
				w := float32(v) * r.W / 2
				if w < 0 {
					p.Fill(mui.Rect{X: mid + w, Y: r.Y, W: -w, H: r.H}, theme.RoleAccent, 3)
				} else {
					p.Fill(mui.Rect{X: mid, Y: r.Y, W: w, H: r.H}, theme.RoleAccent, 3)
				}
				p.Fill(mui.Rect{X: mid - 0.5, Y: r.Y - 1, W: 1, H: r.H + 2}, theme.RoleBorderStrong, 0)
			})
			mono(c, fmt.Sprintf("%+4.0f", v*100)).FontSize(theme.TextCaption).TextColor(theme.RoleText2).Width(36)
		}
	})
}

func triggerBar(c *mui.Context, name string, v uint8) {
	frac := float32(v) / 255
	mui.Row(c).Grow(1).Basis(0).Gap(theme.Space2).AlignItems(mui.Center).Label(fmt.Sprintf("%s %.0f %%", name, frac*100)).Children(func() {
		mono(c, name).FontSize(theme.TextCaption).TextColor(theme.RoleText2).Width(20)
		mui.Box(c).Grow(1).Height(6).Draw(func(p *mui.Painter, r mui.Rect) {
			p.Fill(r, theme.RoleWell, 3)
			p.Fill(mui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, theme.RoleAccent, 3)
		})
	})
}

// padInputs are the inputs a mapping binds, by SDL name, and how the
// picker names them.
var (
	padInputs = func() []string {
		out := make([]string, gamepad.NumInputs)
		for i := range out {
			out[i] = gamepad.Input(i).String()
		}
		return out
	}()
	padInputLabels = []string{"A", "B", "X", "Y", "Back", "Guide", "Start", "Left stick press", "Right stick press",
		"Left bumper", "Right bumper", "D-pad up", "D-pad down", "D-pad left", "D-pad right",
		"Left stick X", "Left stick Y", "Right stick X", "Right stick Y", "Left trigger", "Right trigger"}
)

func padInputLabel(name string) string {
	if i := indexOf(padInputs, name); i >= 0 {
		return padInputLabels[i]
	}
	return name
}

// learnPrompt says what to move for target while Map waits.
func learnPrompt(target string) string {
	l := padInputLabel(target)
	switch target {
	case "leftx", "rightx":
		return "Push the stick for " + l + " to the right."
	case "lefty", "righty":
		return "Push the stick for " + l + " down."
	case "lefttrigger", "righttrigger":
		return "Pull the trigger for " + l + "."
	}
	return "Press the control for " + l + "."
}

// bindingText is what drives target in a mapping line, "" when nothing.
func bindingText(line, target string) string {
	m, err := gamepad.ParseSDLMapping(line)
	in, ierr := gamepad.ParseInput(target)
	if err != nil || ierr != nil {
		return ""
	}
	parts := []string{}
	if b := m.Bindings[in].String(); b != "" {
		parts = append(parts, b)
	}
	for i, sign := range [2]string{"+", "-"} {
		if b := m.Halves[in][i].String(); b != "" {
			parts = append(parts, sign+" "+b)
		}
	}
	return strings.Join(parts, ", ")
}

// mappingRows pick an input of the selected controller to map, unmap or
// reset, and copy or paste the whole mapping as an SDL gamecontrollerdb
// line.
func (a *App) mappingRows(c *mui.Context, f *settingsFrame) {
	p, ok := a.selectedPad(f)
	if !ok {
		return
	}
	ed := &a.set.pad
	if indexOf(padInputs, ed.target) < 0 {
		ed.target = padInputs[0]
	}
	mui.Column(c).FillWidth().Gap(theme.Space2).Label("Button mapping").Children(func() {
		f.row(c, "Button mapping", mappingHelp(p), func() {})
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			dropdown(c, "Input to map", &ed.target, padInputs, padInputLabels, 168)
			bound := bindingText(p.Mapping, ed.target)
			if bound == "" {
				bound = "unmapped"
			}
			mono(c, bound).FontSize(12).TextColor(theme.RoleText2).Grow(1).Shrink(1).SingleLine()
		})
		mui.Row(c).FillWidth().Wrap().Gap(theme.Space2).Children(func() {
			act := func(text string, do func() error) {
				if button(c, buttonSecondary, text, false).Clicked() {
					ed.err, ed.note = "", ""
					if err := do(); err != nil {
						ed.err = err.Error()
					}
				}
			}
			act("Map", func() error { return a.backend.MapGamepadButton(p.ID, ed.target) })
			act("Unmap", func() error { return a.backend.UnmapGamepadButton(p.ID, ed.target) })
			act("Reset to default", func() error { return a.backend.ResetGamepadMapping(p.ID) })
			if button(c, buttonGhost, "Copy SDL map", false).Clicked() {
				c.WriteClipboard(p.Mapping)
				ed.err, ed.note = "", "Copied the mapping."
			}
			if button(c, buttonGhost, "Paste SDL map", false).Clicked() {
				ed.err, ed.note = "", ""
				if err := a.backend.SetGamepadSDLMapping(p.ID, c.ReadClipboard()); err != nil {
					ed.err = "That is not an SDL mapping: " + strings.TrimPrefix(err.Error(), "gamepad: ")
				} else {
					ed.note = "Pasted the mapping."
				}
			}
		})
		switch {
		case p.Learning != "":
			infoWell(c, learnPrompt(p.Learning)+" Map waits ten seconds.")
		case ed.err != "":
			warningLine(c, ed.err)
		case ed.note != "":
			caption(c, ed.note).TextColor(theme.RoleDirect)
		}
	})
}

func mappingHelp(p GamepadInfo) string {
	if p.Custom {
		return "A saved mapping for every " + p.Name + "."
	}
	return "The default mapping for " + p.Name + "."
}

// percentRow is a 0 to max percent slider.
func (f *settingsFrame) percentRow(c *mui.Context, title, help string, v *int, limit int) {
	f.row(c, title, help, func() {
		x := float64(*v)
		c.Key("pct:" + title)
		if mui.Slider(c, &x, 0, float64(limit)).Label(title).Width(160).Changed() {
			*v = int(math.Round(x))
		}
		mono(c, strconv.Itoa(*v)+" %").FontSize(theme.TextCaption).TextColor(theme.RoleText2).Width(44)
	})
}

// The limits of GamepadSettings' percentages.
const (
	gamepadMaxDeadzone = config.MaxGamepadDeadzone
	gamepadMaxTrigger  = config.MaxGamepadTriggerThreshold
	gamepadMaxRumble   = config.MaxGamepadRumbleStrength
)

var (
	virtualPadValues = []string{VirtualPadXbox360, VirtualPadDS4, VirtualPadDualSense}
	virtualPadLabels = []string{"Xbox 360", "DualShock 4", "DualSense"}
)

func (a *App) virtualTypeRow(c *mui.Context, f *settingsFrame) {
	f.row(c, "Virtual controller type", "What games see a client’s controllers as.", func() {
		dropdown(c, "Virtual controller type", &f.gp.VirtualType, virtualPadValues, virtualPadLabels, 160)
	})
	st := a.backend.VirtualPadStatus()
	if f.gp.VirtualType == VirtualPadDualSense && st.Installed && st.Effective(gamepad.PadDualSense) != gamepad.PadDualSense {
		infoWell(c, st.DriverName+" has no DualSense. Games see a DualShock 4, which most treat the same.")
	}
}

// virtualPadStatusLine says whether this machine has a virtual controller
// driver and, when not, how to get one.
func (a *App) virtualPadStatusLine(c *mui.Context, f *settingsFrame) {
	st := a.backend.VirtualPadStatus()
	bars, col, text, hint := 3, theme.RoleDirect, st.DriverName+" is installed.", ""
	switch {
	case st.Installed:
	case st.DriverName != "":
		bars, col, text, hint = 0, theme.RoleText3, st.DriverName+" is not installed.", st.InstallHint
	case st.InstallHint == "":
		bars, col, text = 0, theme.RoleText3, "Checking for a virtual gamepad driver…"
	default:
		bars, col, text = 0, theme.RoleText3, st.InstallHint
	}
	f.row(c, "Virtual gamepad driver", "", func() {})
	mui.Column(c).FillWidth().Gap(theme.Space1).Label("Virtual gamepad driver status").Children(func() {
		mui.Row(c).FillWidth().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			sliceMeter(c, bars, 3, col)
			secondary(c, text).Grow(1).Shrink(1)
		})
		if hint != "" {
			caption(c, hint).FixedLineHeight(theme.LeadingCaption)
		}
	})
}

// slotRows are the host's four virtual controller slots, each with its
// moves, and Reset slots.
func (a *App) slotRows(c *mui.Context, f *settingsFrame) {
	slots := a.backend.HostGamepadSlots()
	mui.Column(c).FillWidth().Gap(theme.Space2).Label("Controller slots").Children(func() {
		mui.Row(c).FillWidth().AlignItems(mui.Center).Gap(theme.Space4).Children(func() {
			mui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
				f.title(c, "Controller slots", theme.RoleText)
				caption(c, "A game’s players 1 to 4. A client’s controllers take them in the order they first send input.").
					FixedLineHeight(theme.LeadingCaption)
			})
			if slots != nil && button(c, buttonSecondary, "Reset slots", false).Clicked() {
				a.setSettingsErr(a.backend.ResetGamepadSlots())
			}
		})
		if slots == nil {
			caption(c, "Slots fill while this "+machineWord(a.goos)+" hosts.").TextColor(theme.RoleText3)
			return
		}
		for i, s := range slots {
			mui.Row(c).FillWidth().MinHeight(32).Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
				mono(c, strconv.Itoa(s.Slot+1)).FontSize(12).TextColor(theme.RoleText3).Width(16)
				who := "Empty"
				col := theme.RoleText3
				if s.Pad >= 0 {
					who, col = "Client controller "+strconv.Itoa(s.Pad+1), theme.RoleText
				}
				label(c, who).TextColor(col).Grow(1).Shrink(1).SingleLine()
				move := func(dir string, to int) {
					off := to < 0 || to >= len(slots)
					b := mui.ButtonBase(c).Height(theme.SizeControl).PaddingX(theme.Space2).AlignItems(mui.Center).Radius(theme.RadiusSm).
						Label("Move slot " + strconv.Itoa(s.Slot+1) + " " + strings.ToLower(dir)).Disabled(off)
					col := theme.RoleText2
					switch {
					case off:
						col = theme.RoleBorderStrong
					case b.Hovered():
						b.Background(theme.RoleHover).Cursor(mui.CursorPointer)
						col = theme.RoleText
					default:
						b.Cursor(mui.CursorPointer)
					}
					b.Children(func() { label(c, dir).FontWeight(600).TextColor(col) })
					if b.Clicked() {
						a.setSettingsErr(a.backend.ReorderGamepadSlot(i, to))
					}
				}
				move("Up", i-1)
				move("Down", i+1)
			})
		}
	})
}
