package calibrate

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// burstCounts are the back-to-back bursts Phase 1 sends, twice each,
// smallest first, until one overflows the receiver's queue: the macOS
// Thunderbolt I225 kept 127 to 158 datagrams (transport.MacReceiveQueue).
var burstCounts = []int{32, 64, 128, 256, 512, 1024, 2048}

const (
	// overflowShare: a burst that lost at least this share of what came
	// after its first loss overflowed a queue; less is random loss, which
	// no pacing avoids.
	overflowShare = 0.1
	// queueMargin and drainMargin keep the paced queue below what was
	// measured: transport.MacReceiveQueue keeps 112 of the 127 measured.
	queueMargin = 0.85
	drainMargin = 0.9
	// The drain rate is searched between minDrainMbps and the bandwidth
	// in drainSteps halvings, with trains drainTrains times the queue.
	minDrainMbps = 20
	drainSteps   = 6
	drainTrains  = 3
	// wholeShare of a train arriving counts as whole.
	wholeShare = 0.99
	// bandwidthHeadroom sends the bandwidth train above the route's
	// bitrate ceiling, which its delivered rate is then measured against.
	bandwidthHeadroom = 1.25
)

// overflowed reports whether a burst lost a queue's worth: past its first
// loss it lost at least overflowShare of the datagrams.
func overflowed(t transport.ProbeTrain) bool {
	n, p := len(t.Arrivals), t.Prefix()
	if p == n {
		return false
	}
	return float64(n-t.Received()) >= overflowShare*float64(n-p)
}

// queueDepth is the fewest datagrams a burst that overflowed kept before
// its first loss, 0 when none overflowed.
func queueDepth(bursts []transport.ProbeTrain) int {
	depth := 0
	for _, b := range bursts {
		if overflowed(b) && (depth == 0 || b.Prefix() < depth) {
			depth = b.Prefix()
		}
	}
	return depth
}

// impliedDrainMbps is the drain a whole train of n datagrams at rateMbps
// over a queue of depth needs: the queue takes depth of them, and the
// rest drain while the train lasts.
func impliedDrainMbps(rateMbps float64, n, depth int) float64 {
	if n <= depth {
		return rateMbps
	}
	return rateMbps * float64(n-depth) / float64(n)
}

// receiveQueue is the queue the host paces into for a measured depth of
// datagrams of dgram bytes and drain, with the margins; zero when no
// burst overflowed.
func receiveQueue(depth int, drainMbps float64, dgram int) defaults.ReceiveQueue {
	if depth <= 0 || drainMbps <= 0 {
		return defaults.ReceiveQueue{}
	}
	return defaults.ReceiveQueue{Bytes: max(1, int(float64(depth)*queueMargin)) * dgram, DrainMbps: max(1, int(drainMbps*drainMargin))}
}

// whole reports whether a train arrived, but for random loss, whole.
func whole(t transport.ProbeTrain) bool {
	return t.Sent > 0 && float64(t.Received()) >= wholeShare*float64(t.Sent)
}

// rttStage is the round trips' p50 and p99 and the mean change between
// consecutive ones, in milliseconds.
func rttStage(rtts []time.Duration) (Stage, float64) {
	if len(rtts) == 0 {
		return Stage{}, 0
	}
	ms := make([]float64, len(rtts))
	var jitter float64
	for i, r := range rtts {
		ms[i] = float64(r) / 1e6
		if i > 0 {
			jitter += math.Abs(ms[i] - ms[i-1])
		}
	}
	if len(ms) > 1 {
		jitter /= float64(len(ms) - 1)
	}
	return stageOf(ms), jitter
}

func stageOf(v []float64) Stage {
	s := slices.Clone(v)
	slices.Sort(s)
	at := func(p float64) float64 { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return Stage{P50: at(0.5), P99: at(0.99)}
}

// network is Phase 1, on one network calibration session.
func (r *run) network(ctx context.Context) error {
	r.report(PhaseNetwork, "connecting", 0)
	c, err := r.d.Dial(ctx, session.CalibrateNetwork, r.o.User, false)
	if err != nil {
		return err
	}
	defer c.Close()
	st := c.Stats()
	caps := c.HostCapabilities()
	if caps == nil {
		return ErrNoHostCapabilities
	}
	r.host = *caps
	r.res.HostID, r.res.HostName, r.res.Addr = st.HostID, st.HostName, st.Addr
	l := &r.res.Link
	l.Route, l.DatagramBytes = st.Path, st.DatagramBytes
	dgram := st.DatagramBytes

	r.report(PhaseNetwork, "round trip", 2)
	rtts, err := c.Ping(ctx, r.timing.Pings, r.timing.PingEvery)
	if err != nil {
		return err
	}
	l.RTT, l.JitterMs = rttStage(rtts)

	r.report(PhaseNetwork, "bandwidth", 6)
	rate := bandwidthRate(st)
	count := min(max(int(rate*1e6/8*r.timing.Bandwidth.Seconds())/dgram, 64), wire.MaxProbeCount)
	bw, err := c.Probe(ctx, transport.ProbeSpec{Count: count, Size: dgram, RateKbps: int(rate * 1000)})
	if err != nil {
		return err
	}
	if span := bw.Span(); span > 0 && bw.Received() > 1 {
		l.BandwidthMbps = min(rate, float64((bw.Received()-1)*dgram*8)/span.Seconds()/1e6)
	}
	l.LossPct = 100 * float64(bw.Sent-bw.Received()) / float64(max(bw.Sent, 1))
	if err := sleep(ctx, r.timing.Settle); err != nil {
		return err
	}

	var bursts []transport.ProbeTrain
	for i, n := range burstCounts {
		r.report(PhaseNetwork, fmt.Sprintf("burst of %d datagrams", n), 10+i)
		over := false
		for range 2 {
			b, err := c.Probe(ctx, transport.ProbeSpec{Count: n, Size: dgram})
			if err != nil {
				return err
			}
			bursts = append(bursts, b)
			over = over || overflowed(b)
			if err := sleep(ctx, r.timing.Settle); err != nil {
				return err
			}
		}
		if over {
			break
		}
	}
	l.QueueDatagrams = queueDepth(bursts)
	r.res.Warnings = append(r.res.Warnings, linkWarnings(*l)...)
	if l.QueueDatagrams == 0 {
		r.report(PhaseNetwork, "no burst overflowed", 25)
		return nil
	}

	n := min(max(drainTrains*l.QueueDatagrams, 64), 4096)
	lo, hi := float64(minDrainMbps), max(l.BandwidthMbps, rate)
	for i := range drainSteps {
		mid := (lo + hi) / 2
		r.report(PhaseNetwork, fmt.Sprintf("drain at %.0f Mbps", mid), 18+i)
		t, err := c.Probe(ctx, transport.ProbeSpec{Count: n, Size: dgram, RateKbps: int(mid * 1000)})
		if err != nil {
			return err
		}
		if whole(t) {
			lo = mid
		} else {
			hi = mid
		}
		if err := sleep(ctx, r.timing.Settle); err != nil {
			return err
		}
	}
	l.DrainMbps = int(impliedDrainMbps(lo, n, l.QueueDatagrams))
	l.ReceiveQueue = receiveQueue(l.QueueDatagrams, float64(l.DrainMbps), dgram)
	r.report(PhaseNetwork, "receive queue measured", 25)
	return nil
}

// bandwidthRate is the rate, in Mbit/s, the bandwidth train goes at: over
// the route's ceiling, and up to most of the LAN's slowest link.
func bandwidthRate(st session.ClientStats) float64 {
	rate := float64(st.MaxBitrateKbps) / 1000 * bandwidthHeadroom
	if rate <= 0 {
		rate = float64(discovery.LANMaxBitrateKbps) / 1000
	}
	if p := st.Profile; p != nil && p.Route == defaults.RouteLAN && p.LinkMbps > 0 {
		rate = max(rate, 0.9*float64(p.LinkMbps))
	}
	return rate
}
