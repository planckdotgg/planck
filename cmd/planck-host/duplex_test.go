package main

import (
	"io"
	"slices"
	"testing"

	"github.com/planckdotgg/planck/internal/session"
)

func TestParseDuplexFlags(t *testing.T) {
	cfg, _, err := parseConfig(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clipboard != (session.ClipboardConfig{Send: true, Receive: true}) || cfg.NoMic || cfg.MicDevice != "" {
		t.Errorf("defaults: clipboard %+v, no mic %v, mic device %q", cfg.Clipboard, cfg.NoMic, cfg.MicDevice)
	}
	cfg, _, err = parseConfig([]string{"-clipboard", "client-to-host", "-mic-device", "CABLE", "-audio-exclude-pids", "12, 34"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clipboard != (session.ClipboardConfig{Receive: true}) || cfg.MicDevice != "CABLE" || !slices.Equal(cfg.AudioExcludePIDs, []int{12, 34}) {
		t.Errorf("got clipboard %+v, mic %q, pids %v", cfg.Clipboard, cfg.MicDevice, cfg.AudioExcludePIDs)
	}
	cfg, _, _ = parseConfig([]string{"-mic-device", "off", "-clipboard", "off"}, io.Discard)
	if !cfg.NoMic || cfg.Clipboard != (session.ClipboardConfig{}) {
		t.Errorf("off: no mic %v, clipboard %+v", cfg.NoMic, cfg.Clipboard)
	}
	for _, args := range [][]string{{"-clipboard", "sideways"}, {"-audio-exclude-pids", "12,x"}, {"-audio-exclude-pids", "-3"}} {
		if _, _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
