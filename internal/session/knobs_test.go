package session

import (
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func TestKnobsOverrides(t *testing.T) {
	o, err := Knobs{Codec: "PyroWave", Chroma: "yuv444p10", Resolution: "1662x1080", FPS: "120", Slices: "4",
		VDD: "on", VDDGPU: "4080", Split: "off", Direct: "on", VSync: "auto", Route: "lan", BitrateMbps: 400}.Overrides()
	if err != nil {
		t.Fatal(err)
	}
	want := defaults.UserOverrides{Codec: "pyrowave", Chroma: "444", BitDepth: "10", Resolution: "custom", CustomWidth: 1662,
		CustomHeight: 1080, FPS: "120", BitrateMbps: 400, VirtualDisplay: "on", VirtualDisplayGPU: "4080", SubFrameSlices: "4",
		NVENC2EngineSplit: "off", MetalDirectDisplay: "on", VSyncMode: "auto", PhaseLock: "auto", RoutePolicy: "lan", HDR10: "auto"}
	if o != want {
		t.Fatalf("got %+v\nwant %+v", o, want)
	}
	if o, _ := (Knobs{Chroma: "420", BitDepth: "auto"}).Overrides(); o.BitDepth != defaults.Auto {
		t.Fatalf("-chroma 420 set the depth to %q", o.BitDepth)
	}
	if o, _ := (Knobs{Resolution: "4K"}).Overrides(); o.Resolution != "4k" {
		t.Fatalf("4K: %q", o.Resolution)
	}
	for _, k := range []Knobs{{Codec: "vp9"}, {Chroma: "422"}, {Resolution: "wide"}, {Resolution: "641x360"}, {FPS: "90"},
		{Slices: "3"}, {VDD: "maybe"}, {Route: "derp"}, {BitrateMbps: -1}} {
		if _, err := k.Overrides(); err == nil {
			t.Errorf("%+v accepted", k)
		}
	}
}
