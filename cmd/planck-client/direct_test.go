//go:build darwin || windows

package main

import (
	"testing"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/player"
)

func TestDirectModeFollowsHUD(t *testing.T) {
	var m directMode
	m.allowed.Store(true)
	want := func(step string, fullScreen, direct bool) {
		t.Helper()
		if got := directWanted(m.allowed.Load(), fullScreen, m.drawerOpen()); got != direct {
			t.Errorf("%s: direct %v, want %v", step, got, direct)
		}
	}
	want("pill, windowed", false, false)
	want("pill, full screen", true, true)
	m.hotkey()
	want("drawer", true, false)
	m.hotkey()
	want("hidden", true, true)
	m.hotkey()
	want("pill again", true, true)
	m.clicked.Store(true)
	want("click in the pill band", true, false)
	m.hotkey()
	want("hotkey after the click: drawer", true, false)
	m.hotkey()
	want("hidden after the click", true, true)
	m.allowed.Store(false)
	want("profile forbids it", true, false)
}

func TestHUDState(t *testing.T) {
	cs := session.ClientStats{Path: "lan-direct", Codec: "HEVC", FPS: 119.6, Kbps: 142_500}
	cs.E2EGlass.P50 = 11.2
	st := hudState(cs, &defaults.ResolvedProfile{Codec: defaults.CodecHEVC, BitDepth: 10})
	if st.Signal != player.SignalDirect || st.Route != "LAN" || st.Codec != "HEVC 10-bit" || st.Mbps != 142.5 || st.LatencyMs != 11.2 {
		t.Fatalf("%+v", st)
	}
	cs.Path, cs.LossPct, cs.Codec = "tailscale-derp", 5, "PyroWave"
	if st := hudState(cs, &defaults.ResolvedProfile{Codec: defaults.CodecPyroWave}); st.Signal != player.SignalFault || st.Route != "Relay" || st.Codec != "PyroWave" {
		t.Fatalf("%+v", st)
	}
}
