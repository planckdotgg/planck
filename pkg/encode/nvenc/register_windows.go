//go:build windows

package nvenc

import (
	"os"
	"strconv"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func init() {
	encode.Register(encode.Backend{Name: "NVENC", Vendor: gpu.VendorNVIDIA, Open: func(dev gpu.Device, cfg encode.Config) (encode.Encoder, error) {
		e, err := Open(win.Device{Object: win.ObjectAt(dev.Handle)}, dev.Adapter, cfg, Options{SplitMode: splitFromEnv()})
		if err != nil {
			return nil, err
		}
		return e, nil
	}})
}

// splitFromEnv reads PLANCK_NVENC_SPLIT, an audit experiment: 2 forces
// two-engine split-frame encode, 15 turns it off, unset leaves NVENC's auto.
func splitFromEnv() uint32 {
	v, _ := strconv.ParseUint(os.Getenv("PLANCK_NVENC_SPLIT"), 10, 32)
	return uint32(v)
}
