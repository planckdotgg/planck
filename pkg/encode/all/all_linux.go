//go:build linux && !android && (amd64 || arm64)

package all

import (
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
	"github.com/planckdotgg/planck/pkg/encode/vaapi"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// Devices lists the GPUs an encoder backend opens on: the CUDA devices
// NVENC drives, then the AMD and Intel render nodes whose VA-API driver
// encodes HEVC. Their Handle is 0: each encoder opens its own context on
// the adapter.
func Devices() []gpu.Device {
	var out []gpu.Device
	if nv, err := nvenc.Adapters(); err == nil {
		for _, a := range nv {
			out = append(out, gpu.Device{Adapter: a})
		}
	}
	if va, err := vaapi.Adapters(); err == nil {
		for _, a := range va {
			if a.Vendor == gpu.VendorAMD || a.Vendor == gpu.VendorIntel {
				out = append(out, gpu.Device{Adapter: a})
			}
		}
	}
	return out
}
