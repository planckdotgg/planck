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

// directMode decides when the stream's surface scans out directly. In
// direct mode MyGo's layers stop compositing, so its pill and drawer do not
// show: the pill is drawn into the video instead, and the drawer opening
// leaves direct mode until it closes.
type directMode struct {
	allowed atomic.Bool
	drawer  atomic.Bool
}

func directWanted(allowed, fullScreen, drawer bool) bool { return allowed && fullScreen && !drawer }

// applyDirect runs on the main thread.
func (v *videoView) applyDirect() {
	w, h := v.win.Load(), v.cur.Load()
	if w == nil || h == nil {
		return
	}
	want := directWanted(v.mode.allowed.Load(), w.IsFullScreen(), v.mode.drawer.Load())
	if h.surf.Direct() == want {
		return
	}
	if err := h.surf.SetDirect(want); err != nil {
		v.logf("planck: direct display %v: %v", want, err)
		v.mode.allowed.Store(false)
		return
	}
	v.drawable.Store(packSize(h.surf.Size()))
	v.logf("planck: direct display %v", want)
}

// drawerChanged hears the UI's stream drawer open or close. It runs inside
// the UI's frame, so the surface changes after it, on the main thread.
func (v *videoView) drawerChanged(open bool) {
	v.mode.drawer.Store(open)
	go mygo.RunOnMain(v.applyDirect)
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
		st := player.HUDState{Visible: s.host.surf.Direct() && (v.hudHidden == nil || !v.hudHidden())}
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
