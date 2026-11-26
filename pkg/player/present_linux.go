//go:build linux && !android && (amd64 || arm64)

package player

import (
	"fmt"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/linuxdec"
	"github.com/planckdotgg/planck/pkg/encode"
)

// openDecoder makes a VA-API or NVDEC decoder. VA-API's pictures go to
// the surface as DMA-BUFs where its compositor takes them, else into
// memory, as NVDEC's always do.
func openDecoder(surf *gpusurface.Surface, cfg decode.Config) (decode.Decoder, error) {
	if cfg.Codec == encode.PyroWave {
		return nil, fmt.Errorf("%w: no PyroWave decoder on Linux yet", decode.ErrUnsupported)
	}
	t := decode.Target{}
	if surf.Wayland() {
		t.Accepts = surf.Accepts
	}
	return linuxdec.Open(t, cfg)
}

func present(surf *gpusurface.Surface, f decode.Frame) error {
	return surf.PresentTagged(f.Surface, f.PTS)
}

// watchPresents hands fn the time each picture reached the screen, where
// the compositor tells (wp_presentation), for PaceSmooth.
func watchPresents(surf *gpusurface.Surface, fn func(pts, submitted, gpuDone, presented int64)) {
	surf.OnPresented(func(pts, submitted, presented int64) { fn(pts, submitted, 0, presented) })
}

func screenNow() int64 { return gpusurface.Now() }
