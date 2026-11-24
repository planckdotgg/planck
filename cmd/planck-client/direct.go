//go:build darwin || windows

package main

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/player"
)

// hudStep mirrors ui.App's HUD hotkey cycle: pill, drawer, hidden.
const (
	hudStepPill = iota
	hudStepDrawer
	hudStepHidden
	hudSteps
)

// directMode decides when the stream's surface scans out directly. In
// direct mode MyGo's layers stop compositing, so its pill and drawer do not
// show: the pill is drawn into the video instead, and anything that may
// open the drawer leaves direct mode first.
//
// The UI does not report its drawer yet. Until it does, the HUD hotkey's
// cycle is mirrored here, and a click in the pill's band counts as the
// drawer opening; it stays composited until the hotkey moves the HUD on
// or the window enters full screen again.
type directMode struct {
	allowed atomic.Bool
	step    atomic.Int32
	clicked atomic.Bool
}

func directWanted(allowed, fullScreen, drawer bool) bool { return allowed && fullScreen && !drawer }

func (m *directMode) drawerOpen() bool {
	return m.step.Load() == hudStepDrawer || m.clicked.Load()
}

// hotkey follows one press of the HUD hotkey.
func (m *directMode) hotkey() {
	m.step.Store((m.step.Load() + 1) % hudSteps)
	m.clicked.Store(false)
}

// pillBandPt is the top of the stream where the pill sits, in points.
const pillBandPt = player.HUDPillTopPt + player.HUDPillHeightPt + player.HUDPillPadPt

// applyDirect runs on the main thread.
func (v *videoView) applyDirect() {
	w, h := v.win.Load(), v.cur.Load()
	if w == nil || h == nil {
		return
	}
	want := directWanted(v.mode.allowed.Load(), w.IsFullScreen(), v.mode.drawerOpen())
	if h.surf.Direct() == want {
		return
	}
	if err := h.surf.SetDirect(want); err != nil {
		v.logf("planck: direct display %v: %v", want, err)
		v.mode.allowed.Store(false)
		return
	}
	v.logf("planck: direct display %v", want)
}

func (v *videoView) enteredFullScreen() {
	v.mode.clicked.Store(false)
	v.applyDirect()
}

// hudEvent runs on the main thread, from the stream's input surface.
func (v *videoView) hudEvent(hotkey bool, yPt float32) {
	switch {
	case hotkey:
		v.mode.hotkey()
	case yPt <= pillBandPt:
		v.mode.clicked.Store(true)
	default:
		return
	}
	v.applyDirect()
}

// hudLoop feeds the pill drawn into the video while direct mode hides the
// window's own, until stop closes.
func (v *videoView) hudLoop(s *surfaceSink, stop <-chan struct{}) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		st := player.HUDState{Visible: s.host.surf.Direct() && v.mode.step.Load() != hudStepHidden}
		if st.Visible && v.stats != nil {
			if cs, ok := v.stats(); ok {
				st = hudState(cs, s.profile.Load())
				st.Visible = true
			}
		}
		s.p.SetHUD(st)
	}
}

func hudState(cs session.ClientStats, p *defaults.ResolvedProfile) player.HUDState {
	st := player.HUDState{FPS: cs.FPS, Mbps: cs.Kbps / 1000, LatencyMs: cs.E2EGlass.P50, Codec: cs.Codec}
	if st.LatencyMs == 0 {
		st.LatencyMs = cs.E2E.P50
	}
	switch cs.Path {
	case discovery.PathLANDirect.String():
		st.Signal, st.Route = player.SignalDirect, "LAN"
	case discovery.PathTailscaleDirect.String():
		st.Signal, st.Route = player.SignalDirect, "Tailscale"
	default:
		st.Signal, st.Route = player.SignalRelay, "Relay"
	}
	if cs.LossPct > 2 {
		st.Signal = player.SignalFault
	}
	if p != nil && p.Codec != defaults.CodecPyroWave {
		st.Codec = fmt.Sprintf("%s %d-bit", cs.Codec, p.BitDepth)
	}
	return st
}

// SetProfile takes the session's resolved profile: whether direct display
// is allowed, and the HUD's codec label.
func (s *surfaceSink) SetProfile(p *defaults.ResolvedProfile) {
	s.profile.Store(p)
	s.v.mode.allowed.Store(p.DirectDisplay)
	mygo.RunOnMain(s.v.applyDirect)
}
