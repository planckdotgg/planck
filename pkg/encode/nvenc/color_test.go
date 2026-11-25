package nvenc

import (
	"encoding/binary"
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

// TestHDR10Color: the HDR10 code points land where nvEncodeAPI.h 13.1
// puts colourPrimaries, transferCharacteristics and colourMatrix.
func TestHDR10Color(t *testing.T) {
	u32 := func(b []byte, o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	hevc := make([]byte, 1024)
	putHEVCColor(hevc[hevcVUI:], encode.HDR10Color)
	for _, f := range []struct {
		name string
		off  int
		want uint32
	}{
		{"videoSignalTypePresentFlag", 72, 1}, {"videoFormat", 76, 5}, {"videoFullRangeFlag", 80, 0},
		{"colourDescriptionPresentFlag", 84, 1}, {"colourPrimaries", 88, 9}, {"transferCharacteristics", 92, 16}, {"colourMatrix", 96, 9},
	} {
		if got := u32(hevc, f.off); got != f.want {
			t.Errorf("HEVC %s at %d = %d, want %d", f.name, f.off, got, f.want)
		}
	}
	av1 := make([]byte, 1024)
	putAV1Color(av1, encode.HDR10Color)
	if p, tr, m := u32(av1, 68), u32(av1, 72), u32(av1, 76); p != 9 || tr != 16 || m != 9 {
		t.Errorf("AV1 colour %d/%d/%d, want 9/16/9", p, tr, m)
	}
}
