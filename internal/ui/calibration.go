package ui

import (
	"errors"
	"time"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

var (
	// ErrCalibrating is StartCalibration's and Connect's answer while a
	// calibration runs.
	ErrCalibrating = errors.New("a calibration is running")
	// ErrStreaming is StartCalibration's answer while a stream runs: the
	// host serves one client at a time.
	ErrStreaming = errors.New("end the stream before calibrating")
	// ErrNoCalibration is ApplyCalibration's answer for a host with no
	// finished calibration this run.
	ErrNoCalibration = errors.New("no finished calibration for this host")
)

// CalibrationPhase is the part of a calibration that runs.
type CalibrationPhase string

const (
	// CalibratingNetwork measures the route: round trip, bandwidth and
	// this device's receive burst queue.
	CalibratingNetwork CalibrationPhase = "network"
	// CalibratingCodec streams a synthetic picture through each encoder
	// and decoder pairing both ends can run.
	CalibratingCodec CalibrationPhase = "codec"
	// CalibratingDisplay shows the pick in the stream window and times
	// present to glass with vsync and phase lock on and off.
	CalibratingDisplay  CalibrationPhase = "display"
	CalibrationFinished CalibrationPhase = "done"
)

// CalibrationState is the calibration running now, or the last one this
// run: the calibration screen's live view.
type CalibrationState struct {
	// Running is set from StartCalibration until the run ends.
	Running  bool
	HostID   string
	HostName string
	Phase    CalibrationPhase
	// Step says what the phase measures now, as "burst of 256 datagrams";
	// Percent is the whole run's share done, 0 to 100.
	Step    string
	Percent int
	// Link, Candidates and Display fill in as the run measures them.
	Link       *CalibrationLink
	Candidates []CalibrationCandidate
	Display    *CalibrationDisplay
	// Result is the finished run, nil while it runs or when it failed.
	Result *CalibrationResult
	// Err is why the run failed, Cancelled set when CancelCalibration
	// ended it.
	Err       string
	Cancelled bool
}

// CalibrationLink is the route as calibration measured it.
type CalibrationLink struct {
	PathKind discovery.PathKind
	RTT      StageStat
	JitterMs float64
	// BandwidthMbps is the rate a paced train arrived at, and LossPct the
	// share of it lost.
	BandwidthMbps float64
	LossPct       float64
	DatagramBytes int
	// QueueDatagrams is how many datagrams of a back-to-back burst this
	// device kept before it dropped one, 0 when no burst dropped any;
	// DrainMbps is how fast it drains past them.
	QueueDatagrams, DrainMbps int
}

// CalibrationCandidate is one stream setting the codec phase tried.
type CalibrationCandidate struct {
	Name string
	// Err is why it did not stream, "" when it did.
	Err string
	// What the host resolved it to.
	Codec              encode.Codec
	BitDepth           int
	Slices             int
	Split              bool
	Width, Height, FPS int
	Encoder            string
	Encode, Network    StageStat
	Decode, Total      StageStat
	BitrateMbps        float64
	Frames             uint64
	// BadPct is the share of the frames the target rate asks for that
	// were dropped, lost or never came.
	BadPct float64
	// Score is what the pick compares, in milliseconds, lower better.
	Score float64
	// Best marks the pick.
	Best bool
}

// CalibrationDisplayStatus says how the display phase went.
type CalibrationDisplayStatus string

const (
	CalibrationDisplayMeasured CalibrationDisplayStatus = "measured"
	// CalibrationDisplaySkipped: nothing streamed to show.
	CalibrationDisplaySkipped CalibrationDisplayStatus = "skipped"
	// CalibrationDisplayUnavailable: the window's present path does not
	// say when pictures reach the screen.
	CalibrationDisplayUnavailable CalibrationDisplayStatus = "unavailable"
)

// CalibrationDisplay is the display phase.
type CalibrationDisplay struct {
	Status   CalibrationDisplayStatus
	Note     string
	Variants []CalibrationDisplayVariant
}

// CalibrationDisplayVariant is the pick shown with one vsync and phase
// lock setting.
type CalibrationDisplayVariant struct {
	Name string
	Err  string
	// Glass is from the present call to the picture on screen, Total from
	// the host's capture to it.
	Glass, Total StageStat
	// ScanOut is "direct" or "composited", "" while unknown.
	ScanOut       string
	CompositedPct float64
	RefreshHz     float64
	Locked        bool
	Best          bool
}

// CalibrationResult is a finished calibration, before ApplyCalibration
// keeps it.
type CalibrationResult struct {
	HostID, HostName string
	At               time.Time
	Link             CalibrationLink
	Candidates       []CalibrationCandidate
	Display          CalibrationDisplay
	// Recommended are the knobs it settled; a knob the user set beats it.
	Recommended defaults.UserOverrides
	// BeforeMs and AfterMs are the default's and the pick's p50 from the
	// host's capture to the decoded picture, plus present to glass when
	// the display phase measured it; 0 is unknown.
	BeforeMs, AfterMs float64
	// Warnings say what costs latency and how to fix it, such as a
	// composited window or a relayed route.
	Warnings []string
}

// CalibrationSummary is what this client keeps for one route to a host.
type CalibrationSummary struct {
	PathKind  discovery.PathKind
	At        time.Time
	Overrides defaults.UserOverrides
	// QueueBytes and DrainMbps are the receive queue the host paces into,
	// 0 when calibration found no limit.
	QueueBytes, DrainMbps int
	RTTMs                 float64
	BandwidthMbps         int
	// GlassMs is the pick's AfterMs.
	GlassMs  float64
	Warnings []string
}
