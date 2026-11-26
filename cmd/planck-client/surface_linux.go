//go:build linux && !android && (amd64 || arm64)

package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"
	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode/all"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/player"
)

// surfaceHost is a gpusurface on the window: a subsurface under the
// toplevel on Wayland, or the window's own content in picture mode, where
// the stream view draws the frames (picture).
type surfaceHost struct {
	surf *gpusurface.Surface
}

// newSurfaceHost runs on the main thread. It presents SDR only.
func newSurfaceHost(w *mygo.Window, _ bool) (*surfaceHost, error) {
	surf, err := gpusurface.New(gpusurface.Options{
		Window: w.NativeHandle(),
		// Picture mode: a new frame redraws the window, whose stream view
		// takes it.
		Redraw: func() { w.Update(func() {}) },
	})
	if err != nil {
		return nil, err
	}
	return &surfaceHost{surf: surf}, nil
}

// fit has the surface read the content's new place. Main thread.
func (h *surfaceHost) fit() { h.surf.Layout() }

func (h *surfaceHost) resizeSurface(p *player.Player, _ *videoView) error { return p.Resize(0, 0) }

func (h *surfaceHost) close() { h.surf.Close() }

// picture draws the newest frame under the overlay, fitted, in picture
// mode; on Wayland the subsurface shows them and it draws nothing.
func (h *surfaceHost) picture(c *mui.Context) {
	b := h.surf.Bitmap()
	if b == nil {
		return
	}
	h.surf.Refresh()
	mui.Image(c, b).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(mui.Contain).
		Background(mui.RGBA(0, 0, 0, 1)).IgnoreInput(true)
}

// localEncoders lists the GPUs with an encoder backend, by name.
func localEncoders() []ui.EncoderInfo {
	var out []ui.EncoderInfo
	seen := map[string]bool{}
	for _, d := range all.Devices() {
		api := defaults.APIVAAPI
		if d.Adapter.Vendor == gpu.VendorNVIDIA {
			api = defaults.APINVENC
		}
		if seen[d.Adapter.Name+api] {
			continue
		}
		seen[d.Adapter.Name+api] = true
		out = append(out, ui.EncoderInfo{GPU: d.Adapter.Name, API: api})
	}
	return out
}
