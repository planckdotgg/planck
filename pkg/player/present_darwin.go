//go:build darwin

package player

import (
	"time"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/vt"
)

// openDecoder makes a VideoToolbox decoder: its IOSurfaces are shared with
// the surface's Metal device without a copy.
func openDecoder(_ *gpusurface.Surface, cfg decode.Config) (decode.Decoder, error) {
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
