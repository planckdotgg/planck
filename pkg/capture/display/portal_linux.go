//go:build linux && !android && (amd64 || arm64)

package display

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/pipewire"
)

// portalDisplays is the one display a ScreenCast session streams: the
// user picks the monitor in the portal's dialog, so its size here is the
// first connected monitor's preferred mode until the stream says.
func portalDisplays() ([]capture.Display, error) {
	if err := pipewire.Available(); err != nil {
		return nil, err
	}
	d := capture.Display{ID: "portal", Name: "Screen shared through the desktop portal", Width: 1920, Height: 1080, Refresh: 60, Primary: true}
	if w, h, ok := connectedMode("/sys/class/drm"); ok {
		d.Width, d.Height = w, h
	}
	return []capture.Display{d}, nil
}

// connectedMode is the preferred mode, the first line of modes, of the
// first connected DRM connector under sysDir.
func connectedMode(sysDir string) (w, h int, ok bool) {
	conns, _ := filepath.Glob(filepath.Join(sysDir, "card*-*"))
	sort.Strings(conns)
	for _, c := range conns {
		if st, err := os.ReadFile(filepath.Join(c, "status")); err != nil || strings.TrimSpace(string(st)) != "connected" {
			continue
		}
		modes, err := os.ReadFile(filepath.Join(c, "modes"))
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(modes), "\n")
		ws, hs, found := strings.Cut(strings.TrimRight(first, "i"), "x")
		w, werr := strconv.Atoi(ws)
		h, herr := strconv.Atoi(hs)
		if found && werr == nil && herr == nil && w > 0 && h > 0 {
			return w, h, true
		}
	}
	return 0, 0, false
}

func openPortal(_ string, opts Options) (*Source, error) {
	src, err := pipewire.Open(opts.FPS)
	if err != nil {
		return nil, err
	}
	return &Source{Source: src, Display: src.Display()}, nil
}
