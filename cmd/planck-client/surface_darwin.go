package main

import (
	"math"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"
	mui "github.com/egoist/mygo/ui"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/defaults"
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

// newSurfaceHost runs on the main thread. An hdr10 surface presents the
// stream's BT.2020 PQ pictures as HDR.
func newSurfaceHost(w *mygo.Window, hdr10 bool) (*surfaceHost, error) {
	surf, err := gpusurface.New(gpusurface.Options{Window: w.NativeHandle(), Composite: true, HDR10: hdr10})
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

// picture draws nothing: the surface shows the frames itself.
func (h *surfaceHost) picture(*mui.Context) {}

// windowPixels is the window's content size in backing pixels. It runs on
// the main thread.
func windowPixels(w *mygo.Window) (int, int) {
	cw, ch := w.ContentSize()
	scale := objc.Send[float64](objc.ID(w.NativeHandle()), objc.RegisterName("backingScaleFactor"))
	if scale <= 0 {
		scale = 1
	}
	return int(math.Round(float64(cw) * scale)), int(math.Round(float64(ch) * scale))
}

// windowDisplay is the display w is on. Main thread.
func windowDisplay(w *mygo.Window) defaults.ClientDisplay {
	return session.WindowDisplay(w.NativeHandle())
}

func localEncoders() []ui.EncoderInfo {
	return []ui.EncoderInfo{{GPU: "Apple GPU", API: "VideoToolbox"}}
}
