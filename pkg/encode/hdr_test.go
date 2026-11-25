package encode

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
)

func TestValidateHDR10(t *testing.T) {
	base := Config{Codec: HEVC, Chroma: P010, Source: capture.FormatRGB10A2, Width: 3840, Height: 2160, FPS: 120, BitrateKbps: 80_000, HDR10: true}
	for _, c := range []Config{base, func() Config { c := base; c.Codec = AV1; return c }()} {
		if err := c.Validate(); err != nil {
			t.Errorf("%s %s HDR10: %v", c.Codec, c.Chroma, err)
		}
	}
	for _, c := range []Config{
		func() Config { c := base; c.Chroma = NV12; return c }(),
		func() Config { c := base; c.Codec = H264; return c }(),
	} {
		if c.Validate() == nil {
			t.Errorf("%s %s HDR10 accepted", c.Codec, c.Chroma)
		}
	}
	if (HDR10Color != ColorDescription{Primaries: 9, Transfer: 16, Matrix: 9}) {
		t.Errorf("HDR10Color %+v: want BT.2020 (9), PQ (16), BT.2020nc (9)", HDR10Color)
	}
}
