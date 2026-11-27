package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/planckdotgg/planck/pkg/defaults"
)

// CalibratedRoute is what calibration measured on one route to a pinned
// host, and the stream settings it picked there.
type CalibratedRoute struct {
	At time.Time `json:"at"`
	// Overrides are the knobs calibration settled; ResolveOverrides puts
	// the user's own on top.
	Overrides defaults.UserOverrides `json:"overrides"`
	// ReceiveQueue is this client's receive queue on the route, which the
	// host paces video into; Bytes 0 found no limit.
	ReceiveQueue defaults.ReceiveQueue `json:"receiveQueue"`
	Link         CalibratedLink        `json:"link"`
	// GlassMs is the capture-to-glass p50 calibration measured for its
	// pick; 0 is unknown, as when its display phase did not run.
	GlassMs  float64  `json:"glassMs,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// CalibratedLink is the route as calibration measured it.
type CalibratedLink struct {
	RTTP50Micros  int `json:"rttP50Micros"`
	RTTP99Micros  int `json:"rttP99Micros"`
	JitterMicros  int `json:"jitterMicros"`
	BandwidthMbps int `json:"bandwidthMbps"`
	DatagramBytes int `json:"datagramBytes"`
}

// ResolveOverrides is what a stream on the route asks for: calibration's
// knobs where user leaves them auto, user's everywhere else.
func (r CalibratedRoute) ResolveOverrides(user defaults.UserOverrides) defaults.UserOverrides {
	return defaults.Merge(r.Overrides, user)
}

// Route is base with what calibration measured of the route.
func (r CalibratedRoute) Route(base defaults.RouteInfo) defaults.RouteInfo {
	q := r.ReceiveQueue
	base.ReceiveQueue = &q
	if r.Link.BandwidthMbps > 0 {
		base.ProbedMbps = r.Link.BandwidthMbps
	}
	if r.Link.RTTP50Micros > 0 {
		base.RTTMicros = r.Link.RTTP50Micros
	}
	return base
}

func (r CalibratedRoute) validate() error {
	if err := r.Overrides.Validate(); err != nil {
		return err
	}
	if r.ReceiveQueue.Bytes < 0 || r.ReceiveQueue.DrainMbps < 0 || (r.ReceiveQueue.Bytes > 0) != (r.ReceiveQueue.DrainMbps > 0) {
		return fmt.Errorf("receive queue %+v: want both set or neither", r.ReceiveQueue)
	}
	return nil
}

// Calibration is the calibration kept for the pinned host hostKey on
// route, a discovery.PathKind's String.
func (c ClientConfig) Calibration(hostKey, route string) (CalibratedRoute, bool) {
	r, ok := c.PinnedHosts[hostKey].Calibrated[route]
	return r, ok
}

// ApplyCalibration keeps r for the pinned host hostKey on route, in place
// of any before it.
func (c *ClientConfig) ApplyCalibration(hostKey, route string, r CalibratedRoute) error {
	p, ok := c.PinnedHosts[hostKey]
	switch {
	case !ok:
		return fmt.Errorf("%w: %s", ErrUnknownHost, hostKey)
	case route == "":
		return errors.New("config: calibration needs a route")
	}
	if err := r.validate(); err != nil {
		return fmt.Errorf("config: calibration of %s on %s: %w", hostKey, route, err)
	}
	p.Calibrated = maps.Clone(p.Calibrated)
	if p.Calibrated == nil {
		p.Calibrated = map[string]CalibratedRoute{}
	}
	p.Calibrated[route] = r
	c.PinnedHosts[hostKey] = p
	return nil
}

// ResetCalibration forgets the calibration of hostKey on route, or on
// every route when route is "", so its streams take the defaults again.
func (c *ClientConfig) ResetCalibration(hostKey, route string) {
	p, ok := c.PinnedHosts[hostKey]
	if !ok || p.Calibrated == nil {
		return
	}
	if route == "" {
		p.Calibrated = nil
	} else {
		p.Calibrated = maps.Clone(p.Calibrated)
		delete(p.Calibrated, route)
		if len(p.Calibrated) == 0 {
			p.Calibrated = nil
		}
	}
	c.PinnedHosts[hostKey] = p
}

func (c ClientConfig) validateCalibrations() error {
	for _, key := range slices.Sorted(maps.Keys(c.PinnedHosts)) {
		cal := c.PinnedHosts[key].Calibrated
		for _, route := range slices.Sorted(maps.Keys(cal)) {
			if err := cal[route].validate(); err != nil {
				return fmt.Errorf("config: client: host %s calibration on %s: %w", key, route, err)
			}
		}
	}
	return nil
}
