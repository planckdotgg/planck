//go:build windows

package all

import (
	"fmt"
	"testing"

	"github.com/watzon/planck/pkg/capture"
	"github.com/watzon/planck/pkg/capture/display"
	"github.com/watzon/planck/pkg/encode"
	"github.com/watzon/planck/pkg/encode/encodetest"
	"github.com/watzon/planck/pkg/encode/nvenc"
	"github.com/watzon/planck/pkg/gpu"
)

func cfg() encode.Config {
	return encode.Config{Codec: encode.HEVC, Chroma: encode.NV12, Source: capture.FormatBGRA8, Width: 1280, Height: 720, FPS: 60, BitrateKbps: 10000}
}

// TestOpenMatchesVendor opens the default encoder on each GPU of the machine
// and checks it is that vendor's, encoding a frame there.
func TestOpenMatchesVendor(t *testing.T) {
	want := map[gpu.Vendor]string{gpu.VendorNVIDIA: "*nvenc.Encoder", gpu.VendorAMD: "*amf.Encoder", gpu.VendorIntel: "*vpl.Encoder"}
	for _, match := range []string{"NVIDIA", "Radeon", "UHD"} {
		t.Run(match, func(t *testing.T) {
			d := encodetest.Device(t, match)
			enc, err := encode.Open(cfg(), encodetest.GPU(d))
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Close()
			if got := fmt.Sprintf("%T", enc); got != want[d.Info.Vendor] {
				t.Fatalf("%s: got %s, want %s", d.Info.Name, got, want[d.Info.Vendor])
			}
			frames := encodetest.Frames(t, d, capture.FormatBGRA8, 1280, 720, 2)
			if pkt, err := enc.Encode(frames[0], true, nil); err != nil || !pkt.Keyframe {
				t.Fatalf("first frame: keyframe=%v err=%v", pkt.Keyframe, err)
			}
		})
	}
}

// TestDisplayToEncoder runs the zero-copy host path on the real desktop:
// capture the primary display, encode on the same GPU.
func TestDisplayToEncoder(t *testing.T) {
	src, err := display.Open("", display.Options{})
	if err != nil {
		t.Skipf("no display: %v", err)
	}
	defer src.Close()
	c := cfg()
	c.Width, c.Height, c.FPS = src.Display.Width, src.Display.Height, int(src.Display.Refresh)
	enc, err := encode.Open(c, src.Device)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	t.Logf("%s %dx%d@%.0f on %s -> %T", src.Display.ID, src.Display.Width, src.Display.Height, src.Display.Refresh, src.Device.Adapter.Name, enc)
	got := 0
	for i := 0; i < 30 && got == 0; i++ {
		f, err := src.Next(100e6)
		if err != nil {
			continue
		}
		pkt, err := enc.Encode(f, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(pkt.Data) > 0 && pkt.Keyframe {
			got++
		}
	}
	if got == 0 {
		t.Skip("desktop produced no frame in 3 s (static screen)")
	}
}

// TestOverrideNeedsThatDevice: an override to another adapter encodes there.
func TestOverrideNeedsThatDevice(t *testing.T) {
	amd := encodetest.Device(t, "Radeon")
	nv := encodetest.Device(t, "NVIDIA")
	c := cfg()
	c.Adapter = nv.Info.LUID
	enc, err := encode.Open(c, encodetest.GPU(amd), encodetest.GPU(nv))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	if _, ok := enc.(*nvenc.Encoder); !ok {
		t.Fatalf("override to the RTX 4080 opened %T", enc)
	}
}
