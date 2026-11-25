package vpl

import (
	"encoding/binary"

	"github.com/planckdotgg/planck/pkg/encode"
)

// mfxExtVideoSignalInfo (mfxstructures.h): the mfxExtBuffer header, then
// six mfxU16 fields.
const (
	extVSI            = 1313428310 // MFX_MAKEFOURCC('V','S','I','N')
	sizeExtVSI        = 20
	vsiVideoFormat    = 8
	vsiFullRange      = 10
	vsiColourDesc     = 12
	vsiPrimaries      = 14
	vsiTransfer       = 16
	vsiMatrix         = 18
	videoFormatUnspec = 5
)

// videoSignalInfo is the extension buffer that signals d at video range.
func videoSignalInfo(d encode.ColorDescription) []byte {
	b := make([]byte, sizeExtVSI)
	le := binary.LittleEndian
	le.PutUint32(b[0:], extVSI)
	le.PutUint32(b[4:], sizeExtVSI)
	le.PutUint16(b[vsiVideoFormat:], videoFormatUnspec)
	le.PutUint16(b[vsiFullRange:], 0)
	le.PutUint16(b[vsiColourDesc:], 1)
	le.PutUint16(b[vsiPrimaries:], uint16(d.Primaries))
	le.PutUint16(b[vsiTransfer:], uint16(d.Transfer))
	le.PutUint16(b[vsiMatrix:], uint16(d.Matrix))
	return b
}
