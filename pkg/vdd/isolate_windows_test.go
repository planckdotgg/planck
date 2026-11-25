//go:build windows

package vdd

import "testing"

func TestSoloConfig(t *testing.T) {
	src := func(x, y int32) modeInfo {
		m := modeInfo{InfoType: modeInfoTypeSource}
		m.Union[0], m.Union[1], m.Union[3], m.Union[4] = 1920, 1080, uint32(x), uint32(y)
		return m
	}
	c := displayConfig{
		modes: []modeInfo{src(0, 0), {InfoType: 2, ID: 7}, src(1920, 0), {InfoType: 2, ID: 9}},
		paths: []pathInfo{
			{Source: pathSourceInfo{ID: 1, ModeInfoIdx: 0}, Target: pathTargetInfo{ID: 7, ModeInfoIdx: 1}},
			{Source: pathSourceInfo{ID: 2, ModeInfoIdx: 2}, Target: pathTargetInfo{ID: 9, ModeInfoIdx: 3}},
		},
	}
	paths, modes := soloConfig(c, 1)
	if len(paths) != 1 || len(modes) != 2 {
		t.Fatalf("got %d paths, %d modes", len(paths), len(modes))
	}
	p := paths[0]
	if p.Source.ID != 2 || p.Target.ID != 9 || p.Source.ModeInfoIdx != 0 || p.Target.ModeInfoIdx != 1 {
		t.Errorf("path %+v", p)
	}
	if modes[0].Union[3] != 0 || modes[0].Union[4] != 0 || modes[1].ID != 9 {
		t.Errorf("modes %+v", modes)
	}
	if c.modes[2].Union[3] != 1920 || c.paths[1].Source.ModeInfoIdx != 2 {
		t.Error("soloConfig changed the saved layout")
	}
	noModes := displayConfig{paths: []pathInfo{{Source: pathSourceInfo{ModeInfoIdx: modeIdxInvalid}, Target: pathTargetInfo{ModeInfoIdx: modeIdxInvalid}}}}
	if paths, modes := soloConfig(noModes, 0); len(paths) != 1 || len(modes) != 0 {
		t.Errorf("no modes: %d paths, %d modes", len(paths), len(modes))
	}
}
