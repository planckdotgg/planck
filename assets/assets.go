// Package assets holds Planck's brand files. The client embeds the static
// fonts and the interface icons; the PNG and ICO files are for packaging.
package assets

import "embed"

//go:embed fonts/*.ttf icons/ui-*.svg icons/planck-mark-glyph.svg
var FS embed.FS
