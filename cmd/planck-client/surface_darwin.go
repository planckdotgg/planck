package main

import (
	"errors"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/player"
)

// composeUnderUI: on macOS the video layer goes under MyGo's in the same
// content view, so the window stays opaque.
const composeUnderUI = false

// surfaceHost is a gpusurface whose CAMetalLayer sits at the back of the
// window's content view, under the layer MyGo draws the UI in, which shows
// it where the UI paints nothing.
type surfaceHost struct {
	surf  *gpusurface.Surface
	scale float64
}

func send(obj objc.ID, sel string, args ...any) objc.ID {
	return obj.Send(objc.RegisterName(sel), args...)
}

// newSurfaceHost runs on the main thread.
func newSurfaceHost(w *mygo.Window) (*surfaceHost, error) {
	win := w.NativeHandle()
	surf, err := gpusurface.New(gpusurface.Options{Window: win})
	if err != nil {
		return nil, err
	}
	parent := send(send(objc.ID(win), "contentView"), "layer")
	layer := send(send(parent, "sublayers"), "lastObject")
	if layer == 0 || !objc.Send[bool](layer, objc.RegisterName("isKindOfClass:"), objc.GetClass("CAMetalLayer")) {
		surf.Close()
		return nil, errors.New("gpusurface's layer is not the content view's last")
	}
	send(parent, "insertSublayer:atIndex:", layer, uint32(0))
	h := &surfaceHost{surf: surf, scale: 1}
	if cw, _ := w.ContentSize(); cw > 0 {
		pw, _ := surf.Size()
		h.scale = float64(pw) / float64(cw)
	}
	return h, nil
}

// fit has nothing to do on macOS: the presenting thread resizes the layer.
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
