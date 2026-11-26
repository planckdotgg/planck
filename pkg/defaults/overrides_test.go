package defaults

import (
	"encoding/json"
	"testing"
)

func TestMergeTopNonAutoWins(t *testing.T) {
	base := UserOverrides{Codec: "hevc", FPS: "120", Resolution: "custom", CustomWidth: 2560, CustomHeight: 1600, BitrateMbps: 80, VirtualDisplayGPU: "4080", VSyncMode: "on"}
	top := UserOverrides{Codec: "auto", FPS: "60", Resolution: "1440p", VirtualDisplayGPU: "", VSyncMode: ""}
	got := Merge(base, top)
	want := UserOverrides{Codec: "hevc", FPS: "60", Resolution: "1440p", BitrateMbps: 80, VirtualDisplayGPU: "4080", VSyncMode: "on"}
	if got != want {
		t.Fatalf("Merge\n got %+v\nwant %+v", got, want)
	}
	if got := Merge(base, UserOverrides{BitrateMbps: 20, VirtualDisplayGPU: "Radeon"}); got.BitrateMbps != 20 || got.VirtualDisplayGPU != "Radeon" || got.Resolution != "custom" || got.CustomWidth != 2560 {
		t.Fatalf("Merge numbers: %+v", got)
	}
}

func TestNormalize(t *testing.T) {
	n := UserOverrides{FPS: "120"}.Normalize()
	if n.Codec != "auto" || n.FPS != "120" || n.RoutePolicy != "auto" || n.VirtualDisplayGPU != "" {
		t.Fatalf("Normalize = %+v", n)
	}
	b, _ := json.Marshal(UserOverrides{})
	if string(b) != "{}" {
		t.Fatalf("empty overrides marshal to %s", b)
	}
}

func TestValidate(t *testing.T) {
	good := []UserOverrides{
		{},
		UserOverrides{}.Normalize(),
		{Codec: "pyrowave", Chroma: "444", BitDepth: "10", FPS: "240", SubFrameSlices: "8", RoutePolicy: "tailscale", BitrateMbps: 3000},
		{Resolution: "custom", CustomWidth: 640, CustomHeight: 360},
		{Resolution: "custom", CustomWidth: 7680, CustomHeight: 4320},
	}
	for _, o := range good {
		if err := o.Validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
	bad := []UserOverrides{
		{Codec: "vp9"},
		{Chroma: "422"},
		{FPS: "90"},
		{VSyncMode: "yes"},
		{Resolution: "custom", CustomWidth: 641, CustomHeight: 480},
		{Resolution: "custom", CustomWidth: 7682, CustomHeight: 480},
		{Resolution: "custom"},
		{BitrateMbps: -1},
		{BitrateMbps: 3001},
	}
	for _, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("%+v: accepted", o)
		}
	}
}

func TestPhaseLockOverride(t *testing.T) {
	for _, c := range []struct {
		o    Toggle
		want bool
	}{{"", true}, {ToggleOn, true}, {ToggleOff, false}} {
		p := Resolve(bigboi(), m5Pro(), lan, UserOverrides{PhaseLock: c.o})
		d := decision(p, "phaseLock")
		if p.PhaseLock != c.want || d.Value != onOff(c.want) {
			t.Errorf("phaseLock %q: %v (decision %+v), want %v", c.o, p.PhaseLock, d, c.want)
		}
	}
	if err := (UserOverrides{PhaseLock: "maybe"}).Validate(); err == nil {
		t.Error("phaseLock maybe validated")
	}
	if got := Merge(UserOverrides{PhaseLock: ToggleOff}, UserOverrides{}).PhaseLock; got != ToggleOff {
		t.Errorf("an auto top replaced phaseLock off with %q", got)
	}
}
