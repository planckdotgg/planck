//go:build darwin

package player

import (
	"time"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/vt"
	"github.com/planckdotgg/planck/pkg/encode"
	_ "github.com/planckdotgg/planck/pkg/pyrowave"
)

// openDecoder makes a VideoToolbox decoder, whose IOSurfaces are shared
// with the surface's Metal device without a copy, or for PyroWave a compute
// decoder on the surface's device and queue.
func openDecoder(surf *gpusurface.Surface, cfg decode.Config) (decode.Decoder, error) {
	if cfg.Codec == encode.PyroWave {
		return decode.Open(decode.Target{Device: surf.Device(), Queue: surf.Queue()}, cfg)
	}
	d, err := vt.Open(cfg)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func present(surf *gpusurface.Surface, f decode.Frame) error {
	return surf.PresentTagged(f.Surface, f.PTS)
}

func observeGlass(surf *gpusurface.Surface, obs Observer) {
	surf.OnPresented(func(pts, submitted, presented int64) {
		if presented == 0 {
			return
		}
		obs.Glass(pts, time.Duration(presented-submitted), time.Duration(gpusurface.Now()-presented))
	})
}
