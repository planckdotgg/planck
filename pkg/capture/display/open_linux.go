//go:build linux && !android && (amd64 || arm64)

package display

import (
	"fmt"
	"os"
	"strings"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/x11"
)

// Capture backends on Linux.
const (
	BackendX11    = "x11"
	BackendPortal = "portal"
)

// Backend is the capture backend for this session: PLANCK_CAPTURE_BACKEND
// when set ("x11" or "portal"), the desktop portal in a Wayland session,
// X11 where only $DISPLAY is set.
func Backend() string {
	if b := strings.ToLower(os.Getenv("PLANCK_CAPTURE_BACKEND")); b != "" {
		return b
	}
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return BackendPortal
	}
	if os.Getenv("DISPLAY") != "" {
		return BackendX11
	}
	return ""
}

// List returns the X screen's monitors under X11, and the portal's one
// display (the monitor the desktop's share dialog picks) under Wayland.
func List() ([]capture.Display, error) {
	switch b := Backend(); b {
	case BackendX11:
		return x11.Displays()
	case BackendPortal:
		return portalDisplays()
	case "":
		return nil, fmt.Errorf("display: no graphical session (neither WAYLAND_DISPLAY nor DISPLAY is set)")
	default:
		return nil, fmt.Errorf("display: unknown capture backend %q", b)
	}
}

// Open captures the display with this ID; empty opens the primary one.
// Frames are capture.DMABuf surfaces on no GPU device: the encoder
// uploads or imports them.
func Open(id string, opts Options) (*Source, error) {
	switch b := Backend(); b {
	case BackendX11:
		// Rootless XWayland composes no root image, so every frame would
		// be black; a forced backend is someone's test.
		if os.Getenv("PLANCK_CAPTURE_BACKEND") == "" && x11.XWayland() {
			return nil, fmt.Errorf("display: the X server is XWayland, which keeps no root image; capture through the desktop portal")
		}
		src, err := x11.Open(id, opts.FPS)
		if err != nil {
			return nil, err
		}
		return &Source{Source: src, Display: src.Display()}, nil
	case BackendPortal:
		return openPortal(id, opts)
	case "":
		return nil, fmt.Errorf("display: no graphical session (neither WAYLAND_DISPLAY nor DISPLAY is set)")
	default:
		return nil, fmt.Errorf("display: unknown capture backend %q", b)
	}
}
