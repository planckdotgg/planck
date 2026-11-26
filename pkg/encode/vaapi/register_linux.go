//go:build linux && !android && (amd64 || arm64)

package vaapi

import (
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func init() {
	open := func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
		e, err := Open(dev.Adapter, cfg)
		if err != nil {
			return nil, err
		}
		return e, nil
	}
	encode.Register(encode.Backend{Name: "VA-API", Vendor: gpu.VendorAMD, Open: open})
	encode.Register(encode.Backend{Name: "VA-API", Vendor: gpu.VendorIntel, Open: open})
}
