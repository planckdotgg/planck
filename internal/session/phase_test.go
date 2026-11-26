package session

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/phaselock"
	"github.com/planckdotgg/planck/pkg/player"
)

// steerPipe is a fakePipe whose frames the phase lock can move.
type steerPipe struct {
	fakePipe
	moved atomic.Int64
}

func (p *steerPipe) canSteerPhase() bool { return true }

func (p *steerPipe) steerPhase(ns int64) {
	p.next += ns
	p.moved.Add(ns)
}

// phaseSink is a NullSink whose pictures are always ready errNs early.
type phaseSink struct {
	NullSink
	errNs int64
}

func (s *phaseSink) Submit(au []byte, pts int64) (player.Timing, error) { return s.NullSink.Submit(au, pts) }

func (s *phaseSink) Phase() phaselock.Report {
	n := uint32(s.Units.Load())
	if n < 32 {
		return phaselock.Report{}
	}
	return phaselock.Report{Valid: true, Mode: phaselock.ModeDirect, PeriodNs: int64(time.Second / 120),
		SlackNs: s.errNs + 1_200_000, TargetNs: 1_200_000, ErrorNs: s.errNs, JitterNs: 100_000, Oldest: n - 32, Newest: n - 1}
}

func phaseClient(t *testing.T, pin string, o defaults.UserOverrides, sink *phaseSink) *clientRig {
	t.Helper()
	r := capsClient(t, pin, o)
	r.client.cfg.OpenSink = func(decode.Config) (Sink, error) { return sink, nil }
	return r
}

// TestPhaseLockSteersTheHost: a client whose pictures come too early
// tells the host, which captures its frames later, by no more than its
// slew a frame.
func TestPhaseLockSteersTheHost(t *testing.T) {
	var forced, kbps atomic.Int64
	pipes := make(chan *steerPipe, 4)
	h := startCapsHost(t, func(p VideoParams) (VideoPipe, error) {
		sp := &steerPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}}
		pipes <- sp
		return sp, nil
	})
	sink := &phaseSink{errNs: 2_000_000}
	r := phaseClient(t, h.PIN(), defaults.UserOverrides{}, sink)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	pipe := <-pipes
	waitFor(t, "the host to steer", func() bool { return pipe.moved.Load() > 0 })
	if !r.client.Stats().PhaseSent {
		t.Fatal("the client says it sent no phase")
	}
	st := h.current().phase.Load().State()
	if !st.Active || st.Corrections == 0 {
		t.Fatalf("controller %+v: want active with a move", st)
	}
	if got := pipe.moved.Load(); got > 2_000_000 {
		t.Fatalf("moved %d ns for a 2 ms error", got)
	}
}

func TestPhaseLockOff(t *testing.T) {
	var forced, kbps atomic.Int64
	pipes := make(chan *steerPipe, 4)
	h := startCapsHost(t, func(p VideoParams) (VideoPipe, error) {
		sp := &steerPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}}
		pipes <- sp
		return sp, nil
	})
	sink := &phaseSink{errNs: 2_000_000}
	r := phaseClient(t, h.PIN(), defaults.UserOverrides{PhaseLock: defaults.ToggleOff}, sink)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	pipe := <-pipes
	waitFor(t, "video frames", func() bool { return sink.Units.Load() >= 150 })
	if r.client.Stats().PhaseSent {
		t.Fatal("phase sent with phase lock off")
	}
	if got := pipe.moved.Load(); got != 0 {
		t.Fatalf("the host moved its frames %d ns with phase lock off", got)
	}
}

// A display capture cannot be steered: the host hears the reports and
// says why it does not act on them.
func TestPhaseLockDisplayCaptureStaysOff(t *testing.T) {
	var forced, kbps atomic.Int64
	h := startCapsHost(t, func(p VideoParams) (VideoPipe, error) {
		return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
	})
	sink := &phaseSink{errNs: 2_000_000}
	r := phaseClient(t, h.PIN(), defaults.UserOverrides{}, sink)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "phase reports", func() bool { return r.client.Stats().PhaseSent })
	st := h.current().phase.Load().State()
	if st.Active || !strings.Contains(st.Reason, "display's refresh") {
		t.Fatalf("controller %+v: want off, on the display's refresh", st)
	}
}
