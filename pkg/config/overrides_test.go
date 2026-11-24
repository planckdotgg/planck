package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

func TestOverridesRoundTripAndPerHostMerge(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultClientConfig()
	cfg.Overrides = defaults.UserOverrides{Codec: "hevc", FPS: "120", VSyncMode: "on"}
	cfg.HostOverrides = map[string]defaults.UserOverrides{
		"bigboi": {Resolution: "1080p", Codec: "pyrowave", VirtualDisplayGPU: "4080"},
	}
	if err := SaveClientConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := defaults.UserOverrides{Codec: "pyrowave", FPS: "120", VSyncMode: "on", Resolution: "1080p", VirtualDisplayGPU: "4080"}
	if got := loaded.OverridesFor("bigboi"); got != want {
		t.Fatalf("OverridesFor(bigboi)\n got %+v\nwant %+v", got, want)
	}
	if got := loaded.OverridesFor("other"); got != cfg.Overrides {
		t.Fatalf("OverridesFor(other) = %+v, want the global overrides", got)
	}

	host := DefaultHostConfig(dir)
	host.Policy = defaults.UserOverrides{BitrateMbps: 50, VirtualDisplay: "off"}
	if err := SaveHostConfig(dir, host); err != nil {
		t.Fatal(err)
	}
	h, err := LoadHostConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h.Policy != host.Policy {
		t.Fatalf("policy = %+v, want %+v", h.Policy, host.Policy)
	}
}

func TestOverridesValidated(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultClientConfig()
	cfg.Overrides.FPS = "90"
	if err := SaveClientConfig(dir, cfg); err == nil {
		t.Fatal("saved fps 90")
	}
	cfg = DefaultClientConfig()
	cfg.HostOverrides = map[string]defaults.UserOverrides{"h1": {Resolution: "custom", CustomWidth: 100, CustomHeight: 100}}
	if err := SaveClientConfig(dir, cfg); err == nil || !strings.Contains(err.Error(), "h1") {
		t.Fatalf("saved a bad per-host custom size: %v", err)
	}
	host := DefaultHostConfig(dir)
	host.Policy.Codec = "vp9"
	if err := SaveHostConfig(dir, host); err == nil {
		t.Fatal("saved policy codec vp9")
	}
}

func TestLegacyClientFieldsMigrate(t *testing.T) {
	tests := []struct {
		file string
		want defaults.UserOverrides
	}{
		{`{"preferredCodec":"hevc","preferredChroma":"nv12","targetFPS":120,"maxBitrateKbps":100000}`, defaults.UserOverrides{}},
		{`{"preferredCodec":"av1","preferredChroma":"yuv444p10","targetFPS":240,"maxBitrateKbps":100000}`,
			defaults.UserOverrides{Codec: "av1", Chroma: "444", BitDepth: "10", FPS: "240"}},
		{`{"preferredCodec":"h264","preferredChroma":"p010","targetFPS":60,"maxBitrateKbps":100000}`,
			defaults.UserOverrides{Codec: "h264", BitDepth: "10", FPS: "60"}},
		{`{"preferredCodec":"hevc","preferredChroma":"yuv444","targetFPS":90,"maxBitrateKbps":100000}`,
			defaults.UserOverrides{Chroma: "444"}},
		{`{"preferredCodec":"av1","preferredChroma":"yuv444","targetFPS":60,"maxBitrateKbps":100000,"overrides":{}}`,
			defaults.UserOverrides{}},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(tt.file), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadClientConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Overrides != tt.want {
			t.Errorf("%s\n got %+v\nwant %+v", tt.file, cfg.Overrides, tt.want)
		}
	}
}

func TestMigratedOverrideResetStaysAuto(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"preferredCodec":"av1","preferredChroma":"nv12","targetFPS":120,"maxBitrateKbps":100000}`
	if err := os.WriteFile(filepath.Join(dir, ClientConfigFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Overrides.Codec != "av1" {
		t.Fatalf("not migrated: %+v", cfg.Overrides)
	}
	cfg.Overrides.Codec = "auto"
	if err := SaveClientConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	again, err := LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Overrides.Codec != "auto" {
		t.Fatalf("reset to auto came back as %q", again.Overrides.Codec)
	}
}
