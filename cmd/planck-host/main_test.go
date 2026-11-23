package main

import (
	"io"
	"testing"

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
