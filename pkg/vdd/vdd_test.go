package vdd

import (
	"strings"
	"testing"
)

func TestMonitorID(t *testing.T) {
	a, b := MonitorID([]byte("client-a")), MonitorID([]byte("client-b"))
	if a != MonitorID([]byte("client-a")) {
		t.Fatal("MonitorID is not stable")
	}
	if a == b {
		t.Fatal("different seeds gave the same ID")
	}
	for _, g := range []GUID{a, b} {
		if g.Data3>>12 != 5 || g.Data4[0]&0xc0 != 0x80 {
			t.Fatalf("%v is not an RFC 4122 version 5 GUID", g)
		}
	}
	// Pinned so a change to the derivation, which would make Windows forget
	// every client's monitor settings, fails here.
	if got, want := MonitorID([]byte("planck-vdd")).String(), "{03096a28-d632-5096-ad8b-1c0cb42878ee}"; got != want {
		t.Fatalf("MonitorID(planck-vdd) = %s, want %s", got, want)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	id := MonitorID([]byte("planck-vdd"))
	s, err := DisplaySpec{Width: 1920, Height: 1080, RefreshMilliHz: 120000, ID: id}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Planck" || s.Serial != "03096a28d6325" || s.AttachTimeout != defaultAttach {
		t.Fatalf("defaults: name %q serial %q attach %v", s.Name, s.Serial, s.AttachTimeout)
	}
}

func TestNormalizeRejects(t *testing.T) {
	id := MonitorID(nil)
	ok := DisplaySpec{Width: 1920, Height: 1080, RefreshMilliHz: 120000, ID: id}
	cases := map[string]func(*DisplaySpec){
		"odd width":     func(s *DisplaySpec) { s.Width = 1921 },
		"zero height":   func(s *DisplaySpec) { s.Height = 0 },
		"refresh in Hz": func(s *DisplaySpec) { s.RefreshMilliHz = 120 },
		"no ID":         func(s *DisplaySpec) { s.ID = GUID{} },
		"long name":     func(s *DisplaySpec) { s.Name = strings.Repeat("x", 14) },
		"non-ASCII":     func(s *DisplaySpec) { s.Serial = "plancké" },
	}
	for name, mutate := range cases {
		s := ok
		mutate(&s)
		if _, err := s.normalize(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestElevatedGPUNameCommandQuotes(t *testing.T) {
	c := ElevatedGPUNameCommand("Bob's GPU")
	if !strings.Contains(c, "-Value 'Bob''s GPU'") || strings.Contains(c, "\n") {
		t.Fatalf("command not one line or not quoted: %s", c)
	}
}

// TestDPIRelative: a scale asks for the nearest Windows step, counted from
// the recommended one and kept within what the display allows.
func TestDPIRelative(t *testing.T) {
	for _, c := range []struct {
		pct, minRel, maxRel, want int
	}{
		{100, -4, 3, -4}, // SudoVDA recommended 200%: 100% is 4 steps down
		{200, -4, 3, 0},
		{150, -4, 3, -2},
		{100, 0, 4, 0}, // recommended 100%
		{200, 0, 4, 4},
		{300, 0, 4, 4}, // above the display's maximum
		{110, -2, 2, -2},
		{113, -2, 2, -1}, // nearer 125 than 100
		{50, -1, 3, -1},  // below 100% is 100%
		{900, -1, 3, 3},
	} {
		if got := dpiRelative(c.pct, c.minRel, c.maxRel); got != c.want {
			t.Errorf("%d%% in [%d, %d]: %d, want %d", c.pct, c.minRel, c.maxRel, got, c.want)
		}
	}
	for _, c := range []struct{ minRel, rel, want int }{{-4, -4, 100}, {-4, 0, 200}, {0, 0, 100}, {-2, 1, 175}, {0, 20, 500}} {
		if got := dpiPercent(c.minRel, c.rel); got != c.want {
			t.Errorf("min %d rel %d: %d%%, want %d%%", c.minRel, c.rel, got, c.want)
		}
	}
}
