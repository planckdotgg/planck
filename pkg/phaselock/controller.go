package phaselock

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// ControllerConfig is what the host knows of the stream it steers.
type ControllerConfig struct {
	// FramePeriod is the stream's frame period.
	FramePeriod int64
	// Steerable is set when the source paces its own frames and can move
	// them. A display capture runs on the display's refresh, which the
	// host cannot move: capturing later only moves the wait from the
	// client to the host.
	Steerable bool
}

// State is what the Controller is doing.
type State struct {
	Active bool
	// Reason says why it is not active, or what it waits for.
	Reason string
	// ErrorNs is the phase error of the last report it acted on.
	ErrorNs int64
	// MovedNs is how far it moved the capture phase in all, later
	// positive; TrimNs the rate trim it adds to every frame.
	MovedNs, TrimNs int64
	Corrections     int
}

const (
	// Errors within deadband early or lateDeadband late are left alone:
	// the estimate's own noise. Late is tighter, where a picture misses
	// its refresh.
	deadband     = int64(300 * time.Microsecond)
	lateDeadband = int64(150 * time.Microsecond)
	// A move is gainNum/gainDen of the error: under 1, so each one lands
	// short of the target instead of past it.
	gainNum, gainDen = 7, 10
	// slewDiv: a frame moves by at most 1/slewDiv of the period, so no
	// frame interval changes by more than about 1.6%.
	slewDiv = 64
	// trimDiv bounds the rate trim to 1/trimDiv of the period a frame,
	// which covers crystal drift (a 120 Hz display measured 13 ppm off
	// the host's), and trimSpan is the least frames a drift is measured
	// over.
	trimDiv  = 2000
	trimSpan = 240
	// The rate trim takes trimNum/trimDen of the measured drift.
	trimNum, trimDen = 1, 2
	// The stream must run at the display's rate or a whole fraction of it
	// within rateTolerance.
	rateTolerance = 0.005
	maxMultiple   = 4
	// Jitter over period/noisyDiv stops steering until it is under
	// period/quietDiv.
	noisyDiv, quietDiv = 4, 6
)

// Controller turns the client's reports into small moves of the host's
// capture phase. Update and Advance may be called from different
// goroutines.
type Controller struct {
	mu  sync.Mutex
	cfg ControllerConfig
	st  State

	pending int64
	// settled is the first frame captured with every move applied.
	settled     uint32
	haveSettled bool
	noisy       bool
	// ref is the error and frame a rate drift is measured from.
	refErr   int64
	refFrame uint32
	haveRef  bool
}

func NewController(cfg ControllerConfig) *Controller {
	c := &Controller{cfg: cfg}
	if !cfg.Steerable {
		c.st.Reason = "the source runs on the display's refresh"
	} else {
		c.st.Reason = "waiting for the client"
	}
	return c
}

// State reports what the Controller is doing.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *Controller) stop(reason string) {
	c.st.Active, c.st.Reason = false, reason
	c.pending, c.st.TrimNs, c.haveRef = 0, 0, false
}

// Update takes a client report.
func (c *Controller) Update(r Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.cfg.Steerable:
		return
	case c.cfg.FramePeriod <= 0:
		c.stop("no frame rate")
		return
	case !r.Valid || r.PeriodNs <= 0:
		if !c.st.Active {
			c.st.Reason = "the client is measuring"
		}
		return
	}
	p := r.PeriodNs
	ratio := float64(c.cfg.FramePeriod) / float64(p)
	k := math.Round(ratio)
	if k < 1 || k > maxMultiple || math.Abs(ratio-k) > rateTolerance*k {
		c.stop(fmt.Sprintf("the stream's %.2f fps is not the display's %.2f Hz or a whole fraction of it", 1e9/float64(c.cfg.FramePeriod), r.RefreshHz()))
		return
	}
	if c.noisy && r.JitterNs < p/quietDiv {
		c.noisy = false
	} else if !c.noisy && r.JitterNs > p/noisyDiv {
		c.noisy = true
	}
	if c.noisy {
		c.stop(fmt.Sprintf("arrival jitter %.1f ms is too high", float64(r.JitterNs)/1e6))
		return
	}
	if c.pending != 0 || (c.haveSettled && int32(r.Oldest-c.settled) < 0) {
		// A move is under way, or the report has frames from before it.
		return
	}
	c.st.Active, c.st.Reason, c.st.ErrorNs = true, "", r.ErrorNs
	e := r.ErrorNs
	if e > deadband || e < -lateDeadband {
		c.pending = min(max(e*gainNum/gainDen, -p/2), p/2)
		c.st.Corrections++
		c.haveRef = false
		return
	}
	// Within the deadband the error moves only by drift between the two
	// clocks: trim the rate by it.
	if !c.haveRef {
		c.refErr, c.refFrame, c.haveRef = e, r.Newest, true
		return
	}
	if span := int64(int32(r.Newest - c.refFrame)); span >= trimSpan {
		drift := (e - c.refErr) / span
		lim := p / trimDiv
		c.st.TrimNs = min(max(c.st.TrimNs+drift*trimNum/trimDen, -lim), lim)
		c.refErr, c.refFrame = e, r.Newest
	}
}

// Advance is called once before each frame is captured, with its ID, and
// returns how much later than its cadence to capture it, earlier when
// below 0. Frames after it carry the move too.
func (c *Controller) Advance(frame uint32) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.st.Active {
		return 0
	}
	step := c.st.TrimNs
	if c.pending != 0 {
		lim := c.cfg.FramePeriod / slewDiv
		d := min(max(c.pending, -lim), lim)
		c.pending -= d
		step += d
		if c.pending == 0 {
			c.settled, c.haveSettled = frame+1, true
		}
	}
	c.st.MovedNs += step
	return step
}
