//go:build windows

package vt

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
	"github.com/watzon/planck/pkg/encode/nvenc"
)

// TestWriteAV1Clip regenerates testdata/av1_640x360.tus, the AV1 clip the
// macOS decoder test plays (Apple silicon decodes AV1 but has no AV1
// encoder). Run with PLANCK_WRITE_TESTDATA=1.
func TestWriteAV1Clip(t *testing.T) {
	if os.Getenv("PLANCK_WRITE_TESTDATA") == "" {
		t.Skip("set PLANCK_WRITE_TESTDATA=1 to regenerate")
	}
	d := encodetest.Device(t, "NVIDIA")
	cfg := encode.Config{Codec: encode.AV1, Chroma: encode.P010, Source: capture.FormatP010, Width: 640, Height: 360, FPS: 60, BitrateKbps: 1500}
	e, err := nvenc.Open(d.D3D, d.Info, cfg, nvenc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	frames := encodetest.Frames(t, d, cfg.Source, cfg.Width, cfg.Height, 10)
	var file []byte
	for i := range frames {
		p, err := e.Encode(frames[i], i == 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		file = binary.BigEndian.AppendUint32(file, uint32(len(p.Data)))
		file = append(file, p.Data...)
	}
	if err := os.WriteFile("testdata/av1_640x360.tus", file, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes", len(file))
}
