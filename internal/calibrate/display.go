package calibrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/transport"
)

// DisplayHints are what the app knows of the stream window while Phase 3
// runs.
type DisplayHints struct {
	Fullscreen bool
	// OverlayOpen: the stream's menu, metrics or HUD is drawn over the
	// picture.
	OverlayOpen bool
}

// compositedShare is the share of pictures, in percent, a compositor
// shows late before Phase 3 warns of it.
const compositedShare = 50

// displayVariants are what Phase 3 shows: the default first, then vsync
// on and phase lock off, where user leaves them auto.
func displayVariants(user defaults.UserOverrides) []DisplayVariant {
	auto := func(t defaults.Toggle) bool { return t == "" || t == defaults.Auto }
	out := []DisplayVariant{{Name: "Default"}}
	if auto(user.VSyncMode) {
		out = append(out, DisplayVariant{Name: "VSync on", Overrides: defaults.UserOverrides{VSyncMode: defaults.ToggleOn}})
	}
	if auto(user.PhaseLock) {
		out = append(out, DisplayVariant{Name: "Phase lock off", Overrides: defaults.UserOverrides{PhaseLock: defaults.ToggleOff}})
	}
	return out
}

// displayScore is a variant's capture-to-glass p50 plus half its spread
// to the p99.
func displayScore(v DisplayVariant) float64 {
	return v.Total.P50 + 0.5*max(v.Total.P99-v.Total.P50, 0)
}

// display is Phase 3: the pick shown in the Dialer's window with each
// variant.
func (r *run) display(ctx context.Context) error {
	dp := &r.res.Display
	switch {
	case !r.o.Display:
		dp.Status, dp.Note = DisplaySkipped, "headless: no window shows the stream, so present to glass was not measured"
		return nil
	case r.res.Best < 0:
		dp.Status, dp.Note = DisplaySkipped, "no candidate streamed to show"
		return nil
	}
	pick := r.res.Candidates[r.res.Best].Overrides
	vs := displayVariants(r.o.User)
	for i := range vs {
		v := &vs[i]
		r.report(PhaseDisplay, v.Name, 75+25*i/len(vs))
		o := defaults.Merge(defaults.Merge(pick, v.Overrides), r.o.User)
		st, hints, err := r.measureShown(ctx, o, true, r.timing.DisplayMeasure)
		v.hints = hints
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, transport.ErrCalibrationUnsupported) {
				return err
			}
			v.Err = err.Error()
		} else {
			v.Glass, v.Total, v.Samples = stage(st.Glass), stage(st.E2EGlass), st.Glass.Samples
			v.ScanOut, v.CompositedPct, v.RefreshHz, v.Locked = st.Phase.Mode.String(), st.Phase.CompositedPct, st.Phase.RefreshHz(), st.Phase.Locked
			if v.ScanOut == "unknown" {
				v.ScanOut = ""
			}
		}
		dp.Variants = append(dp.Variants, *v)
	}
	dp.Best = -1
	for i, v := range dp.Variants {
		if v.Err == "" && v.Samples > 0 && (dp.Best < 0 || displayScore(v) < displayScore(dp.Variants[dp.Best])) {
			dp.Best = i
		}
	}
	if dp.Best < 0 {
		dp.Status, dp.Note = DisplayUnavailable, "this window does not report when pictures reach the screen"
		return nil
	}
	dp.Status = DisplayMeasured
	best := dp.Variants[dp.Best]
	r.res.Warnings = append(r.res.Warnings, displayWarnings(best, best.hints)...)
	return nil
}

// displayWarnings explain a present path that costs a refresh: why the
// system composites the stream, and what to change. hints nil knows
// nothing of the window.
func displayWarnings(v DisplayVariant, hints *DisplayHints) []string {
	if v.ScanOut != "composited" && v.CompositedPct < compositedShare {
		return nil
	}
	cost := "a refresh"
	if v.RefreshHz > 0 {
		cost = fmt.Sprintf("about %.1f ms, a refresh at %.0f Hz,", 1000/v.RefreshHz, v.RefreshHz)
	}
	out := []string{fmt.Sprintf("The system composites the stream before it reaches the screen (%.0f%% of pictures), which adds %s to Present.", v.CompositedPct, cost)}
	switch {
	case hints != nil && !hints.Fullscreen:
		out = append(out, "The stream window is not full screen; only a full-screen window can go straight to the display. Make it full screen and calibrate again.")
	case hints != nil && hints.OverlayOpen:
		out = append(out, "The stream's menu, metrics or HUD was open over the picture, which makes the system composite it. Close it while you play.")
	default:
		out = append(out, "Something draws over the stream or redraws the screen: close overlays such as FPS counters, recording or chat overlays and notifications, then calibrate again.")
	}
	return out
}

// jitterWarnMs, lossWarnPct: a route past either gets a warning.
const (
	jitterWarnMs = 2.0
	lossWarnPct  = 1.0
)

// linkWarnings explain what of the route costs latency or pictures.
func linkWarnings(l Link) []string {
	var out []string
	if l.Route == discovery.PathTailscaleDERP.String() {
		out = append(out, "The host is reached through a Tailscale relay (DERP), which adds latency and caps the bitrate; a direct tailnet or LAN path is faster.")
	}
	if l.JitterMs > jitterWarnMs {
		out = append(out, fmt.Sprintf("The round trip changes by %.1f ms from one ping to the next, as Wi-Fi does; a wired connection keeps frames on time.", l.JitterMs))
	}
	if l.LossPct > lossWarnPct {
		out = append(out, fmt.Sprintf("%.1f%% of a paced train was lost; parity covers some of it, at a bitrate cost.", l.LossPct))
	}
	return out
}
