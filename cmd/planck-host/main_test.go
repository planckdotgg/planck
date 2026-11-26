package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/encode"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, stats, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":47701" || cfg.MediaPort != 47702 || cfg.DiscoveryAddr != ":47700" {
		t.Errorf("ports: %q %d %q", cfg.ListenAddr, cfg.MediaPort, cfg.DiscoveryAddr)
	}
	if cfg.Codec != encode.HEVC || cfg.Chroma != encode.NV12 || cfg.FPS != 0 || !cfg.InjectInput || cfg.Synth || stats == 0 {
		t.Errorf("defaults: %+v stats=%v", cfg, stats)
	}
}

func TestParseConfigSynthBenchmark(t *testing.T) {
	cfg, _, err := parseConfig([]string{"-synth", "-adapter", "NVIDIA", "-fps", "240", "-codec", "av1", "-chroma", "yuv444p10",
		"-no-input", "-no-audio", "-discovery-port", "0", "-pin", "123456", "-slices", "4", "-subframe"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Synth || cfg.Adapter != "NVIDIA" || cfg.FPS != 240 || cfg.Width != 1920 || cfg.Height != 1080 ||
		cfg.Codec != encode.AV1 || cfg.Chroma != encode.YUV444P10 || cfg.InjectInput || !cfg.NoAudio ||
		cfg.DiscoveryAddr != "off" || cfg.PIN != "123456" || cfg.Slices != 4 || !cfg.SubFrame {
		t.Errorf("got %+v", cfg)
	}
	if cfg, _, _ := parseConfig([]string{"-synth"}, io.Discard); cfg.FPS != 60 {
		t.Errorf("-synth without -fps: %d fps, want 60", cfg.FPS)
	}
}

func TestParseConfigRejects(t *testing.T) {
	for _, args := range [][]string{
		{"-codec", "vp9"},
		{"-chroma", "422"},
		{"-pin", "12ab56"},
		{"-pin", "1234"},
		{"-slices", "17"},
		{"-subframe"},
		{"-bitrate", "0"},
		{"-media-port", "70000"},
		{"stray"},
	} {
		if _, _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

// TestParseSetName: -set-name renames for good, even to "", -name for the
// run only, and not both.
func TestParseSetName(t *testing.T) {
	o, err := parseOptions([]string{"-set-name", "Den PC"}, io.Discard)
	if err != nil || o.setName == nil || *o.setName != "Den PC" || o.cfg.Name != "" {
		t.Fatalf("-set-name: %+v, %v", o, err)
	}
	if o, err := parseOptions([]string{"-set-name", ""}, io.Discard); err != nil || o.setName == nil || *o.setName != "" {
		t.Fatalf(`-set-name "": %+v, %v`, o, err)
	}
	if o, err := parseOptions([]string{"-name", "Den PC"}, io.Discard); err != nil || o.setName != nil || o.cfg.Name != "Den PC" {
		t.Fatalf("-name: %+v, %v", o, err)
	}
	if _, err := parseOptions([]string{"-name", "a", "-set-name", "b"}, io.Discard); err == nil {
		t.Fatal("-name with -set-name accepted")
	}
}

func TestParseConfigPolicy(t *testing.T) {
	cfg, _, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := policyLine(cfg.Policy); got != "auto" {
		t.Errorf("default flags bind %q, want nothing", got)
	}
	cfg, _, err = parseConfig([]string{"-codec", "pyrowave", "-res", "1080p", "-fps", "120", "-vdd", "on", "-vdd-gpu", "4080",
		"-bitrate", "400000", "-split", "off", "-vdd-primary"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Policy
	if p.Codec != "pyrowave" || p.Resolution != "1080p" || p.FPS != "120" || p.VirtualDisplay != "on" || p.VirtualDisplayGPU != "4080" ||
		p.BitrateMbps != 400 || p.NVENC2EngineSplit != "off" || !cfg.VDDPrimary || cfg.Codec != 0 {
		t.Errorf("policy %+v, legacy codec %v", p, cfg.Codec)
	}
	for _, args := range [][]string{{"-vdd", "maybe"}, {"-res", "wide"}, {"-bit-depth", "12"}, {"-subframe-slices", "3"}} {
		if _, _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

func TestParseOptionsVersion(t *testing.T) {
	for _, args := range [][]string{{"-version"}, {"--version"}, {"-synth", "-version"}} {
		if _, err := parseOptions(args, io.Discard); !errors.Is(err, errShowVersion) {
			t.Errorf("%q: err = %v, want errShowVersion", args, err)
		}
	}
}

func TestNoTelemetryFlag(t *testing.T) {
	o, err := parseOptions([]string{"-no-telemetry"}, io.Discard)
	if err != nil || !o.noTelemetry {
		t.Fatalf("-no-telemetry: %+v, %v", o.noTelemetry, err)
	}
	if o, _ := parseOptions(nil, io.Discard); o.noTelemetry {
		t.Fatal("telemetry is off without -no-telemetry")
	}
}

// TestHostTelemetryFollowsTheAppSetting: the host reads client.json's
// switch, and again before each session, and -no-telemetry beats it.
func TestHostTelemetryFollowsTheAppSetting(t *testing.T) {
	t.Setenv("PLANCK_TELEMETRY_APP_ID", "APP")
	t.Setenv("PLANCK_TELEMETRY_ENDPOINT", "http://127.0.0.1:1/v2/")
	dir := t.TempDir()
	tel, on := hostTelemetry(dir, false)
	defer tel.Close(0)
	if !tel.Active() || !on() {
		t.Fatal("off by default")
	}
	if err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error { off := false; c.TelemetryEnabled = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	sessionTelemetry(tel, on)(session.SessionEvent{Started: true, Codec: encode.HEVC})
	if tel.Active() {
		t.Fatal("still on after the app turned it off")
	}
	forced, on := hostTelemetry(t.TempDir(), true)
	defer forced.Close(0)
	if forced.Active() || on() {
		t.Fatal("-no-telemetry left it on")
	}
}

// TestHostSendsNothingAfterTheAppTurnsTelemetryOff: signals queued behind a
// refused send are dropped, not flushed on shutdown, once client.json says
// off.
func TestHostSendsNothingAfterTheAppTurnsTelemetryOff(t *testing.T) {
	var requests atomic.Int32
	got := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		got <- struct{}{}
	}))
	defer srv.Close()
	t.Setenv("PLANCK_TELEMETRY_APP_ID", "APP")
	t.Setenv("PLANCK_TELEMETRY_ENDPOINT", srv.URL+"/v2/")
	dir := t.TempDir()
	tel, _ := hostTelemetry(dir, false)
	tel.Launched()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no first send")
	}
	if err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error { off := false; c.TelemetryEnabled = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	tel.Close(5 * time.Second)
	if n := requests.Load(); n != 1 {
		t.Fatalf("%d requests, want only the one before telemetry was turned off", n)
	}
}
