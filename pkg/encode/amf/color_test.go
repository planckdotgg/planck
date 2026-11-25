package amf

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/encode"
)

func TestHDRProps(t *testing.T) {
	want := map[string]int64{
		"HevcInColorProfile": 2, "HevcOutColorProfile": 2,
		"HevcInColorTransferChar": 16, "HevcOutColorTransferChar": 16,
		"HevcInColorPrimaries": 9, "HevcOutColorPrimaries": 9,
	}
	got := hdrProps(encode.HDR10Color)
	if len(got) != len(want) {
		t.Fatalf("%d properties, want %d", len(got), len(want))
	}
	for _, p := range got {
		if v, ok := want[p.name]; !ok || v != p.value {
			t.Errorf("%s = %d, want %d", p.name, p.value, want[p.name])
		}
	}
}
