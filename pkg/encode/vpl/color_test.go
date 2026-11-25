package vpl

import (
	"encoding/binary"
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

func TestVideoSignalInfo(t *testing.T) {
	b := videoSignalInfo(encode.HDR10Color)
	le := binary.LittleEndian
	if id := le.Uint32(b); id != 'V'|'S'<<8|'I'<<16|'N'<<24 {
		t.Errorf("BufferId %#x, want VSIN", id)
	}
	if n := le.Uint32(b[4:]); n != uint32(len(b)) || n != 20 {
		t.Errorf("BufferSz %d for %d bytes", n, len(b))
	}
	for _, f := range []struct {
		name      string
		off, want int
	}{{"VideoFormat", 8, 5}, {"VideoFullRange", 10, 0}, {"ColourDescriptionPresent", 12, 1}, {"ColourPrimaries", 14, 9}, {"TransferCharacteristics", 16, 16}, {"MatrixCoefficients", 18, 9}} {
		if got := int(le.Uint16(b[f.off:])); got != f.want {
			t.Errorf("%s = %d, want %d", f.name, got, f.want)
		}
	}
}
