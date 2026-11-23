//go:build windows

package vpl

import (
	"github.com/watzon/planck/internal/win"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/gpu"
)

func init() {
	encode.Register(encode.Backend{Name: "oneVPL", Vendor: gpu.VendorIntel, Open: func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
		e, err := Open(win.Device{Object: win.ObjectAt(dev.Handle)}, dev.Adapter, cfg)
		if err != nil {
			return nil, err
		}
		return e, nil
	}})
}
