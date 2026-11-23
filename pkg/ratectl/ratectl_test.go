package ratectl

import (
	"net/netip"
	"testing"
	"time"
)

// link is a FIFO bottleneck with a fixed one-way propagation delay. Frames
// are sent whole at their frame time; acks return after another propagation
// delay.
type link struct {
	capKbps func(t int64) float64
	prop    int64
	free    int64
}

type pendingAck struct {
	at  int64
	ack Ack
}

type simResult struct {
	targets []int
	queueMs []float64
	times   []int64
}

func simulate(c *Controller, l *link, fps int, dur time.Duration, loss func(frame int) (float64, bool)) simResult {
	var res simResult
	interval := int64(time.Second) / int64(fps)
	var pending []pendingAck
	for i := 0; int64(i)*interval < int64(dur); i++ {
		t := int64(i+1) * interval
		for len(pending) > 0 && pending[0].at <= t {
			a := pending[0].ack
			a.Now = pending[0].at
			c.Update(a)
			pending = pending[1:]
		}
		bytes := c.TargetKbps() * 1000 / 8 / fps
		start := max(t, l.free)
		arrival := start + int64(float64(bytes*8)/(l.capKbps(t)*1000)*1e9)
		l.free = arrival
		a := Ack{SendTS: t, ArrivalTS: arrival + l.prop, Bytes: bytes, RTT: time.Duration(2*l.prop + (start - t)), Loss: -1}
		if loss != nil {
			a.Loss, a.FrameLost = loss(i)
		}
		pending = append(pending, pendingAck{at: arrival + 2*l.prop, ack: a})
		res.targets = append(res.targets, c.TargetKbps())
		res.queueMs = append(res.queueMs, float64(start-t)/1e6)
		res.times = append(res.times, t)
	}
	return res
}

func window(r simResult, from, to time.Duration) (avgTarget, avgQueue, maxQueue float64) {
	n := 0
	for i, t := range r.times {
		if t < int64(from) || t >= int64(to) {
			continue
		}
		avgTarget += float64(r.targets[i])
		avgQueue += r.queueMs[i]
		maxQueue = max(maxQueue, r.queueMs[i])
		n++
	}
	return avgTarget / float64(n), avgQueue / float64(n), maxQueue
}

func constant(kbps float64) func(int64) float64 { return func(int64) float64 { return kbps } }

func TestConvergesBelowBottleneck(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StartKbps = 10_000
	c := New(cfg)
	r := simulate(c, &link{capKbps: constant(50_000), prop: int64(2 * time.Millisecond)}, 120, 40*time.Second, nil)
	avg, q, qmax := window(r, 25*time.Second, 40*time.Second)
	t.Logf("last 15 s: avg target %.0f kbps, avg queue %.2f ms, max queue %.2f ms", avg, q, qmax)
	if avg < 0.6*50_000 || avg > 1.05*50_000 {
		t.Fatalf("average target %.0f kbps, want 60-105%% of the 50 Mbps bottleneck", avg)
	}
	if q > 20 {
		t.Fatalf("average queueing delay %.1f ms: the controller lets the queue grow", q)
	}
}

func TestBacksOffWhenCapacityDrops(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StartKbps = 60_000
	c := New(cfg)
	capFn := func(t int64) float64 {
		if t < int64(10*time.Second) {
			return 80_000
		}
		return 30_000
	}
	r := simulate(c, &link{capKbps: capFn, prop: int64(2 * time.Millisecond)}, 120, 20*time.Second, nil)
	before, _, _ := window(r, 8*time.Second, 10*time.Second)
	after, q, _ := window(r, 12*time.Second, 20*time.Second)
	t.Logf("before drop %.0f kbps, after %.0f kbps, queue after %.2f ms", before, after, q)
	if after > 33_000 {
		t.Fatalf("target %.0f kbps after capacity fell to 30 Mbps", after)
	}
	if q > 30 {
		t.Fatalf("queue %.1f ms after the drop: did not drain", q)
	}
}

func TestGrowsOnCleanFastPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StartKbps = 10_000
	c := New(cfg)
	r := simulate(c, &link{capKbps: constant(1_000_000), prop: int64(time.Millisecond)}, 120, 10*time.Second, nil)
	if last := r.targets[len(r.targets)-1]; last < 15_000 {
		t.Fatalf("target %d kbps after 10 s on an unconstrained path, want growth from 10000", last)
	}
}

func TestLossResponse(t *testing.T) {
	run := func(loss float64) int {
		cfg := DefaultConfig()
		cfg.StartKbps = 40_000
		c := New(cfg)
		r := simulate(c, &link{capKbps: constant(1_000_000), prop: int64(time.Millisecond)}, 120, 5*time.Second,
			func(int) (float64, bool) { return loss, false })
		return r.targets[len(r.targets)-1]
	}
	if got := run(0.15); got >= 40_000/2 {
		t.Fatalf("15%% loss: target %d kbps, want a steep cut from 40000", got)
	}
	if got := run(0.02); got < 40_000 {
		t.Fatalf("2%% loss (FEC territory): target %d kbps fell below the start rate", got)
	}
	if got := run(0.05); got != 40_000 {
		t.Fatalf("5%% loss: target %d kbps, want held at 40000", got)
	}
}

func TestPathCeilings(t *testing.T) {
	c := New(DefaultConfig())
	c.SetPath(PathTailscaleRelay)
	if got := c.TargetKbps(); got != 8_000 {
		t.Fatalf("relay path: target %d, want ceiling 8000", got)
	}
	c.SetPath(PathTailscaleDirect)
	r := simulate(c, &link{capKbps: constant(1_000_000), prop: int64(time.Millisecond)}, 120, 60*time.Second, nil)
	for _, v := range r.targets {
		if v > 100_000 {
			t.Fatalf("target %d above the Tailscale direct ceiling", v)
		}
	}
	if r.targets[len(r.targets)-1] != 100_000 {
		t.Fatalf("did not reach the Tailscale direct ceiling: %d", r.targets[len(r.targets)-1])
	}
}

func TestKeyframeRecommendationIsRateLimited(t *testing.T) {
	c := New(DefaultConfig())
	ms := int64(time.Millisecond)
	if !c.Update(Ack{Now: 1000 * ms, Loss: -1, FrameLost: true, RTT: 20 * time.Millisecond}).RequestKeyframe {
		t.Fatal("lost frame did not recommend a keyframe")
	}
	if c.Update(Ack{Now: 1050 * ms, Loss: -1, FrameLost: true, RTT: 20 * time.Millisecond}).RequestKeyframe {
		t.Fatal("second recommendation within MinKeyframeInterval")
	}
	if !c.Update(Ack{Now: 1101 * ms, Loss: -1, FrameLost: true, RTT: 20 * time.Millisecond}).RequestKeyframe {
		t.Fatal("no recommendation after the interval passed")
	}
	if c.Update(Ack{Now: 2000 * ms, Loss: -1}).RequestKeyframe {
		t.Fatal("recommendation without a lost frame")
	}
}

func TestClassifyAddr(t *testing.T) {
	cases := map[string]Path{
		"100.104.23.37":      PathTailscaleDirect,
		"fd7a:115c:a1e0::1":  PathTailscaleDirect,
		"192.168.188.165":    PathLAN,
		"::ffff:10.0.0.2":    PathLAN,
		"127.0.0.1":          PathLAN,
		"fe80::1":            PathLAN,
		"8.8.8.8":            PathUnknown,
		"2001:4860:4860::88": PathUnknown,
	}
	for s, want := range cases {
		if got := ClassifyAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v, want %v", s, got, want)
		}
	}
}
