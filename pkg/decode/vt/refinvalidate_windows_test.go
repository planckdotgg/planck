package vt

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/encodetest"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
)

// TestWriteRefInvalidationClip regenerates testdata/hevc_refinv_640x360.bin,
// the NVENC stream TestDecodeAfterReferenceInvalidation plays on macOS:
// frames 20 to 22 are left out as lost, and before frame 23 the encoder
// invalidates them. Each record is the source frame's index and the access
// unit's length, both big-endian uint32, then the access unit. Run with
// PLANCK_WRITE_TESTDATA=1.
func TestWriteRefInvalidationClip(t *testing.T) {
	if os.Getenv("PLANCK_WRITE_TESTDATA") == "" {
		t.Skip("set PLANCK_WRITE_TESTDATA=1 to regenerate")
	}
	d := encodetest.Device(t, "NVIDIA")
	cfg := encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: capture.FormatNV12, Width: 640, Height: 360, FPS: 60, BitrateKbps: 1500}
	e, err := nvenc.Open(d.D3D, d.Info, cfg, nvenc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 48)
	var file []byte
	for i := range frames {
		if i == 23 {
			for lost := 20; lost <= 22; lost++ {
				if err := e.InvalidateRefs(int64(lost)); err != nil {
					t.Fatal(err)
				}
			}
		}
		p, err := e.Encode(frames[i], i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.Keyframe != (i == 0) {
			t.Fatalf("frame %d: keyframe %v", i, p.Keyframe)
		}
		if i >= 20 && i <= 22 {
			continue
		}
		file = binary.BigEndian.AppendUint32(file, uint32(i))
		file = binary.BigEndian.AppendUint32(file, uint32(len(p.Data)))
		file = append(file, p.Data...)
	}
	if err := os.WriteFile("testdata/hevc_refinv_640x360.bin", file, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes", len(file))
}
