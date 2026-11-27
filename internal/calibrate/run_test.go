package calibrate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/gpu"
	"github.com/planckdotgg/planck/pkg/phaselock"
	"github.com/planckdotgg/planck/pkg/transport"
)

var nvidia = gpu.Adapter{LUID: 7, Vendor: gpu.VendorNVIDIA, Name: "NVIDIA GeForce RTX 4080"}

func hostCaps() defaults.HostCapabilities {
	return defaults.HostCapabilities{OS: "windows", Encoders: []defaults.EncoderCap{
		{Adapter: nvidia, API: defaults.APINVENC, Engines: 2, SubFrame: true,
			Codecs: []defaults.CodecCap{{Codec: defaults.CodecH264}, {Codec: defaults.CodecHEVC, TenBit: true}, {Codec: defaults.CodecAV1, TenBit: true}}},
		{Adapter: nvidia, API: defaults.APIPyroWave, Engines: 1, Codecs: []defaults.CodecCap{{Codec: defaults.CodecPyroWave, TenBit: true}}},
	}}
}

func clientCaps() defaults.ClientCapabilities {
	return defaults.ClientCapabilities{OS: "darwin", Decoders: []defaults.CodecCap{
		{Codec: defaults.CodecHEVC, TenBit: true}, {Codec: defaults.CodecAV1, TenBit: true}, {Codec: defaults.CodecPyroWave}}}
}

func names(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestCandidates(t *testing.T) {
	lan := discovery.PathLANDirect.String()
	got := names(candidates(hostCaps(), clientCaps(), lan, defaults.UserOverrides{}))
	want := []string{"Default", "HEVC, 8-bit", "HEVC, 10-bit", "AV1, 8-bit", "PyroWave", "HEVC, 8-bit, NVENC split", "HEVC, 8-bit, 1 slice"}
	if !slices.Equal(got, want) {
		t.Fatalf("LAN candidates %q,\nwant %q", got, want)
	}
	ts := names(candidates(hostCaps(), clientCaps(), discovery.PathTailscaleDirect.String(), defaults.UserOverrides{}))
	if slices.Contains(ts, "PyroWave") {
		t.Fatalf("PyroWave off the LAN: %q", ts)
	}
	noAV1 := clientCaps()
	noAV1.Decoders = noAV1.Decoders[:1]
	if got := names(candidates(hostCaps(), noAV1, lan, defaults.UserOverrides{})); slices.ContainsFunc(got, func(n string) bool { return strings.HasPrefix(n, "AV1") }) {
		t.Fatalf("AV1 with no AV1 decoder: %q", got)
	}
	// A knob the user set is never varied.
	user := defaults.UserOverrides{Codec: "hevc", BitDepth: "10", NVENC2EngineSplit: "off", SubFrameSlices: "4"}
	if got := candidates(hostCaps(), clientCaps(), lan, user); len(got) != 1 {
		t.Fatalf("every knob set by the user, still candidates %q", names(got))
	}
	for _, c := range candidates(hostCaps(), clientCaps(), lan, defaults.UserOverrides{BitDepth: "8"}) {
		if c.Overrides.BitDepth != "" {
			t.Fatalf("bit depth varied over the user's: %+v", c)
		}
	}
}

func TestPick(t *testing.T) {
	ok := func(name, codec string, depth int, p50, p99 float64, frames, dropped uint64) Candidate {
		c := Candidate{Name: name, Codec: codec, BitDepth: depth, Total: Stage{p50, p99}, Frames: frames, Dropped: dropped, Expected: 200}
		c.Score = score(c)
		return c
	}
	fast := ok("fast but dropping", "pyrowave", 8, 4, 5, 200, 20)
	steady := ok("steady", "hevc", 8, 6, 7, 200, 0)
	if i := pick([]Candidate{steady, fast}); i != 0 {
		t.Fatalf("picked %d: %.2f vs %.2f; 10%% drops must cost more than 2 ms", i, steady.Score, fast.Score)
	}
	short := ok("missing frames", "hevc", 8, 5, 6, 150, 0)
	if short.BadPct() != 25 || pick([]Candidate{steady, short}) != 0 {
		t.Fatalf("a stream a quarter short of its rate: bad %.0f%%, score %.2f vs %.2f", short.BadPct(), short.Score, steady.Score)
	}
	av1 := ok("av1", "av1", 8, 6.3, 7, 200, 0)
	if i := pick([]Candidate{steady, av1}); i != 1 {
		t.Fatal("AV1 within the tie did not win on efficiency")
	}
	if i := pick([]Candidate{{Name: "failed", Err: "no picture arrived"}, {Name: "few", Frames: 3}}); i != -1 {
		t.Fatalf("picked %d of none viable", i)
	}
}

func TestDisplayWarnings(t *testing.T) {
	composited := DisplayVariant{ScanOut: "composited", CompositedPct: 97, RefreshHz: 120}
	all := func(ws []string) string { return strings.Join(ws, " ") }
	if w := all(displayWarnings(composited, &DisplayHints{Fullscreen: false})); !strings.Contains(w, "8.3 ms") || !strings.Contains(w, "not full screen") {
		t.Fatalf("windowed: %q", w)
	}
	if w := all(displayWarnings(composited, &DisplayHints{Fullscreen: true, OverlayOpen: true})); !strings.Contains(w, "HUD") {
		t.Fatalf("HUD open: %q", w)
	}
	if w := all(displayWarnings(composited, &DisplayHints{Fullscreen: true})); !strings.Contains(w, "overlays") {
		t.Fatalf("full screen, nothing open: %q", w)
	}
	if w := all(displayWarnings(composited, nil)); !strings.Contains(w, "overlays") {
		t.Fatalf("no hints: %q", w)
	}
	if w := displayWarnings(DisplayVariant{ScanOut: "direct", CompositedPct: 2, RefreshHz: 120}, &DisplayHints{}); w != nil {
		t.Fatalf("direct scan-out warned %q", w)
	}
}

func TestLinkWarnings(t *testing.T) {
	if w := linkWarnings(Link{Route: discovery.PathLANDirect.String(), JitterMs: 0.1}); w != nil {
		t.Fatalf("a clean LAN warned %q", w)
	}
	w := strings.Join(linkWarnings(Link{Route: discovery.PathTailscaleDERP.String(), JitterMs: 4, LossPct: 3}), " ")
	for _, want := range []string{"relay", "Wi-Fi", "3.0%"} {
		if !strings.Contains(w, want) {
			t.Fatalf("missing %q in %q", want, w)
		}
	}
}

// perf is how a scripted stream does.
type perf struct {
	p50, p99 float64
	dropped  uint64
	glass    float64
	scanOut  phaselock.Mode
}

// fakeDialer scripts the sessions of a run: probes go through sim, and a
// stream's stats come from perfOf.
type fakeDialer struct {
	sim    *queueSim
	perfOf func(o defaults.UserOverrides, display bool) perf
	// block makes Probe wait for its context.
	block  bool
	failAt string

	mu     sync.Mutex
	dials  []defaults.UserOverrides
	open   atomic.Int32
	closed atomic.Int32
}

type fakeConn struct {
	d       *fakeDialer
	mode    session.CalibrationMode
	o       defaults.UserOverrides
	display bool
	frames  atomic.Uint64
}

func (d *fakeDialer) Dial(ctx context.Context, mode session.CalibrationMode, o defaults.UserOverrides, display bool) (Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	if mode == session.CalibrateVideo {
		d.dials = append(d.dials, o)
	}
	d.mu.Unlock()
	if d.failAt != "" && string(o.Codec) == d.failAt {
		return nil, errors.New("encoder open failed")
	}
	d.open.Add(1)
	return &fakeConn{d: d, mode: mode, o: o, display: display}, nil
}

func (c *fakeConn) Stats() session.ClientStats {
	st := session.ClientStats{Connected: true, HostID: "host-1", HostName: "bigboi", Path: discovery.PathLANDirect.String(),
		DatagramBytes: 1452, MaxBitrateKbps: 150_000}
	prof := &defaults.ResolvedProfile{Route: defaults.RouteLAN, LinkMbps: 2500, Codec: defaults.CodecHEVC, BitDepth: 8, FPS: 120, Width: 1920, Height: 1080, Slices: 4}
	switch c.o.Codec {
	case "av1":
		prof.Codec = defaults.CodecAV1
	case "pyrowave":
		prof.Codec = defaults.CodecPyroWave
	}
	if c.o.BitDepth == "10" {
		prof.BitDepth = 10
	}
	prof.Split = c.o.NVENC2EngineSplit == "on"
	st.Profile = prof
	if c.mode == session.CalibrateVideo {
		p := c.d.perfOf(c.o, c.display)
		n := c.frames.Add(200)
		st.FramesReceived, st.FramesDropped = n, p.dropped
		st.E2E = session.Latency{P50: p.p50, P99: p.p99, Samples: 200}
		if c.display && p.glass > 0 {
			st.Glass = session.Latency{P50: p.glass, P99: p.glass + 1, Samples: 200}
			st.E2EGlass = session.Latency{P50: p.p50 + p.glass, P99: p.p99 + p.glass + 1, Samples: 200}
			st.Phase = phaselock.Report{Valid: true, Mode: p.scanOut, PeriodNs: 8_333_333, CompositedPct: map[bool]float64{true: 98}[p.scanOut == phaselock.ModeComposited]}
		}
	}
	return st
}

func (c *fakeConn) ResetStats() { c.frames.Store(0) }

func (c *fakeConn) HostCapabilities() *defaults.HostCapabilities {
	h := hostCaps()
	return &h
}

func (c *fakeConn) Probe(ctx context.Context, spec transport.ProbeSpec) (transport.ProbeTrain, error) {
	if c.d.block {
		<-ctx.Done()
		return transport.ProbeTrain{}, ctx.Err()
	}
	return c.d.sim.train(spec), nil
}

func (c *fakeConn) Ping(ctx context.Context, n int, every time.Duration) ([]time.Duration, error) {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Duration(400+20*(i%2)) * time.Microsecond
	}
	return out, nil
}

func (c *fakeConn) Close() { c.d.closed.Add(1) }

var fastTiming = Timing{Pings: 4, PingEvery: time.Millisecond, Settle: time.Millisecond, Bandwidth: 10 * time.Millisecond,
	Warmup: time.Second, Measure: 5 * time.Millisecond, DisplayMeasure: 5 * time.Millisecond}

func scripted(o defaults.UserOverrides, display bool) perf {
	p := perf{p50: 6, p99: 7, glass: 9, scanOut: phaselock.ModeDirect}
	switch {
	case o.Codec == "pyrowave":
		p.p50, p.p99, p.dropped = 3, 4, 30
	case o.SubFrameSlices == "1":
		p.p50 = 7
	case o.NVENC2EngineSplit == "on":
		p.p50, p.p99 = 4.5, 5.5
	}
	if o.VSyncMode == "on" {
		p.glass = 14
	}
	if o.PhaseLock == "off" {
		p.glass = 11
	}
	return p
}

func TestRunScripted(t *testing.T) {
	d := &fakeDialer{sim: &queueSim{depth: 140, drainMbps: 175, lineMbps: 2500}, perfOf: scripted}
	var phases []Phase
	last := -1
	res, err := Run(context.Background(), d, Options{Client: clientCaps(), User: defaults.UserOverrides{FPS: "120"}, Display: true, Timing: &fastTiming},
		func(p Progress) {
			if p.Percent < last {
				t.Errorf("progress went back from %d to %d at %s", last, p.Percent, p.Step)
			}
			last = p.Percent
			if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
				phases = append(phases, p.Phase)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(phases, []Phase{PhaseNetwork, PhaseCodec, PhaseDisplay, PhaseDone}) || last != 100 {
		t.Fatalf("phases %v, last %d%%", phases, last)
	}
	l := res.Link
	if l.Route != "lan-direct" || l.RTT.P50 != 0.42 || l.QueueDatagrams < 140 || l.QueueDatagrams > 160 {
		t.Fatalf("link %+v", l)
	}
	if l.DrainMbps < 150 || l.DrainMbps > 200 || l.ReceiveQueue.Bytes == 0 || l.ReceiveQueue.Bytes > 140*1452 {
		t.Fatalf("drain %d Mbps, queue %+v; the receiver drains 175", l.DrainMbps, l.ReceiveQueue)
	}
	if res.Best < 0 || res.Candidates[res.Best].Name != "HEVC, 8-bit, NVENC split" {
		t.Fatalf("best %d of %q", res.Best, names(res.Candidates))
	}
	if res.Display.Status != DisplayMeasured || res.Display.Variants[res.Display.Best].Name != "Default" || len(res.Display.Variants) != 3 {
		t.Fatalf("display %+v", res.Display)
	}
	want := defaults.UserOverrides{Codec: "hevc", BitDepth: "8", NVENC2EngineSplit: "on"}
	if res.Recommended != want {
		t.Fatalf("recommended %+v, want %+v", res.Recommended, want)
	}
	if res.BeforeMs != 6+9 || res.AfterMs != 4.5+9 {
		t.Fatalf("before %.1f after %.1f ms", res.BeforeMs, res.AfterMs)
	}
	for _, o := range d.dials {
		if o.FPS != "120" {
			t.Fatalf("a session dropped the user's fps: %+v", o)
		}
	}
	if d.open.Load() != d.closed.Load() {
		t.Fatalf("%d sessions opened, %d closed", d.open.Load(), d.closed.Load())
	}
	stored := res.Stored()
	if stored.Overrides != want || stored.ReceiveQueue != l.ReceiveQueue || stored.Link.RTTP50Micros != 420 || stored.GlassMs != res.AfterMs {
		t.Fatalf("stored %+v", stored)
	}
}

func TestRunHeadlessAndFallback(t *testing.T) {
	d := &fakeDialer{sim: &queueSim{depth: 1 << 20, drainMbps: 10_000, lineMbps: 10_000}, perfOf: scripted, failAt: "pyrowave"}
	res, err := Run(context.Background(), d, Options{Client: clientCaps(), Timing: &fastTiming}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Display.Status != DisplaySkipped || !strings.Contains(res.Display.Note, "headless") {
		t.Fatalf("headless display %+v", res.Display)
	}
	if res.Link.QueueDatagrams != 0 || res.Link.ReceiveQueue != (defaults.ReceiveQueue{}) {
		t.Fatalf("a receiver with no queue limit: %+v", res.Link)
	}
	for _, c := range res.Candidates {
		if c.Name == "PyroWave" && c.Err == "" {
			t.Fatal("the failed candidate has no error")
		}
	}
	if res.Best < 0 {
		t.Fatal("no pick with candidates streaming")
	}

	none := &fakeDialer{sim: d.sim, perfOf: scripted}
	none.failAt = "hevc"
	res, err = Run(context.Background(), &onlyFails{none}, Options{Client: clientCaps(), Timing: &fastTiming}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Best != -1 || res.Recommended != (defaults.UserOverrides{}) || !strings.Contains(strings.Join(res.Warnings, " "), "keep their defaults") {
		t.Fatalf("no candidate streamed: best %d, recommended %+v, warnings %q", res.Best, res.Recommended, res.Warnings)
	}
}

// onlyFails fails every video session.
type onlyFails struct{ *fakeDialer }

func (d *onlyFails) Dial(ctx context.Context, mode session.CalibrationMode, o defaults.UserOverrides, display bool) (Conn, error) {
	if mode == session.CalibrateVideo {
		return nil, errors.New("no encoder")
	}
	return d.fakeDialer.Dial(ctx, mode, o, display)
}

// TestRunCancel: cancelling a run mid-phase ends it at once with the
// context's error, and closes the session it had open.
func TestRunCancel(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    *fakeDialer
		opt  Options
	}{
		{"network", &fakeDialer{sim: &queueSim{depth: 140, drainMbps: 175, lineMbps: 2500}, perfOf: scripted, block: true}, Options{Client: clientCaps(), Timing: &fastTiming}},
		{"codec", &fakeDialer{sim: &queueSim{depth: 140, drainMbps: 175, lineMbps: 2500}, perfOf: scripted},
			Options{Client: clientCaps(), Timing: &Timing{Pings: 1, PingEvery: time.Millisecond, Warmup: time.Second, Measure: time.Hour, DisplayMeasure: time.Hour}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			errc := make(chan error, 1)
			var phase atomic.Value
			go func() {
				_, err := Run(ctx, tc.d, tc.opt, func(p Progress) { phase.Store(p.Phase) })
				errc <- err
			}()
			deadline := time.Now().Add(2 * time.Second)
			for phase.Load() != Phase(tc.name) || tc.d.open.Load() == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("never reached the %s phase", tc.name)
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			start := time.Now()
			cancel()
			select {
			case err := <-errc:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled run: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("the cancelled run did not end")
			}
			if d := time.Since(start); d > 100*time.Millisecond {
				t.Fatalf("cancel took %v", d)
			}
			if tc.d.open.Load() != tc.d.closed.Load() {
				t.Fatalf("%d sessions opened, %d closed", tc.d.open.Load(), tc.d.closed.Load())
			}
		})
	}
}

// TestStoredGlassOnlyWhenMeasured: client.json keeps a time to glass only
// from a run whose display phase measured one; AfterMs without it ends
// at the decoded picture.
func TestStoredGlassOnlyWhenMeasured(t *testing.T) {
	for _, st := range []DisplayStatus{DisplaySkipped, DisplayUnavailable} {
		r := Result{AfterMs: 5.66, Best: 0, Display: DisplayProbe{Status: st, Best: -1}}
		if g := r.Stored().GlassMs; g != 0 {
			t.Errorf("display %s: stored glassMs %v", st, g)
		}
	}
	r := Result{AfterMs: 13.5, Best: 0, Display: DisplayProbe{Status: DisplayMeasured, Best: 0}}
	if g := r.Stored().GlassMs; g != 13.5 {
		t.Errorf("display measured: stored glassMs %v, want 13.5", g)
	}
}

func TestRunHeadlessStoresNoGlass(t *testing.T) {
	d := &fakeDialer{sim: &queueSim{depth: 1 << 20, drainMbps: 10_000, lineMbps: 10_000}, perfOf: scripted}
	res, err := Run(context.Background(), d, Options{Client: clientCaps(), Timing: &fastTiming}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.AfterMs <= 0 || res.Display.Status != DisplaySkipped {
		t.Fatalf("after %v ms, display %+v", res.AfterMs, res.Display)
	}
	if g := res.Stored().GlassMs; g != 0 {
		t.Fatalf("headless run stored glassMs %v", g)
	}
}
