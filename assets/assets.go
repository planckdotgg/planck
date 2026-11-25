// Package assets holds Planck's brand files. The client embeds the static
// fonts and the interface icons; the PNG and ICO files are for packaging.
package assets

import "embed"

//go:embed fonts/*.ttf icons/ui-*.svg icons/planck-mark-glyph.svg
var FS embed.FS

// AppIconMacOSPNG is the 1024 px macOS app icon, the Dock and Cmd+Tab image.
//
//go:embed png/app-icon-macos-1024.png
var AppIconMacOSPNG []byte

// AppIconPlatedPNG is the 256 px plated app icon for the Windows and Linux
// taskbar and window.
//
//go:embed png/app-icon-plated-256.png
var AppIconPlatedPNG []byte

// AppIcon returns the app icon PNG for the given GOOS.
func AppIcon(goos string) []byte {
	if goos == "darwin" {
		return AppIconMacOSPNG
	}
	return AppIconPlatedPNG
}
