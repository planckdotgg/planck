package amf

import "github.com/planckdotgg/planck/pkg/encode"

// colorProfile2020 is AMF_VIDEO_CONVERTER_COLOR_PROFILE_2020: the BT.2020
// matrix at studio range (ColorSpace.h).
const colorProfile2020 = 2

// intProp is an amf_int64 encoder property.
type intProp struct {
	name  string
	value int64
}

// hdrProps describe a BT.2020 PQ input and output, which AMF converts with
// the BT.2020 matrix and signals in the VUI (VideoEncoderHEVC.h).
func hdrProps(d encode.ColorDescription) []intProp {
	return []intProp{
		{"HevcInColorProfile", colorProfile2020},
		{"HevcInColorTransferChar", int64(d.Transfer)},
		{"HevcInColorPrimaries", int64(d.Primaries)},
		{"HevcOutColorProfile", colorProfile2020},
		{"HevcOutColorTransferChar", int64(d.Transfer)},
		{"HevcOutColorPrimaries", int64(d.Primaries)},
	}
}
