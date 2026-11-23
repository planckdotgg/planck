//go:build darwin

package vt

import (
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/gpu"
)

func init() {
	encode.Register(encode.Backend{Name: "VideoToolbox", Vendor: gpu.VendorApple, Open: func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
		e, err := Open(cfg)
		if err != nil {
			return nil, err
		}
		return e, nil
	}})
}
