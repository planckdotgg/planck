//go:build windows

package player

import (
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/d3d11"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
	_ "github.com/planckdotgg/planck/pkg/pyrowave"
)

func openDecoder(surf *gpusurface.Surface, cfg decode.Config) (decode.Decoder, error) {
	if cfg.Codec == encode.PyroWave {
		return decode.Open(decode.Target{Device: surf.Device(), Adapter: gpu.Adapter{Name: surf.AdapterName()}}, cfg)
	}
	d, err := d3d11.Open(gpu.Device{Adapter: gpu.Adapter{Name: surf.AdapterName()}, Handle: surf.Device()}, cfg)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func present(surf *gpusurface.Surface, f decode.Frame) error {
	return surf.PresentSlice(f.Surface, f.Slice, f.Width, f.Height)
}

// observeGlass has nothing to report: the surface cannot tell when a
// frame reached the screen.
// watchPresents: no screen times here, so PaceSmooth is PaceLowestLatency.
func watchPresents(*gpusurface.Surface, func(pts, submitted, gpuDone, presented int64)) {}

func screenNow() int64 { return monoNow() }

func nominalPeriod(*gpusurface.Surface) int64 { return 0 }
