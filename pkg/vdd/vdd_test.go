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
