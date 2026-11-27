// Package calibrate measures one client, host and route and picks the
// stream settings that reach the screen soonest there: the route's round
// trip, bandwidth and receive burst queue (Phase 1, network), a short
// synthetic stream through each encoder and decoder pairing both ends can
// run (Phase 2, codec), and, in a window, the time from present to glass
// with vsync and phase lock on and off (Phase 3, display). A run takes 30
// to 45 seconds, reports its progress as it goes, and ends promptly when
// its context does.
package calibrate

import (
	"context"
	"errors"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/transport"
)

// Phase is a part of a run.
type Phase string

const (
	PhaseNetwork Phase = "network"
	PhaseCodec   Phase = "codec"
	PhaseDisplay Phase = "display"
	PhaseDone    Phase = "done"
)

// Progress is where a run is: its phase, what it measures now, its share
// done in percent, and what it measured so far.
type Progress struct {
	Phase   Phase
	Step    string
	Percent int
	// Link is filled once the network phase measured it.
	Link *Link
	// Candidates are the codec phase's results so far, in the order run.
	Candidates []Candidate
	// Display is the display phase's results so far.
	Display *DisplayProbe
}

// Stage is one pipeline stage's p50 and p99, in milliseconds.
type Stage struct {
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
}

// Link is the route as Phase 1 measured it.
type Link struct {
	// Route is the path the sessions took: a discovery.PathKind's String.
	Route string `json:"route"`
	// RTT is the round trip of media pings, and JitterMs the mean change
	// between one round trip and the next.
	RTT      Stage   `json:"rttMs"`
	JitterMs float64 `json:"jitterMs"`
	// BandwidthMbps is the rate a paced train arrived at; it is a floor
	// when the train was sent no faster.
	BandwidthMbps float64 `json:"bandwidthMbps"`
	// LossPct is the share of a paced train's datagrams lost below the
	// burst queue's limit: loss that pacing cannot avoid.
	LossPct float64 `json:"lossPct"`
	// DatagramBytes is the largest media datagram the route carried.
	DatagramBytes int `json:"datagramBytes"`
	// QueueDatagrams is how many datagrams of a back-to-back burst the
	// receiver kept before it dropped one, 0 when no burst sent dropped
	// any; DrainMbps is the fastest a burst past them arrived whole.
	QueueDatagrams int `json:"queueDatagrams"`
	DrainMbps      int `json:"drainMbps"`
	// ReceiveQueue is the queue the host paces video into, with margins
	// below what was measured; zero when no burst dropped.
	ReceiveQueue defaults.ReceiveQueue `json:"receiveQueue"`
}

// Candidate is one stream setting Phase 2 tried.
type Candidate struct {
	Name string `json:"name"`
	// Overrides are the knobs the candidate sets; the default candidate
	// sets none.
	Overrides defaults.UserOverrides `json:"overrides"`
	// Err is why it did not stream, "" when it did.
	Err string `json:"error,omitempty"`
	// What the host resolved it to.
	Codec    string `json:"codec,omitempty"`
	BitDepth int    `json:"bitDepth,omitempty"`
	Slices   int    `json:"slices,omitempty"`
	Split    bool   `json:"split,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	FPS      int    `json:"fps,omitempty"`
	Encoder  string `json:"encoder,omitempty"`
	// Stage times from the host's capture: Total is to the decoded
	// picture, as nothing is shown.
	Encode  Stage `json:"encodeMs"`
	Network Stage `json:"networkMs"`
	Decode  Stage `json:"decodeMs"`
	Total   Stage `json:"totalMs"`
	// BitrateMbps is what arrived; Frames counts the pictures decoded,
	// and Dropped and Lost those this client dropped or the route lost.
	BitrateMbps float64 `json:"bitrateMbps"`
	Frames      uint64  `json:"frames"`
	Dropped     uint64  `json:"dropped"`
	Lost        uint64  `json:"lost"`
	// Expected is how many frames the target rate asks for over the
	// measurement.
	Expected float64 `json:"expectedFrames"`
	// Score is what the pick compares, in milliseconds, lower better: see
	// score. 0 for a candidate that did not stream.
	Score float64 `json:"score,omitempty"`
}

// Viable reports whether c streamed enough to be scored.
func (c Candidate) Viable() bool { return c.Err == "" && c.Frames >= minFrames }

// DisplayStatus says how the display phase went.
type DisplayStatus string

const (
	DisplayMeasured DisplayStatus = "measured"
	// DisplaySkipped: no window shows the stream, as headless.
	DisplaySkipped DisplayStatus = "skipped"
	// DisplayUnavailable: the window's present path does not say when
	// pictures reach the screen.
	DisplayUnavailable DisplayStatus = "unavailable"
)

// DisplayProbe is Phase 3: the present-to-glass time of each variant.
type DisplayProbe struct {
	Status   DisplayStatus    `json:"status"`
	Note     string           `json:"note,omitempty"`
	Variants []DisplayVariant `json:"variants,omitempty"`
	// Best indexes Variants, -1 without one.
	Best int `json:"best"`
}

// DisplayVariant is the stream shown with one vsync and phase lock
// setting.
type DisplayVariant struct {
	Name      string                 `json:"name"`
	Overrides defaults.UserOverrides `json:"overrides"`
	Err       string                 `json:"error,omitempty"`
	// Glass is from the present call to the picture on screen, and Total
	// from the host's capture to it.
	Glass Stage `json:"glassMs"`
	Total Stage `json:"totalMs"`
	// ScanOut is "direct" or "composited", "" while unknown; CompositedPct
	// is the share of pictures a compositor showed a refresh late.
	ScanOut       string  `json:"scanOut,omitempty"`
	CompositedPct float64 `json:"compositedPct"`
	RefreshHz     float64 `json:"refreshHz,omitempty"`
	Locked        bool    `json:"locked"`
	Samples       int     `json:"samples"`
	// hints are the window's while the variant showed.
	hints *DisplayHints
}

// Result is a whole run.
type Result struct {
	HostID   string `json:"hostID"`
	HostName string `json:"hostName"`
	// Addr is the host's control address on the route.
	Addr    string    `json:"addr"`
	At      time.Time `json:"at"`
	Seconds float64   `json:"seconds"`
	Link    Link      `json:"link"`
	// Candidates are Phase 2's, the default first; Best indexes the pick,
	// -1 when none streamed.
	Candidates []Candidate  `json:"candidates"`
	Best       int          `json:"best"`
	Display    DisplayProbe `json:"display"`
	Warnings   []string     `json:"warnings,omitempty"`
	// Recommended are the knobs the run settled: the best candidate's,
	// and the best display variant's.
	Recommended defaults.UserOverrides `json:"recommended"`
	// BeforeMs and AfterMs are the default candidate's and the pick's
	// p50 from capture to the decoded picture, each plus the p50 from
	// present to glass of its display settings when Phase 3 measured them:
	// the default variant's before, the best one's after. 0 is unknown.
	BeforeMs float64 `json:"beforeMs"`
	AfterMs  float64 `json:"afterMs"`
}

// HostKey is the key client.json pins the host under: its host ID, or
// its address when it sent none.
func (r Result) HostKey() string {
	if r.HostID != "" {
		return r.HostID
	}
	return "addr:" + r.Addr
}

// Stored is r as client.json keeps it for its route. Its GlassMs is 0
// unless Phase 3 measured the time to glass: AfterMs alone ends at the
// decoded picture.
func (r Result) Stored() config.CalibratedRoute {
	glass := 0.0
	if r.Display.Status == DisplayMeasured {
		glass = r.AfterMs
	}
	return config.CalibratedRoute{
		At: r.At, Overrides: r.Recommended, ReceiveQueue: r.Link.ReceiveQueue, GlassMs: glass, Warnings: r.Warnings,
		Link: config.CalibratedLink{
			RTTP50Micros: int(r.Link.RTT.P50 * 1000), RTTP99Micros: int(r.Link.RTT.P99 * 1000), JitterMicros: int(r.Link.JitterMs * 1000),
			BandwidthMbps: int(r.Link.BandwidthMbps), DatagramBytes: r.Link.DatagramBytes,
		},
	}
}

// Conn is one calibration session; *session.Client is one.
type Conn interface {
	Stats() session.ClientStats
	ResetStats()
	HostCapabilities() *defaults.HostCapabilities
	Probe(ctx context.Context, spec transport.ProbeSpec) (transport.ProbeTrain, error)
	Ping(ctx context.Context, n int, every time.Duration) ([]time.Duration, error)
	Close()
}

// Dialer opens the run's sessions to its host. A display session shows
// the stream in the window Phase 3 measures; o is what it asks for.
type Dialer interface {
	Dial(ctx context.Context, mode session.CalibrationMode, o defaults.UserOverrides, display bool) (Conn, error)
}

// Options shape a run.
type Options struct {
	// Client is this device's capabilities, whose decoders the codec phase
	// pairs with the host's encoders.
	Client defaults.ClientCapabilities
	// User are the user's overrides for the host: a knob set there is not
	// varied, and every session asks for it.
	User defaults.UserOverrides
	// Display runs Phase 3 in the Dialer's window; off, as headless, it is
	// skipped.
	Display bool
	// Hints describe the window while a Phase 3 stream shows, for its
	// warnings; nil knows nothing of it.
	Hints func() DisplayHints
	// Timing replaces DefaultTiming.
	Timing *Timing
}

// Timing is how long each measurement runs.
type Timing struct {
	// Pings are sent PingEvery apart.
	Pings     int
	PingEvery time.Duration
	// Settle is the pause after a burst, for the receiver's queue to
	// drain.
	Settle time.Duration
	// Bandwidth is the length of the paced bandwidth train.
	Bandwidth time.Duration
	// Warmup waits for a stream's first picture, at most; Measure is how
	// long a candidate or display variant is measured after it.
	Warmup, Measure time.Duration
	// DisplayMeasure is Measure for a display variant: the phase
	// estimate wants a few hundred pictures.
	DisplayMeasure time.Duration
}

// DefaultTiming fits a run with six candidates and three display variants
// in about 40 seconds.
var DefaultTiming = Timing{
	Pings: 40, PingEvery: 10 * time.Millisecond, Settle: 30 * time.Millisecond, Bandwidth: 600 * time.Millisecond,
	Warmup: 1500 * time.Millisecond, Measure: 1600 * time.Millisecond, DisplayMeasure: 2200 * time.Millisecond,
}

// ErrNoHostCapabilities is a host that resolves no streams from both
// ends' capabilities: there is nothing to pick between.
var ErrNoHostCapabilities = errors.New("calibrate: the host sent no capabilities")

// Run calibrates the route d dials. progress, if set, hears each step; it
// runs on Run's goroutine and must not block. A cancelled ctx ends the run
// with ctx's error once the session open closes, which waits for nothing
// slower than a handshake.
func Run(ctx context.Context, d Dialer, o Options, progress func(Progress)) (Result, error) {
	r := &run{d: d, o: o, progress: progress, timing: DefaultTiming, start: time.Now()}
	if o.Timing != nil {
		r.timing = *o.Timing
	}
	r.res.At, r.res.Best, r.res.Display.Best = r.start, -1, -1
	if err := r.network(ctx); err != nil {
		return r.res, err
	}
	if err := r.codecs(ctx); err != nil {
		return r.res, err
	}
	if err := r.display(ctx); err != nil {
		return r.res, err
	}
	r.finish()
	r.report(PhaseDone, "done", 100)
	return r.res, nil
}

type run struct {
	d        Dialer
	o        Options
	progress func(Progress)
	timing   Timing
	start    time.Time
	host     defaults.HostCapabilities
	res      Result
}

func (r *run) report(p Phase, step string, pct int) {
	if r.progress == nil {
		return
	}
	pr := Progress{Phase: p, Step: step, Percent: pct, Candidates: append([]Candidate(nil), r.res.Candidates...)}
	if r.res.Link.Route != "" {
		l := r.res.Link
		pr.Link = &l
	}
	if len(r.res.Display.Variants) > 0 || r.res.Display.Status != "" {
		dp := r.res.Display
		dp.Variants = append([]DisplayVariant(nil), dp.Variants...)
		pr.Display = &dp
	}
	r.progress(pr)
}

// sleep waits d, or returns ctx's error.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finish settles the recommendation and the before and after times.
func (r *run) finish() {
	res := &r.res
	res.Seconds = time.Since(r.start).Seconds()
	if res.Best < 0 {
		res.Warnings = append(res.Warnings, "No candidate stream decoded enough pictures to compare; streams keep their defaults.")
		return
	}
	res.Recommended = res.Candidates[res.Best].Overrides
	res.AfterMs = res.Candidates[res.Best].Total.P50
	if res.Candidates[0].Viable() {
		res.BeforeMs = res.Candidates[0].Total.P50
	}
	if dp := res.Display; dp.Best >= 0 {
		res.Recommended = defaults.Merge(res.Recommended, dp.Variants[dp.Best].Overrides)
		res.AfterMs += dp.Variants[dp.Best].Glass.P50
		if res.BeforeMs > 0 && dp.Variants[0].Samples > 0 {
			res.BeforeMs += dp.Variants[0].Glass.P50
		} else {
			res.BeforeMs = 0
		}
	}
}
