package nvenc

import (
	"encoding/binary"

	"github.com/planckdotgg/planck/pkg/encode"
)

// putHEVCColor writes d into an NV_ENC_CONFIG_HEVC_VUI_PARAMETERS: video
// range, the format unspecified, and the colour description present.
func putHEVCColor(vui []byte, d encode.ColorDescription) {
	le := binary.LittleEndian
	le.PutUint32(vui[vuiSignalTypePresent:], 1)
	le.PutUint32(vui[vuiVideoFormat:], vuiVideoFormatUnspec)
	le.PutUint32(vui[vuiColourDescPresent:], 1)
	le.PutUint32(vui[vuiColourPrimaries:], uint32(d.Primaries))
	le.PutUint32(vui[vuiTransfer:], uint32(d.Transfer))
	le.PutUint32(vui[vuiColourMatrix:], uint32(d.Matrix))
}

// putAV1Color writes d into an NV_ENC_CONFIG_AV1, at studio range.
func putAV1Color(av1 []byte, d encode.ColorDescription) {
	le := binary.LittleEndian
	le.PutUint32(av1[av1ColorPrimaries:], uint32(d.Primaries))
	le.PutUint32(av1[av1Transfer:], uint32(d.Transfer))
	le.PutUint32(av1[av1Matrix:], uint32(d.Matrix))
	le.PutUint32(av1[av1ColorRange:], 0)
}
