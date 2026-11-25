package ui

import (
	"errors"
	"slices"
	"strings"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

func (a *App) inputPage(c *mui.Context, f *settingsFrame) {
	settingsHeader(c, "Input & Hotkeys", "Chords stay on "+a.chordWord()+" so "+a.hostKeysWord()+" keys reach the host. All are yours to change.", nil)
	columns(c, 568, 320, func() {
		setPanel(c, "Hotkeys", func() { a.resetAllLink(c) }, func() { a.entries(c, f, "input.hotkeys") })
	}, func() {
		setPanel(c, "Input", nil, func() { a.entries(c, f, "input.syskeys", "input.mouse", "input.swap") })
		setPanel(c, "Never send these keys", nil, func() { a.entries(c, f, "input.neverSend") })
		setPanel(c, "What the capture field accepts", nil, func() { a.entries(c, f, "input.rules") })
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

// hotkeyEdit is the chord capture in progress on Input & Hotkeys.
type hotkeyEdit struct {
	// recording is the hotkey whose pill takes the next chord; never is
	// the never-send list's "+ Record a chord" doing so instead.
	recording HotkeyAction
	never     bool
	// focus gives the recording pill the keyboard on the next frame.
	focus bool
	// errAction and err are the last chord refused for a hotkey, shown
	// under its row; neverErr is the never-send list's.
	errAction HotkeyAction
	err       string
	neverErr  string
}

func (e *hotkeyEdit) stop() { e.recording, e.never, e.focus = "", false, false }

func (a *App) resetAllLink(c *mui.Context) {
	if textLink(c, "Reset all to defaults", theme.RoleText3) {
		a.set.keys.stop()
		a.set.keys.err, a.set.keys.errAction = "", ""
		a.setSettingsErr(a.backend.ResetAllHotkeys())
	}
}

// hotkeyTable is every hotkey with its chord pill and reset: the stream's,
// then the host's, which fire while Planck is not in front.
func (a *App) hotkeyTable(c *mui.Context, f *settingsFrame) {
	keys := a.backend.Hotkeys()
	named := func(h Hotkey) bool { return strings.Contains(strings.ToLower(hotkeyNames[h.Action]), f.query) }
	// A search that names no hotkey found the table by its other words:
	// it shows whole.
	all := !f.searching || !slices.ContainsFunc(keys, named)
	shown := func(h Hotkey) bool { return all || named(h) }
	mui.Column(c).FillWidth().Label("Hotkeys").Children(func() {
		mui.Row(c).FillWidth().Height(28).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
			sectionLabel(c, "Action").Grow(1)
			sectionLabel(c, "Shortcut").Width(150)
			mui.Box(c).Width(24)
		})
		headed := false
		for _, h := range keys {
			if !shown(h) {
				continue
			}
			if h.Global && !headed {
				headed = true
				divider(c)
				mui.Row(c).FillWidth().Height(28).AlignItems(mui.End).Gap(theme.Space2).Children(func() {
					sectionLabel(c, "When hosting")
					caption(c, "these fire even when Planck is not in front")
				})
			}
			divider(c)
			a.hotkeyRow(c, f, h)
		}
		divider(c)
	})
}

func (a *App) hotkeyRow(c *mui.Context, f *settingsFrame, h Hotkey) {
	name := hotkeyNames[h.Action]
	e := &a.set.keys
	mui.Row(c).FillWidth().Height(32).AlignItems(mui.Center).Gap(theme.Space2).Children(func() {
		f.title(c, name, theme.RoleText)
		mui.Spacer(c)
		recording := e.recording == h.Action
		pill := a.chordPill(c, "Shortcut for "+name, chordLabel(a.goos, h.Chord), recording, 150)
		if pill.Clicked() && !recording {
			e.stop()
			e.recording, e.focus = h.Action, true
			e.err, e.errAction = "", ""
		}
		if recording {
			action, old := h.Action, h.Chord
			pill.HandleInput(func(ev mui.InputEvent) bool { return a.hotkeyKey(ev, action, old) })
			a.holdFocus(pill)
		}
		if h.Chord == h.Default {
			mui.Box(c).Size(24, 24)
			return
		}
		r := mui.ButtonBase(c).Size(24, 24).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).
			Label("Reset " + name).Cursor(mui.CursorPointer)
		col := theme.RoleText3
		if r.Hovered() {
			col = theme.RoleText
		}
		r.Children(func() { mui.Icon(c, theme.IconReset).Size(14, 14).TextColor(col) })
		if r.Clicked() {
			e.stop()
			e.err, e.errAction = "", ""
			if err := a.backend.ResetHotkey(h.Action); err != nil {
				e.err, e.errAction = a.chordRefused(err, h.Chord), h.Action
			}
		}
	})
	if e.errAction == h.Action && e.err != "" {
		mui.Box(c).FillWidth().MinHeight(18).Children(func() { warningLine(c, e.err) })
	}
}

// holdFocus gives the recording pill the keyboard once, and ends the
// recording when the keyboard leaves it.
func (a *App) holdFocus(pill mui.Element) {
	e := &a.set.keys
	if e.focus {
		e.focus = false
		pill.Focus(mui.FocusOptions{PreventScroll: true})
		return
	}
	if pill.FocusLeft() {
		e.stop()
	}
}

// chordPill is a chord's 24 px pill: the chord, Press keys… while it
// records, or a dashed Set a shortcut while unbound. It is a button named
// name.
func (a *App) chordPill(c *mui.Context, name, chord string, recording bool, width float32) mui.Element {
	b := mui.ButtonBase(c.Key(name)).Size(width, 24).Shrink(0).AlignItems(mui.Center).Justify(mui.Center).Gap(6).
		Radius(theme.RadiusXs).Label(name).Cursor(mui.CursorPointer).Focusable()
	switch {
	case recording:
		b.Background(theme.RoleWell).Border(1, theme.RoleAccent).Description("Recording: press keys, Esc to cancel")
		b.Children(func() {
			caption(c, "Press keys…").TextColor(theme.RoleText2)
			mui.Box(c).Size(1, 12).Background(theme.RoleAccent)
		})
	case chord == "":
		b.Draw(func(p *mui.Painter, r mui.Rect) { p.StrokeDashed(r, theme.RoleBorderStrong, theme.RadiusXs, 1) })
		b.Description("Not set")
		b.Children(func() { caption(c, "Set a shortcut") })
	default:
		edge := theme.RoleBorder
		if b.Hovered() {
			edge = theme.RoleBorderStrong
		}
		b.Background(theme.RoleWell).Border(1, edge).Description(chord)
		b.Children(func() {
			mono(c, chord).FontSize(theme.TextCaption).LetterSpacing(0.06 * theme.TextCaption)
		})
	}
	return b
}

// chordOf is the chord a key press makes, false for a key Planck has no
// HID usage for or a modifier on its own.
func chordOf(ev mui.InputEvent) (string, bool) {
	u, ok := HIDUsage[ev.Key]
	if !ok {
		u, ok = HIDUsage[ev.PhysicalKey]
	}
	if !ok {
		return "", false
	}
	return ChordOf(ev.Mods, u).String(), true
}

// hotkeyKey takes a key pressed while action's pill records: Esc keeps
// old, Backspace on its own unbinds it, and any other chord binds it or
// says why not.
func (a *App) hotkeyKey(ev mui.InputEvent, action HotkeyAction, old string) bool {
	if ev.Kind != mui.InputKeyDown {
		return ev.Kind == mui.InputKeyUp || ev.Kind == mui.InputText
	}
	e := &a.set.keys
	if ev.Repeat {
		return true
	}
	switch {
	case ev.Mods == 0 && ev.Key == mui.KeyEscape:
		e.stop()
		e.err, e.errAction = "", ""
		return true
	case ev.Mods == 0 && (ev.Key == mui.KeyBackspace || ev.Key == mui.KeyDelete):
		e.stop()
		e.err, e.errAction = "", ""
		a.setSettingsErr(a.backend.SetHotkey(action, ""))
		return true
	}
	chord, ok := chordOf(ev)
	if !ok {
		return ev.Key != mui.KeyUnknown
	}
	if err := a.backend.SetHotkey(action, chord); err != nil {
		e.errAction = action
		e.err = a.chordRefused(err, old)
		return true
	}
	e.stop()
	e.err, e.errAction = "", ""
	return true
}

// chordRefused says why a chord was refused for a hotkey whose chord was
// old, and what to do next.
func (a *App) chordRefused(err error, old string) string {
	keep := "Press another chord, or Esc to leave it unset."
	if l := chordLabel(a.goos, old); l != "" {
		keep = "Press another chord, or Esc to keep " + l + "."
	}
	var he *HotkeyError
	if !errors.As(err, &he) {
		return err.Error()
	}
	chord := chordLabel(a.goos, he.Chord.String())
	switch {
	case errors.Is(err, ErrChordInUse) && he.Owner != "":
		return chord + " already belongs to " + hotkeyNames[he.Owner] + ". " + keep
	case errors.Is(err, ErrChordInUse):
		return chord + " is on the never-send list. " + keep
	case errors.Is(err, ErrReservedChord):
		return chord + ": " + he.Reason + ". " + keep
	case errors.Is(err, ErrNoModifier):
		return "A shortcut needs at least one modifier. " + keep
	case errors.Is(err, ErrNotGlobal):
		return chord + " cannot fire while Planck is not in front. " + keep
	case errors.Is(err, ErrDesktopRefused):
		return chord + " is taken by another app. " + keep
	}
	return err.Error()
}

// swapRow trades Cmd and Ctrl for a host of the other OS family.
func (a *App) swapRow(c *mui.Context, f *settingsFrame) {
	title, help := "Swap ⌘ and Ctrl on Windows hosts", "⌘C copies on the host instead of Ctrl+C."
	if a.goos != "darwin" {
		title, help = "Swap Ctrl and ⌘ on Mac hosts", "Ctrl+C copies on a Mac host as ⌘C."
	}
	f.switchRow(c, title, help, &f.s.SwapCmdCtrl)
}

// neverSendList is the chords kept on this device: a chip each with its
// remove, and a pill that records one more.
func (a *App) neverSendList(c *mui.Context, f *settingsFrame) {
	e := &a.set.keys
	mui.Column(c).FillWidth().Gap(theme.Space3).Label("Never send these keys").Children(func() {
		caption(c, "Kept on this "+machineWord(a.goos)+" even in immersive mode: push-to-talk, screenshots, whatever you need locally.").
			FixedLineHeight(theme.LeadingCaption)
		mui.Row(c).FillWidth().Wrap().Gap(theme.Space2).AlignItems(mui.Center).Children(func() {
			for _, chord := range a.backend.NeverSend() {
				a.neverChip(c, chord)
			}
		})
		mui.Row(c).FillWidth().Children(func() { a.neverRecord(c) })
		if e.neverErr != "" {
			warningLine(c, e.neverErr)
		}
	})
}

// neverRecord is + Record a chord, or its pill while it records.
func (a *App) neverRecord(c *mui.Context) {
	e := &a.set.keys
	const name = "Record a chord to never send"
	if e.never {
		pill := a.chordPill(c, name, "", true, 150)
		pill.HandleInput(a.neverKey)
		a.holdFocus(pill)
		return
	}
	b := mui.ButtonBase(c.Key(name)).Height(26).PaddingX(10).AlignItems(mui.Center).Radius(theme.RadiusXs).
		Label(name).Cursor(mui.CursorPointer)
	col := theme.RoleText3
	if b.Hovered() {
		col = theme.RoleText2
	}
	b.Draw(func(p *mui.Painter, r mui.Rect) { p.StrokeDashed(r, theme.RoleBorderStrong, theme.RadiusXs, 1) })
	b.Children(func() { caption(c, "+ Record a chord").TextColor(col) })
	if b.Clicked() {
		e.stop()
		e.never, e.focus, e.neverErr = true, true, ""
	}
}

func (a *App) neverChip(c *mui.Context, chord string) {
	l := chordLabel(a.goos, chord)
	mui.Row(c).Height(26).PaddingStart(10).PaddingEnd(4).Gap(4).AlignItems(mui.Center).Radius(theme.RadiusXs).
		Border(1, theme.RoleBorderStrong).Children(func() {
		mono(c, l).FontSize(theme.TextCaption).LetterSpacing(0.06 * theme.TextCaption)
		if closeButton(c, "Send "+l+" to the host again") {
			a.set.keys.neverErr = ""
			a.setSettingsErr(a.backend.RemoveNeverSend(chord))
		}
	})
}

// closeButton is a chip's 18 px ×; it reports a click.
func closeButton(c *mui.Context, name string) bool {
	b := mui.ButtonBase(c).Size(18, 18).AlignItems(mui.Center).Justify(mui.Center).Radius(theme.RadiusXs).
		Label(name).Cursor(mui.CursorPointer)
	col := theme.RoleText3
	if b.Hovered() {
		col = theme.RoleText
	}
	b.Children(func() { mui.Icon(c, theme.IconClose).Size(10, 10).TextColor(col) })
	return b.Clicked()
}

// neverKey takes a key pressed while the never-send pill records: Esc
// cancels; any other key, bare or with modifiers, joins the list.
func (a *App) neverKey(ev mui.InputEvent) bool {
	if ev.Kind != mui.InputKeyDown {
		return ev.Kind == mui.InputKeyUp || ev.Kind == mui.InputText
	}
	e := &a.set.keys
	if ev.Repeat {
		return true
	}
	if ev.Mods == 0 && ev.Key == mui.KeyEscape {
		e.stop()
		e.neverErr = ""
		return true
	}
	chord, ok := chordOf(ev)
	if !ok {
		return ev.Key != mui.KeyUnknown
	}
	if err := a.backend.AddNeverSend(chord); err != nil {
		e.neverErr = a.neverRefused(err)
		return true
	}
	e.stop()
	e.neverErr = ""
	return true
}

func (a *App) neverRefused(err error) string {
	var he *HotkeyError
	if errors.As(err, &he) && errors.Is(err, ErrChordInUse) && he.Owner != "" {
		return chordLabel(a.goos, he.Chord.String()) + " is the " + hotkeyNames[he.Owner] + " hotkey, which never reaches the host already. Press another chord, or Esc."
	}
	return err.Error()
}

// captureRules are the rules the chord capture keeps, as the user sees
// them.
func (a *App) captureRules(c *mui.Context, f *settingsFrame) {
	clear := "⌫"
	if a.goos != "darwin" {
		clear = "Backspace"
	}
	mui.Column(c).FillWidth().Gap(theme.Space2).Children(func() {
		for _, r := range []string{
			"At least one modifier. A bare letter is refused.",
			"Chords the OS owns are refused, with the reason.",
			"A chord already in use names the action it belongs to.",
			"Esc keeps the old chord, " + clear + " clears it.",
			"Bound chords stay local even in immersive mode.",
		} {
			mui.Row(c).FillWidth().Gap(10).AlignItems(mui.Start).Children(func() {
				mui.Box(c).Size(3, 16).Shrink(0).Margin(1, 0, 0).Background(theme.RoleBorderStrong)
				secondary(c, r).FontSize(12).FixedLineHeight(18).Grow(1).Shrink(1)
			})
		}
	})
}
