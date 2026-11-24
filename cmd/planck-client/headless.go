package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
)

// Percentiles is a p50/p95/p99 summary in milliseconds.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// Summary is what a headless run measured.
type Summary struct {
	Host     string  `json:"host"`
	HostID   string  `json:"host_id"`
	Addr     string  `json:"addr"`
	PathKind string  `json:"path_kind"`
	Codec    string  `json:"codec"`
	Chroma   string  `json:"chroma"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Seconds  float64 `json:"duration_s"`

	// The rest of the stream's identity comes from the host's resolved
	// profile, and is empty from an older host. VirtualDisplay is what
	// the profile chose; the host falls back to its physical display if
	// it cannot make one, and logs it. A headless run shows nothing, so
	// direct display is only ever allowed, not active.
	Encoder              string                    `json:"encoder,omitempty"`
	BitDepth             int                       `json:"bit_depth,omitempty"`
	TargetFPS            int                       `json:"target_fps,omitempty"`
	VirtualDisplay       bool                      `json:"virtual_display"`
	DirectDisplayAllowed bool                      `json:"direct_display_allowed"`
	DirectDisplayActive  bool                      `json:"direct_display_active"`
	Warnings             []string                  `json:"warnings,omitempty"`
	Profile              *defaults.ResolvedProfile `json:"profile,omitempty"`

	FramesDecoded uint64  `json:"frames_decoded"`
	FramesLost    uint64  `json:"frames_lost"`
	FramesDropped uint64  `json:"frames_dropped"`
	SinkErrors    uint64  `json:"sink_errors"`
	FPS           float64 `json:"fps"`
	BitrateMbps   float64 `json:"bitrate_mbps"`
	FECPct        float64 `json:"fec_pct"`
	LossPct       float64 `json:"loss_pct"`
	// FramesPartial counts PyroWave frames shown with chunks missing.
	FramesPartial uint64 `json:"frames_partial"`

	AudioPackets uint64  `json:"audio_packets"`
	AudioBufMs   float64 `json:"audio_buf_ms"`
	InputSent    uint64  `json:"input_sent"`

	// Stage times over the last frames the client kept (up to 600); the
	// round trip is sampled ten times a second.
	EncodeMs    Percentiles `json:"encode_ms"`
	NetRTTMs    Percentiles `json:"net_rtt_ms"`
	NetOneWayMs Percentiles `json:"net_oneway_ms"`
	DecodeMs    Percentiles `json:"decode_ms"`
	PresentMs   Percentiles `json:"present_ms"`
	E2EMs       Percentiles `json:"e2e_ms"`
}

// headlessDeps replaces the hardware for tests.
type headlessDeps struct {
	openSink  session.SinkOpener
	tailscale interface {
		session.PathInfoer
		tailnet
	}
	logf func(string, ...any)
}

func runHeadless(o options, out io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	s, err := headless(ctx, o, headlessDeps{})
	if err != nil {
		return err
	}
	if o.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(s); err != nil {
			return err
		}
	} else {
		printSummary(out, s)
	}
	if s.FramesDecoded == 0 {
		return errors.New("no frame decoded")
	}
	return nil
}

// countingSink counts what the decoder gave pictures for.
type countingSink struct {
	session.Sink
	frames, bytes atomic.Uint64
	first, last   atomic.Int64
}

func (s *countingSink) Submit(au []byte, pts int64) (player.Timing, error) {
	t, err := s.Sink.Submit(au, pts)
	s.bytes.Add(uint64(len(au)))
	if err == nil {
		now := time.Now().UnixNano()
		s.first.CompareAndSwap(0, now)
		s.last.Store(now)
		s.frames.Add(1)
	}
	return t, err
}

// silentPlayer takes the decoded audio and plays none of it.
type silentPlayer struct{}

func (silentPlayer) Write([]int16) error { return nil }
func (silentPlayer) Close() error        { return nil }

func headless(ctx context.Context, o options, d headlessDeps) (Summary, error) {
	logf := d.logf
	if logf == nil {
		logf = log.New(os.Stderr, "", log.LstdFlags).Printf
	}
	open := d.openSink
	if open == nil {
		open = session.OpenDecodeSink
	}
	var ts interface {
		session.PathInfoer
		tailnet
	} = d.tailscale
	if ts == nil {
		ts = tailscale.New()
	}
	var sinkMu sync.Mutex
	var sink *countingSink
	cfg := session.ClientConfig{
		ConfigDir: o.configDir, Name: o.name, PIN: o.pin, NoAudio: o.noAudio, Tailscale: ts, Logf: logf,
		HandshakeTimeout: o.timeout,
		Quality:          session.Quality{Codec: o.codec, Chroma: o.chroma, FPS: o.fps, BitrateKbps: o.bitrateKbps, Slices: o.slices},
		Overrides:        o.overrides,
		OpenSink: func(c decode.Config) (session.Sink, error) {
			inner, err := open(c)
			if err != nil {
				return nil, err
			}
			s := &countingSink{Sink: inner}
			sinkMu.Lock()
			sink = s
			sinkMu.Unlock()
			return s, nil
		},
	}
	if o.silentAudio {
		cfg.OpenAudioPlayer = func() (audio.Player, error) { return silentPlayer{}, nil }
	}
	c, err := session.NewClient(cfg)
	if err != nil {
		return Summary{}, err
	}
	target, err := headlessTarget(ctx, o, ts, logf)
	if err != nil {
		return Summary{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, o.timeout)
	err = c.Connect(cctx, target)
	cancel()
	if err != nil {
		if errors.Is(err, transport.ErrAuthDenied) && o.pin == "" {
			return Summary{}, fmt.Errorf("%w: pass the PIN planck-host shows with -pin", err)
		}
		return Summary{}, err
	}
	defer c.Disconnect()
	if st := c.Stats(); !pathAllowed(o.pathMode, st.Path) {
		return Summary{}, fmt.Errorf("connected over %s, but -path-mode is %s", st.Path, o.pathMode)
	}

	var rtts, audioBuf []float64
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	probe := time.NewTicker(max(o.inputEvery, time.Millisecond))
	defer probe.Stop()
	end := time.After(o.duration)
	start := time.Now()
	var last session.ClientStats
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-c.Done():
			return Summary{}, fmt.Errorf("session ended: %v", c.Err())
		case <-end:
			break loop
		case <-probe.C:
			if o.inputProbe {
				if err := c.SendInput(input.Event{Type: input.TypeMouseMoveRel}); err != nil {
					logf("planck: input probe: %v", err)
				}
			}
		case <-tick.C:
			if st := c.Stats(); st.Connected {
				last = st
				if st.RTTMs > 0 {
					rtts = append(rtts, st.RTTMs)
				}
				if st.AudioPackets > 0 {
					audioBuf = append(audioBuf, st.AudioBufferMs)
				}
			}
		}
	}
	if st := c.Stats(); st.Connected {
		last = st
	}
	sinkMu.Lock()
	cs := sink
	sinkMu.Unlock()
	s := summarize(last, time.Since(start), rtts, audioBuf)
	if cs != nil {
		s.FramesDecoded = cs.frames.Load()
		if span := time.Duration(cs.last.Load() - cs.first.Load()); span > 0 && s.FramesDecoded > 1 {
			s.FPS = float64(s.FramesDecoded-1) / span.Seconds()
		}
		s.BitrateMbps = float64(cs.bytes.Load()) * 8 / 1e6 / s.Seconds
	}
	return s, nil
}

func pathAllowed(m pathMode, path string) bool {
	switch m {
	case pathLAN:
		return path == discovery.PathLANDirect.String()
	case pathTailscale:
		return path == discovery.PathTailscaleDirect.String() || path == discovery.PathTailscaleDERP.String()
	}
	return true
}

func kindAllowed(m pathMode) func(discovery.PathKind) bool {
	return func(k discovery.PathKind) bool { return pathAllowed(m, k.String()) }
}

// headlessTarget is -connect's address, or the paths -discover found.
func headlessTarget(ctx context.Context, o options, ts tailnet, logf func(string, ...any)) (session.Target, error) {
	if o.connect != "" {
		t, _ := identify(ctx, o.connect)
		return t, nil
	}
	f := newFinder(fmt.Sprintf(":%d", discovery.DefaultPort), ts, nil, logf)
	if len(o.discoveryAddrs) > 0 {
		f = newFinder("", nil, o.discoveryAddrs, logf)
	}
	fctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	go f.run(fctx)
	// Once one path answers, the others get a moment to, so the race has
	// them all.
	v, err := f.find(fctx, o.discover, kindAllowed(o.pathMode), 300*time.Millisecond)
	if err != nil {
		return session.Target{}, err
	}
	logf("planck: found %q (%s) on %d path(s)", v.Beacon.Name, v.Beacon.HostID, len(v.Candidates))
	return session.Target{Candidates: v.Candidates, HostID: v.Beacon.HostID, Fingerprint: v.Beacon.CertFingerprint}, nil
}

func summarize(st session.ClientStats, elapsed time.Duration, rtts, audioBuf []float64) Summary {
	s := summarizeStats(st, elapsed, rtts, audioBuf)
	if p := st.Profile; p != nil {
		s.Encoder = fmt.Sprintf("%s on %s", p.EncoderAPI, p.EncodeAdapter.Name)
		s.BitDepth, s.TargetFPS, s.VirtualDisplay, s.DirectDisplayAllowed = p.BitDepth, p.FPS, p.VirtualDisplay, p.DirectDisplay
		s.Warnings, s.Profile = p.Warnings, p
	}
	return s
}

func summarizeStats(st session.ClientStats, elapsed time.Duration, rtts, audioBuf []float64) Summary {
	return Summary{
		Host: st.HostName, HostID: st.HostID, Addr: st.Addr, PathKind: st.Path, Codec: st.Codec, Chroma: st.Chroma,
		Width: st.Width, Height: st.Height, Seconds: elapsed.Seconds(),
		FramesLost: st.FramesLost, FramesDropped: st.FramesDropped, SinkErrors: st.SinkErrors,
		FECPct: st.FECParityPct, LossPct: st.LossPct, FramesPartial: st.FramesPartial,
		AudioPackets: st.AudioPackets, AudioBufMs: percentiles(audioBuf).P50, InputSent: st.InputSent,
		EncodeMs: fromLatency(st.Encode), NetRTTMs: percentiles(rtts), NetOneWayMs: fromLatency(st.Network),
		DecodeMs: fromLatency(st.Decode), PresentMs: fromLatency(st.Present), E2EMs: fromLatency(st.E2E),
	}
}

func fromLatency(l session.Latency) Percentiles { return Percentiles{l.P50, l.P95, l.P99} }

func percentiles(v []float64) Percentiles {
	if len(v) == 0 {
		return Percentiles{}
	}
	s := slices.Clone(v)
	slices.Sort(s)
	at := func(p float64) float64 { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return Percentiles{at(0.50), at(0.95), at(0.99)}
}

func printSummary(w io.Writer, s Summary) {
	pc := func(p Percentiles) string { return fmt.Sprintf("%6.2f %6.2f %6.2f", p.P50, p.P95, p.P99) }
	fmt.Fprintf(w, "host       %s (%s) at %s over %s\n", s.Host, s.HostID, s.Addr, s.PathKind)
	fmt.Fprintf(w, "stream     %s %s %dx%d, %.1f s\n", s.Codec, s.Chroma, s.Width, s.Height, s.Seconds)
	if s.Profile != nil {
		fmt.Fprintf(w, "profile    %d-bit %d fps, %s, virtual display %v, direct display allowed %v\n", s.BitDepth, s.TargetFPS, s.Encoder, s.VirtualDisplay, s.DirectDisplayAllowed)
		for _, warn := range s.Warnings {
			fmt.Fprintf(w, "warning    %s\n", warn)
		}
	}
	fmt.Fprintf(w, "frames     %d decoded, %d lost, %d partial, %d dropped, %d sink errors, %.1f fps\n", s.FramesDecoded, s.FramesLost, s.FramesPartial, s.FramesDropped, s.SinkErrors, s.FPS)
	fmt.Fprintf(w, "network    %.1f Mbps, FEC parity %.1f%%, loss %.2f%%\n", s.BitrateMbps, s.FECPct, s.LossPct)
	fmt.Fprintf(w, "audio      %d packets, %.1f ms buffered; input %d sent\n", s.AudioPackets, s.AudioBufMs, s.InputSent)
	fmt.Fprintf(w, "ms           p50    p95    p99\n")
	fmt.Fprintf(w, "encode     %s\n", pc(s.EncodeMs))
	fmt.Fprintf(w, "rtt        %s\n", pc(s.NetRTTMs))
	fmt.Fprintf(w, "one-way    %s\n", pc(s.NetOneWayMs))
	fmt.Fprintf(w, "decode     %s\n", pc(s.DecodeMs))
	fmt.Fprintf(w, "present    %s\n", pc(s.PresentMs))
	fmt.Fprintf(w, "e2e        %s\n", pc(s.E2EMs))
}
