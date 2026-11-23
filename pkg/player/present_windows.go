//go:build windows

package player

import (
	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/d3d11"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func openDecoder(surf *gpusurface.Surface, cfg decode.Config) (decode.Decoder, error) {
	d, err := d3d11.Open(gpu.Device{Adapter: gpu.Adapter{Name: surf.AdapterName()}, Handle: surf.Device()}, cfg)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func present(surf *gpusurface.Surface, f decode.Frame) error {
	return surf.PresentSlice(f.Surface, f.Slice, f.Width, f.Height)
}
