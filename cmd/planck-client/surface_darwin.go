package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/player"
)

// composeUnderUI: on macOS the video's view goes under MyGo's in the same
// content view, so the window stays opaque.
const composeUnderUI = false

// surfaceHost is a gpusurface composed under the view MyGo draws the UI
// in, which shows it where the UI paints nothing.
type surfaceHost struct {
	surf  *gpusurface.Surface
	scale float64
}

// newSurfaceHost runs on the main thread.
func newSurfaceHost(w *mygo.Window) (*surfaceHost, error) {
	surf, err := gpusurface.New(gpusurface.Options{Window: w.NativeHandle(), Composite: true})
	if err != nil {
		return nil, err
	}
	h := &surfaceHost{surf: surf, scale: 1}
	if cw, _ := w.ContentSize(); cw > 0 {
		pw, _ := surf.Size()
		h.scale = float64(pw) / float64(cw)
	}
	return h, nil
}

// fit has nothing to do on macOS: the surface's view follows the window,
// and the presenting thread resizes the drawables.
func (h *surfaceHost) fit() {}

func (h *surfaceHost) resizeSurface(p *player.Player, v *videoView) error {
	cw, ch := v.contentSize()
	if cw <= 0 || ch <= 0 {
		return nil
	}
	return p.Resize(int(float64(cw)*h.scale+0.5), int(float64(ch)*h.scale+0.5))
}

func (h *surfaceHost) close() { h.surf.Close() }

func localEncoders() []ui.EncoderInfo {
	return []ui.EncoderInfo{{GPU: "Apple GPU", API: "VideoToolbox"}}
}
