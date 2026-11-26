//go:build linux && !android && (amd64 || arm64)

package nvenc

import (
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func init() {
	encode.Register(encode.Backend{Name: "NVENC", Vendor: gpu.VendorNVIDIA, Open: func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
		e, err := Open(dev.Adapter, cfg, Options{})
		if err != nil {
			return nil, err
		}
		return e, nil
	}})
}
