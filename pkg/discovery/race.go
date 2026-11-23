package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Candidate is one way to reach a host. HostID, when set, must match the
// beacon in the probe reply, so a stale address that now belongs to another
// host cannot win.
type Candidate struct {
	HostID string
	Kind   PathKind
	Addr   netip.AddrPort
}

type Result struct {
	Candidate
	Beacon                    Beacon
	ControlAddr               netip.AddrPort
	RTT                       time.Duration
	SafeShardPayload          int
	RecommendedMaxBitrateKbps int
}

// DefaultHeadStart is how long each path kind probes alone before the next,
// worse kind starts.
const DefaultHeadStart = 50 * time.Millisecond

type Racer struct {
	// HeadStart defaults to DefaultHeadStart.
	HeadStart time.Duration
	// Probe defaults to the package Probe function.
	Probe func(ctx context.Context, addr netip.AddrPort) (Beacon, time.Duration, error)
}

// Race probes candidates in tiers by kind: LAN direct first, Tailscale
// direct after one head start, Tailscale DERP after another. A tier starts
// early when every candidate started before it has failed. The first valid
// reply wins and the other probes are cancelled.
func (r Racer) Race(ctx context.Context, candidates []Candidate) (Result, error) {
	headStart := r.HeadStart
	if headStart <= 0 {
		headStart = DefaultHeadStart
	}
	probe := r.Probe
	if probe == nil {
		probe = Probe
	}

	var tiers [][]Candidate
	for _, kind := range []PathKind{PathLANDirect, PathTailscaleDirect, PathTailscaleDERP} {
		var tier []Candidate
		for _, c := range candidates {
			if c.Kind == kind {
				tier = append(tier, c)
			}
		}
		if len(tier) > 0 {
			tiers = append(tiers, tier)
		}
	}
	if len(tiers) == 0 {
		return Result{}, errors.New("discovery: no candidates")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		c      Candidate
		beacon Beacon
		rtt    time.Duration
		err    error
	}
	outcomes := make(chan outcome)
	pending, next := 0, 0
	startTier := func() {
		for _, c := range tiers[next] {
			pending++
			go func() {
				b, rtt, err := probe(ctx, c.Addr)
				select {
				case outcomes <- outcome{c, b, rtt, err}:
				case <-ctx.Done():
				}
			}()
		}
		next++
	}
	startTier()
	timer := time.NewTimer(headStart)
	defer timer.Stop()

	var errs []error
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
			if next < len(tiers) {
				startTier()
				timer.Reset(headStart)
			}
		case o := <-outcomes:
			pending--
			if o.err == nil && o.c.HostID != "" && o.beacon.HostID != o.c.HostID {
				o.err = fmt.Errorf("host ID %q, want %q", o.beacon.HostID, o.c.HostID)
			}
			if o.err == nil {
				return Result{
					Candidate:                 o.c,
					Beacon:                    o.beacon,
					ControlAddr:               netip.AddrPortFrom(o.c.Addr.Addr(), o.beacon.ControlPort),
					RTT:                       o.rtt,
					SafeShardPayload:          SafeShardPayload,
					RecommendedMaxBitrateKbps: o.c.Kind.RecommendedMaxBitrateKbps(),
				}, nil
			}
			errs = append(errs, fmt.Errorf("%s %s: %w", o.c.Kind, o.c.Addr, o.err))
			if pending == 0 {
				if next == len(tiers) {
					return Result{}, fmt.Errorf("discovery: every path failed: %w", errors.Join(errs...))
				}
				startTier()
				timer.Reset(headStart)
			}
		}
	}
}
