//go:build darwin || windows

package main

import (
	"testing"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/player"
)

// TestDirectModeFollowsDrawer: direct mode needs full screen, a profile
// that allows it and the drawer closed, and the UI's drawer callback
// moves it.
func TestDirectModeFollowsDrawer(t *testing.T) {
	v := &videoView{logf: t.Logf}
	v.mode.allowed.Store(true)
	want := func(step string, fullScreen, direct bool) {
		t.Helper()
		if got := directWanted(v.mode.allowed.Load(), fullScreen, v.mode.drawer.Load()); got != direct {
			t.Errorf("%s: direct %v, want %v", step, got, direct)
		}
	}
	want("pill, windowed", false, false)
	want("pill, full screen", true, true)
	v.drawerChanged(true)
	want("drawer open", true, false)
	v.drawerChanged(false)
	want("drawer closed", true, true)
	want("drawer closed, windowed", false, false)
	v.mode.allowed.Store(false)
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
