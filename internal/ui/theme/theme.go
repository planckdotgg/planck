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

	IconFullscreen   = mustSVG("icons/ui-fullscreen.svg")
	IconImmersive    = mustSVG("icons/ui-immersive.svg")
	IconCursor       = mustSVG("icons/ui-cursor.svg")
	IconDirect       = mustSVG("icons/ui-direct.svg")
	IconDisplay      = mustSVG("icons/ui-display.svg")
	IconResolution   = mustSVG("icons/ui-resolution.svg")
	IconCodec        = mustSVG("icons/ui-codec.svg")
	IconColor        = mustSVG("icons/ui-color.svg")
	IconFrameRate    = mustSVG("icons/ui-framerate.svg")
	IconBitrate      = mustSVG("icons/ui-bitrate.svg")
	IconVSync        = mustSVG("icons/ui-vsync.svg")
	IconSound        = mustSVG("icons/ui-sound.svg")
	IconClipboard    = mustSVG("icons/ui-clipboard.svg")
	IconMetrics      = mustSVG("icons/ui-metrics.svg")
	IconHide         = mustSVG("icons/ui-hide.svg")
	IconPower        = mustSVG("icons/ui-power.svg")
	IconChevronRight = mustSVG("icons/ui-chevron-right.svg")
	IconClose        = mustSVG("icons/ui-close.svg")
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

// Overlay chrome over live video, without backdrop blur: the alpha does
// the blur's work.
var (
	HUDGlass     = ui.RGBA(14, 15, 18, 0.92)    // panels, the button hovered, the toast
	HUDGlassIdle = ui.RGBA(14, 15, 18, 0.55)    // the button at rest
	HUDGlassTab  = ui.RGBA(14, 15, 18, 0.82)    // the docked edge tab
	HUDEdge      = ui.RGBA(242, 239, 234, 0.10) // hairline
	HUDEdgeLit   = ui.RGBA(242, 239, 234, 0.18) // hovered, dragged or popped out
	HUDDivider   = ui.RGBA(242, 239, 234, 0.08) // inside a panel
	HUDRowHover  = ui.RGBA(242, 239, 234, 0.06) // a menu row under the pointer, an open row
	HUDTray      = ui.RGBA(242, 239, 234, 0.04) // an inline picker tray
	HUDTrace     = ui.RGBA(255, 122, 31, 0.55)  // the line left on the edge by a popped-out button
	HUDDockGlow  = ui.RGBA(255, 122, 31, 0.30)  // the dock target's wash while dragging
)

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
