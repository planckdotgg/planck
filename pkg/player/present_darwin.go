//go:build darwin

package player

import (
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

// watchPresents hands fn the time each picture reached the screen, the
// refresh times PaceSmooth follows.
func watchPresents(surf *gpusurface.Surface, fn func(pts, submitted, gpuDone, presented int64)) {
	surf.OnPresentedGPU(fn)
}

func screenNow() int64 { return gpusurface.Now() }

// nominalPeriod is the refresh period the system states for the surface's
// screen.
func nominalPeriod(surf *gpusurface.Surface) int64 { return int64(surf.RefreshPeriod()) }
