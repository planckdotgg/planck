package linuxdec

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/nvdec"
	"github.com/planckdotgg/planck/pkg/decode/vaapi"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
)

// Open opens the best hardware decoder for cfg on this machine. VA-API
// comes first where t takes its DMA-BUFs (no copy at all), unless its
// driver is NVIDIA's VA-API shim; NVDEC first where pictures go to memory
// anyway, its copy being the cheaper one. AV1 decodes on NVDEC only.
func Open(t decode.Target, cfg decode.Config) (decode.Decoder, error) {
	var errs []error
	tryNVDEC := func() decode.Decoder {
		d, err := nvdec.Open(cfg)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		return d
	}
	switch cfg.Codec {
	case encode.AV1:
		if d := tryNVDEC(); d != nil {
			return d, nil
		}
	case encode.HEVC:
		if t.Accepts == nil {
			if d := tryNVDEC(); d != nil {
				return d, nil
			}
		}
		v, err := vaapi.OpenHEVC(t, cfg)
		if err == nil {
			if !nvidiaShim(v.Vendor()) || t.Accepts == nil {
				return v, nil
			}
			if d := tryNVDEC(); d != nil {
				v.Close()
				return d, nil
			}
			return v, nil
		}
		errs = append(errs, err)
		if t.Accepts != nil {
			if d := tryNVDEC(); d != nil {
				return d, nil
			}
		}
	default:
		return nil, fmt.Errorf("%w: %s", decode.ErrUnsupported, cfg.Codec)
	}
	return nil, fmt.Errorf("%w: no hardware %s decoder (%v)", decode.ErrUnsupported, cfg.Codec, errors.Join(errs...))
}

func nvidiaShim(vendor string) bool {
	v := strings.ToLower(vendor)
	return strings.Contains(v, "nvdec") || strings.Contains(v, "nvidia")
}

var probed struct {
	once sync.Once
	caps []defaults.CodecCap
}

// Codecs lists the codecs a decoder opens for, probed once.
func Codecs() []defaults.CodecCap {
	probed.once.Do(func() {
		nv := nvdec.Available() == nil
		hevc, hevc10 := nv, nv
		if !nv {
			hevc, hevc10 = vaapi.HEVCProfiles()
		}
		if hevc {
			probed.caps = append(probed.caps, defaults.CodecCap{Codec: defaults.CodecHEVC, TenBit: hevc10})
		}
		if nv {
			probed.caps = append(probed.caps, defaults.CodecCap{Codec: defaults.CodecAV1, TenBit: true})
		}
	})
	return probed.caps
}
