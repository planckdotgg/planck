// Package theme turns Planck's design tokens into a MyGo theme, and
// registers the brand fonts and icons the client draws with.
package theme

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/assets"
)

// fontFiles maps each embedded static instance to the family it joins.
var fontFiles = []struct{ file, family string }{
	{"fonts/Archivo-Regular.ttf", FontUI},
	{"fonts/Archivo-Medium.ttf", FontUI},
	{"fonts/Archivo-SemiBold.ttf", FontUI},
	{"fonts/Archivo-ExpandedSemiBold.ttf", FontDisplay},
	{"fonts/MartianMono-SemiCondensed.ttf", FontMono},
	{"fonts/MartianMono-SemiCondensedMedium.ttf", FontMono},
}

var (
	registerOnce sync.Once
	registerErr  error
)

// RegisterFonts registers Archivo, Archivo Expanded and Martian Mono with
// MyGo under FontUI, FontDisplay and FontMono. Call it once at start-up,
// before the first window opens; later calls return the first result.
func RegisterFonts() error {
	registerOnce.Do(func() {
		for _, f := range fontFiles {
			data, err := assets.FS.ReadFile(f.file)
			if err != nil {
				registerErr = err
				return
			}
			if err := ui.RegisterFont(data, f.family); err != nil {
				registerErr = fmt.Errorf("theme: register %s: %w", f.file, err)
				return
			}
		}
	})
	return registerErr
}

// Icons are parsed once: MyGo caches SVGs by identity.
var (
	Mark        = mustSVG("icons/planck-mark-glyph.svg")
	IconHosts   = mustSVG("icons/ui-hosts.svg")
	IconPairing = mustSVG("icons/ui-pairing.svg")
	IconSetting = mustSVG("icons/ui-settings.svg")
	IconHosting = mustSVG("icons/ui-hosting.svg")
	IconChevron = mustSVG("icons/ui-chevron-down.svg")
	IconEdit    = mustSVG("icons/ui-edit.svg")
)

func mustSVG(name string) *ui.SVG {
	data, err := assets.FS.ReadFile(name)
	if err != nil {
		panic("theme: " + err.Error())
	}
	return ui.MustParseSVG(data)
}

// FloatShadow is the one shadow, for things that float over content.
var FloatShadow = ui.RGBA(0, 0, 0, 0.55)

// HUDGlass is the HUD's fill over live video, without backdrop blur.
var HUDGlass = ui.RGBA(14, 15, 18, 0.92)

// HUDEdge is the HUD's hairline over live video.
var HUDEdge = ui.RGBA(242, 239, 234, 0.10)

// Planck returns the client's theme: dark only, built from the role
// tokens so MyGo's stock widgets (switches, sliders, inputs) match.
func Planck() *ui.Theme {
	t := ui.DarkTheme()
	t.Background = RoleGround
	t.Surface = RoleRaised
	t.SurfaceHover = RoleHover
	t.SurfacePressed = RoleBorder
	t.Border = RoleBorderStrong
	t.Text = RoleText
	t.TextMuted = RoleText3
	t.Accent = RoleAccent
	t.AccentHover = RoleAccentHover
	t.AccentPressed = RoleAccentPressed
	t.AccentText = RoleOnAccent
	t.InactiveAccent = RoleAccent
	t.Danger = RoleFault
	t.Warning = RoleRelay
	t.Success = RoleDirect
	t.Selection = RoleAccent.Alpha(0.35)
	t.Focus = RoleFocus
	t.FocusRingWidth = 2
	t.Inverse = RoleRaised
	t.InverseText = RoleText
	t.Scrollbar = RoleBorderStrong
	t.Radius = RadiusSm
	t.Spacing = Space1
	t.FontSize = TextBody
	t.Font = FontUI
	t.TooltipDelay = 400 * time.Millisecond
	return t
}
