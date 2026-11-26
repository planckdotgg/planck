package session

import (
	"time"

	"github.com/planckdotgg/planck/pkg/phaselock"
	"github.com/planckdotgg/planck/pkg/wire"
)

// phaseSteerer is a VideoPipe whose source paces its own frames and can
// capture them earlier or later than its cadence (the synthetic pattern).
// A display capture runs on the display's refresh: DWM composes a virtual
// display on a fixed 120 Hz grid, so a later capture would find no newer
// picture and only move the client's wait onto the host.
type phaseSteerer interface {
	canSteerPhase() bool
	steerPhase(ns int64)
}

func steererOf(p VideoPipe) phaseSteerer {
	if s, ok := p.(phaseSteerer); ok && s.canSteerPhase() {
		return s
	}
	return nil
}

func (p *sourcePipe) canSteerPhase() bool {
	_, ok := p.src.(interface{ SteerPhase(int64) })
	return ok
}

func (p *sourcePipe) steerPhase(ns int64) {
	p.src.(interface{ SteerPhase(int64) }).SteerPhase(ns)
}

// SteerPhase captures the next frame ns later than its cadence, and every
// one after it; earlier when ns is below 0.
func (s *synthPattern) SteerPhase(ns int64) {
	if s.next != 0 {
		s.next += ns
	}
}

// openPhase gives a newly opened pipe its phase controller: one that
// steers where the profile has phase lock on and the source can move its
// frames, and one that stays off and says why otherwise.
func (st *stream) openPhase(pipe VideoPipe, fps int) {
	on := st.profile != nil && st.profile.PhaseLock
	cfg := phaselock.ControllerConfig{Steerable: on && steererOf(pipe) != nil}
	if fps > 0 {
		cfg.FramePeriod = int64(time.Second) / int64(fps)
	}
	st.phase.Store(phaselock.NewController(cfg))
	st.phaseMu.Lock()
	st.phaseLog = phaselock.State{}
	st.phaseMu.Unlock()
}

// phaseReport hands the client's display phase to the controller, on the
// control goroutine, and logs when steering starts or stops.
func (st *stream) phaseReport(m *wire.DisplayPhase) {
	ctl := st.phase.Load()
	if ctl == nil {
		return
	}
	mode := phaselock.ModeUnknown
	switch {
	case m.Direct:
		mode = phaselock.ModeDirect
	case m.Composited:
		mode = phaselock.ModeComposited
	}
	ctl.Update(phaselock.Report{
		Valid: m.Valid, Locked: m.Locked, Mode: mode, PeriodNs: int64(m.PeriodNs),
		SlackNs: int64(m.SlackNs), TargetNs: int64(m.TargetNs), ErrorNs: int64(m.ErrorNs), JitterNs: int64(m.JitterNs),
		LatePct: float64(m.LatePermille) / 10, CompositedPct: float64(m.CompositedPermille) / 10,
		Oldest: m.Oldest, Newest: m.Newest,
	})
	now := ctl.State()
	st.phaseMu.Lock()
	last := st.phaseLog
	st.phaseLog = now
	st.phaseMu.Unlock()
	switch {
	case now.Active && !last.Active:
		st.h.logf("planck: phase lock steering: client %.3f Hz, %s, error %.2f ms", 1e9/float64(max(m.PeriodNs, 1)), mode, float64(m.ErrorNs)/1e6)
	case !now.Active && now.Reason != last.Reason:
		st.h.logf("planck: phase lock off: %s (client %s)", now.Reason, mode)
	}
}
