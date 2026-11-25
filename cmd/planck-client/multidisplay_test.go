package main

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func hostDisplays(n int) []defaults.HostDisplay {
	ids := []string{`\\.\DISPLAY1`, `\\.\DISPLAY2`, `\\.\DISPLAY3`, `\\.\DISPLAY4`, `\\.\DISPLAY5`}
	ds := make([]defaults.HostDisplay, n)
	for i := range ds {
		ds[i] = defaults.HostDisplay{ID: ids[i]}
	}
	if n > 1 {
		ds[1].Primary = true
	}
	return ds
}

func TestPlanDisplayWindows(t *testing.T) {
	d := func(i int) string { return hostDisplays(5)[i-1].ID }
	for _, c := range []struct {
		name     string
		ds       []defaults.HostDisplay
		streamed string
		every    bool
		max, at  int
		want     []string
		hidden   int
	}{
		{"one display, every", hostDisplays(1), d(1), true, 4, 4, []string{d(1)}, 0},
		{"no displays", nil, "", true, 4, 4, []string{""}, 0},
		{"every off", hostDisplays(3), d(3), false, 4, 4, []string{d(3)}, 2},
		{"one stream at a time", hostDisplays(3), d(1), true, 4, hostStreamsAtOnce, []string{d(1)}, 2},
		{"streamed first, then the main one", hostDisplays(3), d(3), true, 4, 4, []string{d(3), d(2), d(1)}, 0},
		{"capped by the setting", hostDisplays(5), d(1), true, 2, 8, []string{d(1), d(2)}, 3},
		{"virtual display", hostDisplays(2), "", true, 4, 4, []string{"", d(2)}, 1},
	} {
		p := planDisplayWindows(c.ds, c.streamed, c.every, c.max, c.at)
		if !slices.Equal(p.windows, c.want) || p.hidden != c.hidden {
			t.Errorf("%s: windows %q hidden %d, want %q hidden %d", c.name, p.windows, p.hidden, c.want, c.hidden)
		}
	}
}

func TestDisplayNotice(t *testing.T) {
	one := planDisplayWindows(hostDisplays(3), `\\.\DISPLAY1`, true, 4, hostStreamsAtOnce)
	if got := displayNotice("bigboi", true, one); got != "bigboi streams one display at a time" {
		t.Errorf("one at a time: %q", got)
	}
	capped := planDisplayWindows(hostDisplays(5), `\\.\DISPLAY1`, true, 2, 8)
	if got := displayNotice("bigboi", true, capped); got != "3 more displays on bigboi" {
		t.Errorf("capped: %q", got)
	}
	for _, c := range []struct {
		every bool
		p     displayPlan
	}{{false, one}, {true, planDisplayWindows(hostDisplays(1), `\\.\DISPLAY1`, true, 4, 1)}} {
		if got := displayNotice("bigboi", c.every, c.p); got != "" {
			t.Errorf("every %v, plan %+v: notice %q", c.every, c.p, got)
		}
	}
}
