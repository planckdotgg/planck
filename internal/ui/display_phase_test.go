package ui

import (
	"strings"
	"testing"
)

func TestDiagnosticsLineDisplay(t *testing.T) {
	for _, c := range []struct {
		d    DisplayPhase
		want []string
		not  []string
	}{
		{DisplayPhase{}, nil, []string{"scan-out", "phase"}},
		{DisplayPhase{ScanOut: "direct", RefreshHz: 120.0013, Locked: true}, []string{"scan-out direct", "phase locked at 120.00 Hz"}, []string{"composited"}},
		{DisplayPhase{ScanOut: "composited", RefreshHz: 120, ErrorMs: 5.43}, []string{"scan-out composited, +1 refresh", "phase +5.4 ms at 120.00 Hz"}, []string{"locked"}},
	} {
		line := diagnosticsLine(SessionStats{Display: c.d})
		for _, w := range c.want {
			if !strings.Contains(line, w) {
				t.Errorf("%+v: %q lacks %q", c.d, line, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(line, n) {
				t.Errorf("%+v: %q has %q", c.d, line, n)
			}
		}
	}
}
