package main

import (
	"context"
	"errors"
	"slices"

	"github.com/planckdotgg/planck/internal/calibrate"
	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/transport"
)

// calibration is the backend's calibration: the run in progress, if any,
// and the last result, which ApplyCalibration keeps. Under backend.mu.
type calibration struct {
	state ui.CalibrationState
	// cancel and done are the running calibration's; nil without one.
	cancel context.CancelFunc
	done   chan struct{}
	// last is the last finished run, for the host lastFor.
	last    *calibrate.Result
	lastFor string
}

// pathKinds are the routes in the order CalibrationStatus lists them.
var pathKinds = []discovery.PathKind{discovery.PathLANDirect, discovery.PathTailscaleDirect, discovery.PathTailscaleDERP}

func (b *backend) StartCalibration(hostID string) error {
	b.mu.Lock()
	switch {
	case b.cal.done != nil:
		b.mu.Unlock()
		return ui.ErrCalibrating
	case b.client != nil:
		b.mu.Unlock()
		return ui.ErrStreaming
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	b.cal.cancel, b.cal.done = cancel, done
	b.cal.state = ui.CalibrationState{Running: true, HostID: hostID, Phase: ui.CalibratingNetwork, Step: "connecting"}
	b.mu.Unlock()
	b.changed()
	go func() {
		defer close(done)
		res, err := b.runCalibration(ctx, hostID)
		b.mu.Lock()
		st := &b.cal.state
		st.Running = false
		switch {
		case err == nil:
			r := calibrationResult(res)
			st.Result, st.Phase, st.Percent = &r, ui.CalibrationFinished, 100
			b.cal.last, b.cal.lastFor = &res, hostID
		case ctx.Err() != nil:
			st.Cancelled = true
		case errors.Is(err, transport.ErrAuthDenied):
			st.Err = "pair with this host before calibrating it"
		default:
			st.Err = err.Error()
		}
		b.cal.cancel, b.cal.done = nil, nil
		b.mu.Unlock()
		cancel()
		b.changed()
	}()
	return nil
}

// runCalibration calibrates the route a stream from hostID would take,
// with the overrides it would ask for.
func (b *backend) runCalibration(ctx context.Context, hostID string) (calibrate.Result, error) {
	t, name := b.resolveTarget(ctx, hostID)
	ov := b.overridesFor(targetKey(t))
	t, err := routeAllowed(t, ov.RoutePolicy)
	if err != nil {
		return calibrate.Result{}, err
	}
	b.mu.Lock()
	b.cal.state.HostName = name
	device := b.name
	b.mu.Unlock()
	d := &calibrate.SessionDialer{
		Config: session.ClientConfig{ConfigDir: b.dir, Name: device, Tailscale: b.ts, Logf: b.logf, Capabilities: b.capabilities},
		Target: t, DisplaySink: b.openSink,
	}
	o := calibrate.Options{Client: b.capabilities(), User: ov, Display: b.openSink != nil, Hints: b.calibrationHints, Timing: b.calibrationTiming}
	return calibrate.Run(ctx, d, o, func(p calibrate.Progress) {
		b.mu.Lock()
		st := &b.cal.state
		st.Phase, st.Step, st.Percent = ui.CalibrationPhase(p.Phase), p.Step, p.Percent
		if p.Link != nil {
			l := calibrationLink(*p.Link)
			st.Link = &l
		}
		st.Candidates = calibrationCandidates(p.Candidates, -1)
		if p.Display != nil {
			dp := calibrationDisplay(*p.Display)
			st.Display = &dp
		}
		b.mu.Unlock()
		b.changed()
	})
}

func (b *backend) CancelCalibration() {
	b.mu.Lock()
	cancel := b.cal.cancel
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelCalibration ends a running calibration and waits for it.
func (b *backend) cancelCalibration() {
	b.mu.Lock()
	cancel, done := b.cal.cancel, b.cal.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (b *backend) calibrating() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cal.done != nil
}

func (b *backend) Calibration() ui.CalibrationState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.cal.state
	st.Candidates = slices.Clone(st.Candidates)
	return st
}

func (b *backend) ApplyCalibration(hostID string) error {
	b.mu.Lock()
	res, forHost := b.cal.last, b.cal.lastFor
	b.mu.Unlock()
	if res == nil || forHost != hostID {
		return ui.ErrNoCalibration
	}
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		return c.ApplyCalibration(res.HostKey(), res.Link.Route, res.Stored())
	})
	if err != nil {
		return err
	}
	b.reloadPins()
	b.changed()
	return nil
}

func (b *backend) ResetCalibration(hostID string, routes ...discovery.PathKind) error {
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		if len(routes) == 0 {
			c.ResetCalibration(hostID, "")
		}
		for _, r := range routes {
			c.ResetCalibration(hostID, r.String())
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.reloadPins()
	b.changed()
	return nil
}

func (b *backend) CalibrationStatus(hostID string) []ui.CalibrationSummary {
	b.mu.Lock()
	cal := b.pinned[hostID].Calibrated
	b.mu.Unlock()
	var out []ui.CalibrationSummary
	for _, k := range pathKinds {
		c, ok := cal[k.String()]
		if !ok {
			continue
		}
		out = append(out, ui.CalibrationSummary{
			PathKind: k, At: c.At, Overrides: c.Overrides, QueueBytes: c.ReceiveQueue.Bytes, DrainMbps: c.ReceiveQueue.DrainMbps,
			RTTMs: float64(c.Link.RTTP50Micros) / 1000, BandwidthMbps: c.Link.BandwidthMbps, GlassMs: c.GlassMs, Warnings: slices.Clone(c.Warnings),
		})
	}
	return out
}

func pathKindOf(route string) discovery.PathKind {
	for _, k := range pathKinds {
		if k.String() == route {
			return k
		}
	}
	return discovery.PathTailscaleDERP
}

func calibrationLink(l calibrate.Link) ui.CalibrationLink {
	return ui.CalibrationLink{
		PathKind: pathKindOf(l.Route), RTT: ui.StageStat(l.RTT), JitterMs: l.JitterMs, BandwidthMbps: l.BandwidthMbps, LossPct: l.LossPct,
		DatagramBytes: l.DatagramBytes, QueueDatagrams: l.QueueDatagrams, DrainMbps: l.DrainMbps,
	}
}

func calibrationCandidates(cs []calibrate.Candidate, best int) []ui.CalibrationCandidate {
	var out []ui.CalibrationCandidate
	for i, c := range cs {
		codec, _ := session.ParseCodec(c.Codec)
		out = append(out, ui.CalibrationCandidate{
			Name: c.Name, Err: c.Err, Codec: codec, BitDepth: c.BitDepth, Slices: c.Slices, Split: c.Split,
			Width: c.Width, Height: c.Height, FPS: c.FPS, Encoder: c.Encoder,
			Encode: ui.StageStat(c.Encode), Network: ui.StageStat(c.Network), Decode: ui.StageStat(c.Decode), Total: ui.StageStat(c.Total),
			BitrateMbps: c.BitrateMbps, Frames: c.Frames, BadPct: c.BadPct(), Score: c.Score, Best: i == best,
		})
	}
	return out
}

func calibrationDisplay(dp calibrate.DisplayProbe) ui.CalibrationDisplay {
	out := ui.CalibrationDisplay{Status: ui.CalibrationDisplayStatus(dp.Status), Note: dp.Note}
	for i, v := range dp.Variants {
		out.Variants = append(out.Variants, ui.CalibrationDisplayVariant{
			Name: v.Name, Err: v.Err, Glass: ui.StageStat(v.Glass), Total: ui.StageStat(v.Total), ScanOut: v.ScanOut,
			CompositedPct: v.CompositedPct, RefreshHz: v.RefreshHz, Locked: v.Locked, Best: i == dp.Best,
		})
	}
	return out
}

func calibrationResult(r calibrate.Result) ui.CalibrationResult {
	return ui.CalibrationResult{
		HostID: r.HostKey(), HostName: r.HostName, At: r.At, Link: calibrationLink(r.Link),
		Candidates: calibrationCandidates(r.Candidates, r.Best), Display: calibrationDisplay(r.Display),
		Recommended: r.Recommended, BeforeMs: r.BeforeMs, AfterMs: r.AfterMs, Warnings: slices.Clone(r.Warnings),
	}
}
