package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func pinnedClient(t *testing.T, dir string) {
	t.Helper()
	err := UpdateClientConfig(dir, func(c *ClientConfig) error {
		return c.Pin(PinnedHost{HostID: "host-1", Name: "bigboi", CertFingerprint: strings.Repeat("ab", 32)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

var lanCalibration = CalibratedRoute{
	At:           time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC),
	Overrides:    defaults.UserOverrides{Codec: "av1", BitDepth: "10", SubFrameSlices: "4", NVENC2EngineSplit: "off", VSyncMode: "off", PhaseLock: "on"},
	ReceiveQueue: defaults.ReceiveQueue{Bytes: 120 * 1452, DrainMbps: 160},
	Link:         CalibratedLink{RTTP50Micros: 410, RTTP99Micros: 900, JitterMicros: 60, BandwidthMbps: 2200, DatagramBytes: 1452},
	GlassMs:      9.5,
	Warnings:     []string{"composited"},
}

// TestCalibrationPersistsPerRoute: a calibration is kept per host and per
// route in client.json, read back whole, and reset by route or all.
func TestCalibrationPersistsPerRoute(t *testing.T) {
	dir := t.TempDir()
	pinnedClient(t, dir)
	ts := lanCalibration
	ts.Overrides = defaults.UserOverrides{Codec: "hevc"}
	ts.ReceiveQueue = defaults.ReceiveQueue{}
	err := UpdateClientConfig(dir, func(c *ClientConfig) error {
		if err := c.ApplyCalibration("host-1", "lan-direct", lanCalibration); err != nil {
			return err
		}
		return c.ApplyCalibration("host-1", "tailscale-direct", ts)
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Calibration("host-1", "lan-direct")
	if !ok || got.Overrides != lanCalibration.Overrides || got.ReceiveQueue != lanCalibration.ReceiveQueue ||
		got.Link != lanCalibration.Link || !got.At.Equal(lanCalibration.At) || got.GlassMs != 9.5 || len(got.Warnings) != 1 {
		t.Fatalf("lan calibration read back as %+v, %v", got, ok)
	}
	if got, ok := c.Calibration("host-1", "tailscale-direct"); !ok || got.Overrides.Codec != "hevc" {
		t.Fatalf("tailscale calibration: %+v, %v", got, ok)
	}
	if _, ok := c.Calibration("host-1", "tailscale-derp"); ok {
		t.Fatal("a route never calibrated has a calibration")
	}

	if err := UpdateClientConfig(dir, func(c *ClientConfig) error { c.ResetCalibration("host-1", "lan-direct"); return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadClientConfig(dir)
	if _, ok := c.Calibration("host-1", "lan-direct"); ok {
		t.Fatal("reset left the lan calibration")
	}
	if _, ok := c.Calibration("host-1", "tailscale-direct"); !ok {
		t.Fatal("resetting the lan route lost the tailscale one")
	}
	if err := UpdateClientConfig(dir, func(c *ClientConfig) error { c.ResetCalibration("host-1", ""); return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadClientConfig(dir)
	if c.PinnedHosts["host-1"].Calibrated != nil || c.PinnedHosts["host-1"].Name != "bigboi" {
		t.Fatalf("reset all: %+v", c.PinnedHosts["host-1"])
	}
}

// TestCalibrationOverridePrecedence: an auto knob takes calibration's
// value, and a knob the user set keeps the user's.
func TestCalibrationOverridePrecedence(t *testing.T) {
	user := defaults.UserOverrides{Codec: "hevc", VSyncMode: "auto", FPS: "240"}
	got := lanCalibration.ResolveOverrides(user)
	want := lanCalibration.Overrides
	want.Codec, want.FPS = "hevc", "240"
	if got != want {
		t.Fatalf("resolved %+v, want %+v", got, want)
	}
	if got := lanCalibration.ResolveOverrides(defaults.UserOverrides{}); got != lanCalibration.Overrides {
		t.Fatalf("all auto resolved to %+v", got)
	}
	r := lanCalibration.Route(defaults.RouteInfo{Kind: defaults.RouteLAN, LANDatagram: true})
	if r.ReceiveQueue == nil || *r.ReceiveQueue != lanCalibration.ReceiveQueue || r.ProbedMbps != 2200 || r.RTTMicros != 410 || !r.LANDatagram {
		t.Fatalf("route %+v", r)
	}
}

func TestApplyCalibrationRefuses(t *testing.T) {
	c := DefaultClientConfig()
	if err := c.ApplyCalibration("nobody", "lan-direct", lanCalibration); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("unpinned host: %v", err)
	}
	if err := c.Pin(PinnedHost{HostID: "host-1", CertFingerprint: strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	bad := lanCalibration
	bad.Overrides.Codec = "mpeg2"
	if err := c.ApplyCalibration("host-1", "lan-direct", bad); err == nil {
		t.Fatal("bad overrides kept")
	}
	bad = lanCalibration
	bad.ReceiveQueue.DrainMbps = 0
	if err := c.ApplyCalibration("host-1", "lan-direct", bad); err == nil {
		t.Fatal("a queue with no drain kept")
	}
	if err := c.ApplyCalibration("host-1", "", lanCalibration); err == nil {
		t.Fatal("calibration with no route kept")
	}
}
