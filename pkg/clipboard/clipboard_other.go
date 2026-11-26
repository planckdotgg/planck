//go:build !darwin && !windows && !linux

package clipboard

// Linux has no backend yet: Wayland's ext-data-control-v1 (or
// wlr-data-control) and X11's XFixes selection events fit Board as it is.
func openBoard() (Board, error) { return nil, ErrUnsupported }
