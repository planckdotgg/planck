//go:build linux && !android && (amd64 || arm64)

package all

import (
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Devices lists the GPUs an encoder backend opens on: the CUDA devices
// NVENC drives. Their Handle is 0: each encoder opens its own context on
// the adapter.
func Devices() []gpu.Device {
	var out []gpu.Device
	if nv, err := nvenc.Adapters(); err == nil {
		for _, a := range nv {
			out = append(out, gpu.Device{Adapter: a})
		}
	}
	return out
}
