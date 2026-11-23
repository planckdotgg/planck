// Package ratectl picks the video bitrate from receiver feedback, in the
// style of Google Congestion Control: a delay-gradient trendline detects
// queue build-up early, loss caps the rate when the path drops packets, and a
// per-path ceiling bounds both.
package ratectl

import (
	"math"
	"net/netip"
	"time"
)

type Path uint8

const (
	PathUnknown Path = iota
	PathLAN
	PathTailscaleDirect
	PathTailscaleRelay
)

func (p Path) String() string {
	switch p {
	case PathLAN:
		return "lan"
	case PathTailscaleDirect:
		return "tailscale-direct"
	case PathTailscaleRelay:
		return "tailscale-relay"
	}
	return "unknown"
}

var (
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// ClassifyAddr guesses the path from the peer's address. A Tailscale address
// is assumed direct; telling a DERP relay apart needs the Tailscale LocalAPI.
func ClassifyAddr(a netip.Addr) Path {
	a = a.Unmap()
	switch {
	case tailscaleV4.Contains(a) || tailscaleV6.Contains(a):
		return PathTailscaleDirect
	case a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast():
		return PathLAN
	}
	return PathUnknown
}

type Config struct {
	MinKbps   int
	MaxKbps   int
	StartKbps int
	// TailscaleDirectKbps and TailscaleRelayKbps cap the rate on those
	// paths. Spike 3 saw Tailscale direct hold 50 to 100 Mbps with extra
	// tail latency; DERP relays are far slower.
	TailscaleDirectKbps int
	TailscaleRelayKbps  int
	// MinKeyframeInterval spaces keyframe recommendations; the effective
	// spacing is at least two RTTs.
	MinKeyframeInterval time.Duration
}

// DefaultConfig suits 1080p to 1440p game streaming.
func DefaultConfig() Config {
	return Config{
		MinKbps:             1_000,
		MaxKbps:             300_000,
		StartKbps:           20_000,
		TailscaleDirectKbps: 100_000,
		TailscaleRelayKbps:  8_000,
		MinKeyframeInterval: 100 * time.Millisecond,
	}
}

// Ack is the feedback for one video frame, in the sender's terms.
type Ack struct {
	// Now is the sender's monotonic clock when the feedback was processed.
	Now int64
	// SendTS is the sender's clock when the frame's last packet left, and
	// ArrivalTS the receiver's clock when the frame completed. Only
	// differences between frames are used, so the clocks need not agree.
	// ArrivalTS 0 means the ack carries no delay sample.
	SendTS    int64
	ArrivalTS int64
	Bytes     int
	// RTT is the current round-trip estimate; 0 if unknown.
	RTT time.Duration
	// Loss is the pre-FEC packet loss fraction in this report; negative
	// if the report has none. Reports are averaged before use.
	Loss float64
	// FrameLost reports a frame that FEC could not rebuild.
	FrameLost bool
}

type Usage uint8

const (
	UsageNormal Usage = iota
	UsageOver
	UsageUnder
)

type Decision struct {
	TargetKbps int
	// RequestKeyframe recommends a keyframe (or reference invalidation)
	// because a frame was lost.
	RequestKeyframe bool
	Usage           Usage
}

const (
	trendWindow    = 20
	smoothing      = 0.9
	trendGain      = 4.0
	maxTrendDeltas = 60
	thresholdInit  = 12.5
	thresholdUp    = 0.0087
	thresholdDown  = 0.039
	overuseTimeMs  = 10.0
	decreaseFactor = 0.85
	highLoss       = 0.10
	lowLoss        = 0.02
	// lossSmoothing averages per-frame loss reports over about 20 frames;
	// one frame's few shards make a single report far too noisy.
	lossSmoothing    = 0.05
	rateWindowNs     = int64(500 * time.Millisecond)
	defaultRTT       = 50 * time.Millisecond
	deliveredHistory = 256
	// The one-way-delay floor is the minimum over two rotating buckets, so
	// it follows route changes and clock drift within baseBucketNs to
	// 2*baseBucketNs.
	baseBucketNs = int64(5 * time.Second)
	// drainTarget is how fast a decrease aims to empty the standing queue.
	drainTarget = 0.5
	// holdQueueNs stops increases while this much queueing delay remains.
	holdQueueNs = int64(15 * time.Millisecond)
	// An application-limited sender sends under appLimitedShare of the
	// target. Its sparse, bursty frames trip the delay trendline without any
	// congestion, so it only acts on delay overuse once a standing queue of
	// appLimitedQueueNs shows the path really is full.
	appLimitedShare   = 0.7
	appLimitedQueueNs = int64(5 * time.Millisecond)
)

type point struct{ x, y float64 }

type delivery struct {
	at, sent int64
	bytes    int
}

// Controller is the sender-side bitrate controller. Not safe for concurrent
// use.
type Controller struct {
	cfg  Config
	path Path

	target       float64
	lastDecrease float64
	lastUpdate   int64
	lastCut      int64
	lastLossCut  int64
	lastKeyframe int64
	haveKeyframe bool

	haveLast    bool
	lastSend    int64
	lastArrival int64
	firstArr    int64
	acc         float64
	smoothed    float64
	points      [trendWindow]point
	npoints     int
	head        int
	deltas      int

	threshold   float64
	prevTrend   float64
	overuseMs   float64
	overuseHits int
	usage       Usage

	loss     float64
	haveLoss bool

	baseCur   int64
	basePrev  int64
	baseStart int64
	haveBase  bool
	queueNs   int64

	hist     [deliveredHistory]delivery
	histHead int
	histLen  int
	avgFrame float64
}

func New(cfg Config) *Controller {
	def := DefaultConfig()
	if cfg.MaxKbps <= 0 {
		cfg.MaxKbps = def.MaxKbps
	}
	if cfg.MinKbps <= 0 {
		cfg.MinKbps = min(def.MinKbps, cfg.MaxKbps)
	}
	if cfg.StartKbps <= 0 {
		cfg.StartKbps = min(def.StartKbps, cfg.MaxKbps)
	}
	if cfg.TailscaleDirectKbps <= 0 {
		cfg.TailscaleDirectKbps = def.TailscaleDirectKbps
	}
	if cfg.TailscaleRelayKbps <= 0 {
		cfg.TailscaleRelayKbps = def.TailscaleRelayKbps
	}
	if cfg.MinKeyframeInterval <= 0 {
		cfg.MinKeyframeInterval = def.MinKeyframeInterval
	}
	c := &Controller{cfg: cfg, threshold: thresholdInit}
	c.target = c.clamp(float64(cfg.StartKbps))
	return c
}

func (c *Controller) TargetKbps() int { return int(c.target) }
func (c *Controller) Path() Path      { return c.path }

// SetPath changes the path and applies its ceiling at once.
func (c *Controller) SetPath(p Path) {
	c.path = p
	c.target = c.clamp(c.target)
}

// Ceiling is the highest rate allowed on the current path.
func (c *Controller) Ceiling() int {
	switch c.path {
	case PathTailscaleDirect:
		return min(c.cfg.MaxKbps, c.cfg.TailscaleDirectKbps)
	case PathTailscaleRelay:
		return min(c.cfg.MaxKbps, c.cfg.TailscaleRelayKbps)
	}
	return c.cfg.MaxKbps
}

func (c *Controller) clamp(kbps float64) float64 {
	return max(float64(c.cfg.MinKbps), min(kbps, float64(c.Ceiling())))
}

// DeliveredKbps is the receive rate over the last half second of acks, or 0
// before there is enough history.
func (c *Controller) DeliveredKbps() int {
	return c.rateKbps(func(d delivery) int64 { return d.at })
}

// sentKbps is the send rate of the acked frames over the last half second.
func (c *Controller) sentKbps() int {
	return c.rateKbps(func(d delivery) int64 { return d.sent })
}

func (c *Controller) rateKbps(ts func(delivery) int64) int {
	if c.histLen < 2 {
		return 0
	}
	newest := c.hist[(c.histHead-1+deliveredHistory)%deliveredHistory]
	bytes := 0
	oldest := ts(newest)
	for i := 0; i < c.histLen; i++ {
		d := c.hist[(c.histHead-1-i+2*deliveredHistory)%deliveredHistory]
		if ts(newest)-ts(d) > rateWindowNs {
			break
		}
		oldest = ts(d)
		if i > 0 {
			bytes += d.bytes
		}
	}
	span := ts(newest) - oldest
	if span <= 0 {
		return 0
	}
	return int(float64(bytes+newest.bytes) * 8 * 1e6 / float64(span))
}

// Update folds one ack into the estimate and returns the new decision.
func (c *Controller) Update(a Ack) Decision {
	rtt := a.RTT
	if rtt <= 0 {
		rtt = defaultRTT
	}
	if a.Loss >= 0 {
		if c.haveLoss {
			c.loss += lossSmoothing * (a.Loss - c.loss)
		} else {
			c.loss, c.haveLoss = a.Loss, true
		}
	}
	if a.ArrivalTS != 0 {
		c.recordDelivery(a)
		c.trackQueue(a)
		c.detect(a)
	}
	usage := c.usage
	c.adjust(a.Now, rtt)

	d := Decision{TargetKbps: int(c.target), Usage: usage}
	if a.FrameLost {
		gap := max(c.cfg.MinKeyframeInterval, 2*rtt)
		if !c.haveKeyframe || a.Now-c.lastKeyframe >= int64(gap) {
			d.RequestKeyframe = true
			c.lastKeyframe = a.Now
			c.haveKeyframe = true
		}
	}
	return d
}

func (c *Controller) recordDelivery(a Ack) {
	c.hist[c.histHead] = delivery{at: a.ArrivalTS, sent: a.SendTS, bytes: a.Bytes}
	c.histHead = (c.histHead + 1) % deliveredHistory
	c.histLen = min(c.histLen+1, deliveredHistory)
	if c.avgFrame == 0 {
		c.avgFrame = float64(a.Bytes)
	} else {
		c.avgFrame += 0.05 * (float64(a.Bytes) - c.avgFrame)
	}
}

// trackQueue estimates the standing queue as the one-way delay above its
// recent floor. The two clocks' offset cancels out.
func (c *Controller) trackQueue(a Ack) {
	owd := a.ArrivalTS - a.SendTS
	if !c.haveBase || a.Now-c.baseStart >= baseBucketNs {
		if !c.haveBase {
			c.basePrev = owd
		} else {
			c.basePrev = c.baseCur
		}
		c.baseCur = owd
		c.baseStart = a.Now
		c.haveBase = true
	}
	c.baseCur = min(c.baseCur, owd)
	c.queueNs = max(0, owd-min(c.baseCur, c.basePrev))
}

// QueueDelay is the current standing-queue estimate.
func (c *Controller) QueueDelay() time.Duration { return time.Duration(c.queueNs) }

// detect runs the trendline estimator and overuse detector on one
// inter-frame delay sample.
func (c *Controller) detect(a Ack) {
	if !c.haveLast {
		c.haveLast = true
		c.lastSend, c.lastArrival, c.firstArr = a.SendTS, a.ArrivalTS, a.ArrivalTS
		return
	}
	if a.SendTS <= c.lastSend || a.ArrivalTS < c.lastArrival {
		return
	}
	interArrival := float64(a.ArrivalTS - c.lastArrival)
	interSend := float64(a.SendTS - c.lastSend)
	dtMs := interArrival / 1e6
	c.lastSend, c.lastArrival = a.SendTS, a.ArrivalTS

	c.acc += (interArrival - interSend) / 1e6
	c.smoothed = smoothing*c.smoothed + (1-smoothing)*c.acc
	c.points[c.head] = point{x: float64(a.ArrivalTS-c.firstArr) / 1e6, y: c.smoothed}
	c.head = (c.head + 1) % trendWindow
	c.npoints = min(c.npoints+1, trendWindow)
	c.deltas = min(c.deltas+1, maxTrendDeltas)
	if c.npoints < trendWindow {
		return
	}
	trend := c.slope() * float64(c.deltas) * trendGain

	switch {
	case trend > c.threshold:
		c.overuseMs += dtMs
		c.overuseHits++
		if c.overuseMs > overuseTimeMs && c.overuseHits > 1 && trend >= c.prevTrend {
			c.overuseMs, c.overuseHits = 0, 0
			c.usage = UsageOver
		}
	case trend < -c.threshold:
		c.overuseMs, c.overuseHits = 0, 0
		c.usage = UsageUnder
	default:
		c.overuseMs, c.overuseHits = 0, 0
		c.usage = UsageNormal
	}
	c.prevTrend = trend
	c.adaptThreshold(trend, dtMs)
}

func (c *Controller) slope() float64 {
	var sx, sy float64
	for _, p := range c.points {
		sx += p.x
		sy += p.y
	}
	mx, my := sx/trendWindow, sy/trendWindow
	var num, den float64
	for _, p := range c.points {
		num += (p.x - mx) * (p.y - my)
		den += (p.x - mx) * (p.x - mx)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func (c *Controller) adaptThreshold(trend, dtMs float64) {
	abs := math.Abs(trend)
	if abs > c.threshold+15 {
		return
	}
	k := thresholdUp
	if abs < c.threshold {
		k = thresholdDown
	}
	c.threshold += k * (abs - c.threshold) * min(dtMs, 100)
	c.threshold = max(6, min(c.threshold, 600))
}

func (c *Controller) adjust(now int64, rtt time.Duration) {
	dt := 0.0
	if c.lastUpdate != 0 {
		dt = min(float64(now-c.lastUpdate)/1e9, 1)
	}
	c.lastUpdate = now
	delivered := float64(c.DeliveredKbps())
	responseNs := int64(rtt + 100*time.Millisecond)

	if c.loss > highLoss && now-c.lastLossCut >= max(int64(300*time.Millisecond), int64(rtt)) {
		c.target *= 1 - 0.5*c.loss
		c.lastLossCut = now
		c.lastDecrease = c.target
	}

	appLimited := float64(c.sentKbps()) < appLimitedShare*c.target
	switch c.usage {
	case UsageOver:
		congested := !appLimited || c.queueNs >= appLimitedQueueNs
		if congested && now-c.lastCut >= responseNs {
			base := c.target
			if delivered > 0 {
				base = min(base, delivered)
			}
			factor := max(0.5, min(decreaseFactor, 1-float64(c.queueNs)/1e9/drainTarget))
			c.target = factor * base
			c.lastDecrease = c.target
			c.lastCut = now
		}
		c.usage = UsageNormal
	case UsageUnder:
	default:
		if c.loss > lowLoss || dt == 0 || c.queueNs > holdQueueNs {
			break
		}
		prev := c.target
		if c.lastDecrease > 0 && c.target < 1.2*c.lastDecrease {
			halfFrameKbits := c.avgFrame * 4 / 1000
			c.target += max(halfFrameKbits, 1) * dt * 1e9 / float64(responseNs)
		} else {
			c.target *= math.Pow(1.08, dt)
		}
		// What was delivered only limits growth; an encoder sending less
		// than asked is no reason to lower the target.
		if delivered > 0 {
			c.target = max(prev, min(c.target, 1.5*delivered+10))
		}
	}
	c.target = c.clamp(c.target)
}
