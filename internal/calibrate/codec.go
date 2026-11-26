package calibrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/transport"
)

const (
	// maxCandidates bounds Phase 2, the default included, to about 20 s.
	maxCandidates = 7
	// minFrames is the fewest pictures a candidate must decode to count.
	minFrames = 20
	// rampUp follows a stream's first picture before the measurement, for
	// the encoder's rate and clocks to settle.
	rampUp = 300 * time.Millisecond
	// dropPenaltyMs is added to a score for each percent of the frames the
	// target rate asks for that were dropped, lost or never came.
	dropPenaltyMs = 2.0
	// efficiencyTieMs: candidates scoring within this of the best are
	// equal on latency, and the one that carries more picture per bit
	// wins.
	efficiencyTieMs = 0.5
)

// efficiency ranks what a codec carries per bit, higher better.
func efficiency(c Candidate) int {
	switch {
	case c.Codec == "av1":
		return 4
	case c.Codec == "hevc" && c.BitDepth == 10:
		return 3
	case c.Codec == "hevc":
		return 2
	case c.Codec == "h264":
		return 1
	}
	return 0
}

// score is a candidate's capture-to-picture p50 plus half its spread to
// the p99, plus dropPenaltyMs for each percent of its frames dropped,
// lost or missing from the target rate.
func score(c Candidate) float64 {
	return c.Total.P50 + 0.5*max(c.Total.P99-c.Total.P50, 0) + dropPenaltyMs*c.BadPct()
}

// BadPct is the share, in percent, of the frames the target rate asks for
// that were dropped, lost or never came.
func (c Candidate) BadPct() float64 {
	want := float64(c.Frames + c.Lost)
	if c.Expected > want {
		want = c.Expected
	}
	if want <= 0 {
		return 100
	}
	good := float64(c.Frames) - float64(c.Dropped)
	return 100 * max(want-good, 0) / want
}

// pick is the viable candidate with the lowest score, the one with the
// best efficiency among those within efficiencyTieMs of it, -1 when none
// is viable.
func pick(cs []Candidate) int {
	best := -1
	for i, c := range cs {
		if c.Viable() && (best < 0 || c.Score < cs[best].Score) {
			best = i
		}
	}
	if best < 0 {
		return -1
	}
	win := best
	for i, c := range cs {
		if c.Viable() && c.Score-cs[best].Score <= efficiencyTieMs && efficiency(c) > efficiency(cs[win]) {
			win = i
		}
	}
	return win
}

// codecSupport is what both ends can do with a codec.
type codecSupport struct{ ok, tenBit bool }

func support(host defaults.HostCapabilities, client defaults.ClientCapabilities, c defaults.Codec) codecSupport {
	var enc, dec codecSupport
	for _, e := range host.Encoders {
		for _, cc := range e.Codecs {
			if cc.Codec == c {
				enc.ok, enc.tenBit = true, enc.tenBit || cc.TenBit
			}
		}
	}
	for _, cc := range client.Decoders {
		if cc.Codec == c {
			dec.ok, dec.tenBit = true, dec.tenBit || cc.TenBit
		}
	}
	return codecSupport{ok: enc.ok && dec.ok, tenBit: enc.tenBit && dec.tenBit}
}

// candidates are what Phase 2 tries on route: the default first, then
// each codec and bit depth both ends have, PyroWave on the LAN, and
// NVENC's split and sub-frame slices where the host has them, varying
// only the knobs user leaves auto, at most maxCandidates.
func candidates(host defaults.HostCapabilities, client defaults.ClientCapabilities, route string, user defaults.UserOverrides) []Candidate {
	auto := func(s string) bool { return s == "" || s == defaults.Auto }
	out := []Candidate{{Name: "Default"}}
	add := func(o defaults.UserOverrides) {
		for _, c := range out {
			if c.Overrides == o {
				return
			}
		}
		out = append(out, Candidate{Name: candidateName(o), Overrides: o})
	}
	codec := defaults.CodecHEVC
	if !auto(string(user.Codec)) {
		for _, c := range []defaults.Codec{defaults.CodecH264, defaults.CodecHEVC, defaults.CodecAV1, defaults.CodecPyroWave} {
			if c.String() == string(user.Codec) {
				codec = c
			}
		}
	}
	split, slices := false, false
	for _, e := range host.Encoders {
		split = split || (e.API == defaults.APINVENC && e.Engines >= 2)
		slices = slices || e.SubFrame
	}
	split = split && auto(string(user.NVENC2EngineSplit))
	slices = slices && auto(string(user.SubFrameSlices))
	depth := func(o defaults.UserOverrides, bits string) defaults.UserOverrides {
		if auto(string(user.BitDepth)) {
			o.BitDepth = defaults.BitDepthPref(bits)
		}
		return o
	}
	pref := func(c defaults.Codec) defaults.UserOverrides {
		if !auto(string(user.Codec)) {
			return defaults.UserOverrides{}
		}
		return defaults.UserOverrides{Codec: defaults.CodecPref(c.String())}
	}
	base := support(host, client, codec)
	if !base.ok && auto(string(user.Codec)) {
		codec, base = defaults.CodecH264, support(host, client, defaults.CodecH264)
	}
	var later []defaults.UserOverrides
	if base.ok {
		plain := depth(pref(codec), "8")
		if split {
			plain.NVENC2EngineSplit = defaults.ToggleOff
		}
		add(plain)
		if base.tenBit {
			add(depth(pref(codec), "10"))
		}
		if split && codec != defaults.CodecPyroWave {
			on := plain
			on.NVENC2EngineSplit = defaults.ToggleOn
			later = append(later, on)
		}
		if slices && codec != defaults.CodecPyroWave {
			for _, n := range []string{"1", "4"} {
				o := depth(pref(codec), "8")
				o.SubFrameSlices = defaults.SlicesPref(n)
				later = append(later, o)
			}
		}
	}
	if auto(string(user.Codec)) {
		if av1 := support(host, client, defaults.CodecAV1); av1.ok {
			add(depth(pref(defaults.CodecAV1), "8"))
			if av1.tenBit {
				later = append(later, depth(pref(defaults.CodecAV1), "10"))
			}
		}
		if support(host, client, defaults.CodecPyroWave).ok && route == discovery.PathLANDirect.String() {
			add(pref(defaults.CodecPyroWave))
		}
	}
	for _, o := range later {
		add(o)
	}
	return out[:min(len(out), maxCandidates)]
}

// candidateName says what o sets, as "HEVC 10-bit, 4 slices".
func candidateName(o defaults.UserOverrides) string {
	var parts []string
	switch o.Codec {
	case "pyrowave":
		parts = append(parts, "PyroWave")
	case "":
	default:
		parts = append(parts, strings.ToUpper(string(o.Codec)))
	}
	if o.BitDepth != "" {
		parts = append(parts, string(o.BitDepth)+"-bit")
	}
	if o.SubFrameSlices != "" {
		n, _ := strconv.Atoi(string(o.SubFrameSlices))
		parts = append(parts, fmt.Sprintf("%d slice%s", n, map[bool]string{true: "s"}[n != 1]))
	}
	if o.NVENC2EngineSplit == defaults.ToggleOn {
		parts = append(parts, "NVENC split")
	}
	if len(parts) == 0 {
		return "Default"
	}
	return strings.Join(parts, ", ")
}

// codecs is Phase 2.
func (r *run) codecs(ctx context.Context) error {
	cs := candidates(r.host, r.o.Client, r.res.Link.Route, r.o.User)
	for i := range cs {
		r.report(PhaseCodec, cs[i].Name, 25+50*i/len(cs))
		if err := r.tryCandidate(ctx, &cs[i]); err != nil {
			return err
		}
		if cs[i].Viable() {
			cs[i].Score = score(cs[i])
		}
		r.res.Candidates = append(r.res.Candidates, cs[i])
	}
	r.res.Best = pick(r.res.Candidates)
	r.report(PhaseCodec, "compared", 75)
	return nil
}

// tryCandidate streams c for Timing.Measure and records what it did. It
// fails only for a cancelled run or a host that cannot calibrate; any
// other failure is c's Err.
func (r *run) tryCandidate(ctx context.Context, c *Candidate) error {
	st, err := r.measure(ctx, defaults.Merge(c.Overrides, r.o.User), false, r.timing.Measure)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, transport.ErrCalibrationUnsupported) || errors.Is(err, transport.ErrAuthDenied) {
			return err
		}
		c.Err = err.Error()
		return nil
	}
	c.fill(st, r.timing.Measure)
	if why := mismatch(c.Overrides, st.Profile); why != "" {
		c.Err = why
	}
	return nil
}

func (c *Candidate) fill(st session.ClientStats, measured time.Duration) {
	c.Codec = st.Codec
	if p := st.Profile; p != nil {
		c.Codec, c.BitDepth, c.Slices, c.Split = p.Codec.String(), p.BitDepth, p.Slices, p.Split
		c.Width, c.Height, c.FPS, c.Encoder = p.Width, p.Height, p.FPS, p.EncoderAPI+" on "+p.EncodeAdapter.Name
		c.Expected = float64(p.FPS) * measured.Seconds()
	}
	c.Encode, c.Network, c.Decode, c.Total = stage(st.Encode), stage(st.Network), stage(st.Decode), stage(st.E2E)
	if st.HostEncodeMs > 0 {
		c.Encode.P50 = st.HostEncodeMs
	}
	c.BitrateMbps, c.Frames, c.Dropped, c.Lost = st.Kbps/1000, st.FramesReceived, st.FramesDropped, st.FramesLost
}

func stage(l session.Latency) Stage { return Stage{P50: l.P50, P99: l.P99} }

// mismatch says how the host's profile p differs from what o asked for,
// "" when it streamed it: a host that fell back did not test o.
func mismatch(o defaults.UserOverrides, p *defaults.ResolvedProfile) string {
	if p == nil {
		return "the host sent no profile"
	}
	switch {
	case o.Codec != "" && string(o.Codec) != p.Codec.String():
		return fmt.Sprintf("the host streamed %s, not %s", p.Codec, o.Codec)
	case o.BitDepth != "" && string(o.BitDepth) != strconv.Itoa(p.BitDepth):
		return fmt.Sprintf("the host streamed %d-bit, not %s-bit", p.BitDepth, o.BitDepth)
	case o.NVENC2EngineSplit == defaults.ToggleOn && !p.Split:
		return "the host did not split the encode"
	}
	return ""
}

// measure dials a calibration stream asking for o, waits for its first
// picture and the encoder to settle, and returns its stats over d. A
// display stream's window hints are read while it still shows.
func (r *run) measure(ctx context.Context, o defaults.UserOverrides, display bool, d time.Duration) (session.ClientStats, error) {
	st, _, err := r.measureShown(ctx, o, display, d)
	return st, err
}

func (r *run) measureShown(ctx context.Context, o defaults.UserOverrides, display bool, d time.Duration) (session.ClientStats, *DisplayHints, error) {
	c, err := r.d.Dial(ctx, session.CalibrateVideo, o, display)
	if err != nil {
		return session.ClientStats{}, nil, err
	}
	defer c.Close()
	deadline := time.Now().Add(r.timing.Warmup)
	for c.Stats().FramesReceived == 0 {
		if time.Now().After(deadline) {
			return session.ClientStats{}, nil, errors.New("no picture arrived")
		}
		if err := sleep(ctx, 20*time.Millisecond); err != nil {
			return session.ClientStats{}, nil, err
		}
	}
	if err := sleep(ctx, rampUp); err != nil {
		return session.ClientStats{}, nil, err
	}
	c.ResetStats()
	if err := sleep(ctx, d); err != nil {
		return session.ClientStats{}, nil, err
	}
	st := c.Stats()
	if !st.Connected {
		return st, nil, errors.New("the stream ended")
	}
	var hints *DisplayHints
	if display && r.o.Hints != nil {
		h := r.o.Hints()
		hints = &h
	}
	return st, hints, nil
}
