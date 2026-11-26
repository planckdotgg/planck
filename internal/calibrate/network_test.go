package calibrate

import (
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/transport"
)

// queueSim is a receiver behind a queue of depth datagrams that drains at
// drainMbps, fed from a sender whose line runs at lineMbps, with every
// lossEvery-th datagram lost on the way (0 none).
type queueSim struct {
	depth                int
	drainMbps, lineMbps  float64
	lossEvery            int
	sent, droppedByQueue int
}

func (q *queueSim) train(spec transport.ProbeSpec) transport.ProbeTrain {
	rate := float64(spec.RateKbps) / 1000
	if rate <= 0 || rate > q.lineMbps {
		rate = q.lineMbps
	}
	gap := float64(spec.Size*8) / (rate * 1e6) * 1e9
	drain := q.drainMbps * 1e6 / float64(spec.Size*8) / 1e9
	t := transport.ProbeTrain{Spec: spec, Sent: spec.Count, SendNs: int64(gap * float64(spec.Count-1)), Arrivals: make([]int64, spec.Count)}
	var held, last float64
	for i := range spec.Count {
		now := gap * float64(i)
		held = max(0, held-(now-last)*drain)
		last = now
		q.sent++
		if q.lossEvery > 0 && q.sent%q.lossEvery == 0 {
			continue
		}
		if held+1 > float64(q.depth) {
			q.droppedByQueue++
			continue
		}
		held++
		t.Arrivals[i] = int64(1e6 + now + held/drain)
	}
	return t
}

func TestOverflowTellsQueueFromRandomLoss(t *testing.T) {
	mac := &queueSim{depth: 140, drainMbps: 175, lineMbps: 2500}
	burst := mac.train(transport.ProbeSpec{Count: 512, Size: 1452})
	if !overflowed(burst) {
		t.Fatalf("a 512 burst into a 140 queue did not overflow: %d arrived, prefix %d", burst.Received(), burst.Prefix())
	}
	if p := burst.Prefix(); p < 140 || p > 160 {
		t.Fatalf("prefix %d, want the queue plus what drained during the burst", p)
	}
	wifi := &queueSim{depth: 1 << 20, drainMbps: 1000, lineMbps: 1000, lossEvery: 300}
	random := wifi.train(transport.ProbeSpec{Count: 512, Size: 1452})
	if random.Received() == 512 || overflowed(random) {
		t.Fatalf("one random loss in 512 read as an overflow: received %d", random.Received())
	}
	whole := (&queueSim{depth: 1 << 20, drainMbps: 2500, lineMbps: 2500}).train(transport.ProbeSpec{Count: 2048, Size: 1452})
	if overflowed(whole) {
		t.Fatal("a whole burst overflowed")
	}
	if d := queueDepth([]transport.ProbeTrain{whole, random, burst}); d != burst.Prefix() {
		t.Fatalf("depth %d, want %d", d, burst.Prefix())
	}
	if d := queueDepth([]transport.ProbeTrain{whole, random}); d != 0 {
		t.Fatalf("depth %d without an overflow", d)
	}
}

func TestReceiveQueueMargins(t *testing.T) {
	q := receiveQueue(132, 180, 1452)
	if q != (defaults.ReceiveQueue{Bytes: 112 * 1452, DrainMbps: 162}) {
		t.Fatalf("queue %+v", q)
	}
	if q := receiveQueue(0, 180, 1452); q != (defaults.ReceiveQueue{}) {
		t.Fatalf("no overflow gave %+v", q)
	}
	// A whole train of 300 at 240 Mbit/s over a 100-datagram queue drains
	// 200 of them while it lasts.
	if d := impliedDrainMbps(240, 300, 100); d != 160 {
		t.Fatalf("drain %.1f", d)
	}
}

func TestRTTStage(t *testing.T) {
	ms := func(v ...float64) []time.Duration {
		var out []time.Duration
		for _, x := range v {
			out = append(out, time.Duration(x*1e6))
		}
		return out
	}
	s, jitter := rttStage(ms(1, 3, 1, 3, 1))
	if s.P50 != 1 || s.P99 != 3 || jitter != 2 {
		t.Fatalf("stage %+v jitter %v", s, jitter)
	}
	if s, j := rttStage(nil); s != (Stage{}) || j != 0 {
		t.Fatalf("no samples: %+v %v", s, j)
	}
}
