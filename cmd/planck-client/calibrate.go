package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"text/tabwriter"

	"github.com/planckdotgg/planck/internal/calibrate"
	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/tailscale"
)

// calibrateDeps replaces the hardware and the timing for tests.
type calibrateDeps struct {
	headlessDeps
	timing *calibrate.Timing
}

// calibrateOutput is what -calibrate and -reset print with -json.
type calibrateOutput struct {
	HostKey string `json:"hostKey"`
	// Reset are the routes -reset forgot, "all" for every one.
	Reset []string `json:"reset,omitempty"`
	// Applied says -apply kept Result in client.json.
	Applied bool              `json:"applied"`
	Result  *calibrate.Result `json:"result,omitempty"`
}

func runCalibrate(o options, out io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := calibrateCLI(ctx, o, calibrateDeps{})
	if err != nil {
		return err
	}
	if o.json {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printCalibration(out, res)
	return nil
}

// resetRoutes are the routes -reset forgets for path mode m; nil is every
// route.
func resetRoutes(m pathMode) []string {
	switch m {
	case pathLAN:
		return []string{discovery.PathLANDirect.String()}
	case pathTailscale:
		return []string{discovery.PathTailscaleDirect.String(), discovery.PathTailscaleDERP.String()}
	}
	return nil
}

func calibrateCLI(ctx context.Context, o options, d calibrateDeps) (calibrateOutput, error) {
	logf := d.logf
	if logf == nil {
		logf = log.New(os.Stderr, "", log.LstdFlags).Printf
	}
	var ts interface {
		session.PathInfoer
		tailnet
	} = d.tailscale
	if ts == nil {
		ts = tailscale.New()
	}
	dir := o.configDir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return calibrateOutput{}, err
		}
	}
	target, err := headlessTarget(ctx, o, ts, logf)
	if err != nil {
		return calibrateOutput{}, err
	}
	out := calibrateOutput{HostKey: targetKey(target)}
	if o.reset {
		routes := resetRoutes(o.pathMode)
		err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
			if routes == nil {
				c.ResetCalibration(out.HostKey, "")
			}
			for _, r := range routes {
				c.ResetCalibration(out.HostKey, r)
			}
			return nil
		})
		if err != nil {
			return out, err
		}
		out.Reset = routes
		if routes == nil {
			out.Reset = []string{"all"}
		}
		logf("planck: calibration of %s forgotten on %v", out.HostKey, out.Reset)
	}
	if !o.calibrate {
		return out, nil
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		return out, err
	}
	user := defaults.Merge(stored.OverridesFor(out.HostKey), o.overrides)
	open := d.openSink
	if open == nil {
		open = session.OpenDecodeSink
	}
	dialer := &calibrate.SessionDialer{
		Config: session.ClientConfig{ConfigDir: dir, Name: deviceName(dir, o.name), PIN: o.pin, Tailscale: ts, Logf: logf,
			HandshakeTimeout: o.timeout, Capabilities: headlessCapabilities, OpenSink: open},
		Target: target,
	}
	var step string
	res, err := calibrate.Run(ctx, dialer, calibrate.Options{Client: headlessCapabilities(), User: user, Timing: d.timing}, func(p calibrate.Progress) {
		if p.Step != step {
			step = p.Step
			logf("planck: calibrate %3d%% %s: %s", p.Percent, p.Phase, p.Step)
		}
	})
	if err != nil {
		return out, fmt.Errorf("calibration: %w", err)
	}
	out.HostKey, out.Result = res.HostKey(), &res
	if !pathAllowed(o.pathMode, res.Link.Route) {
		return out, fmt.Errorf("calibrated over %s, but -path-mode is %s", res.Link.Route, o.pathMode)
	}
	if o.apply {
		err := config.UpdateClientConfig(dir, func(c *config.ClientConfig) error {
			return c.ApplyCalibration(out.HostKey, res.Link.Route, res.Stored())
		})
		if err != nil {
			return out, err
		}
		out.Applied = true
	}
	return out, nil
}

func printCalibration(w io.Writer, out calibrateOutput) {
	if len(out.Reset) > 0 {
		fmt.Fprintf(w, "reset      %s on %v\n", out.HostKey, out.Reset)
	}
	r := out.Result
	if r == nil {
		return
	}
	l := r.Link
	fmt.Fprintf(w, "host       %s (%s) at %s over %s, %.1f s\n", r.HostName, out.HostKey, r.Addr, l.Route, r.Seconds)
	fmt.Fprintf(w, "rtt        p50 %.2f ms, p99 %.2f ms, jitter %.2f ms\n", l.RTT.P50, l.RTT.P99, l.JitterMs)
	fmt.Fprintf(w, "link       %.0f Mbps, %.2f%% loss, %d-byte datagrams\n", l.BandwidthMbps, l.LossPct, l.DatagramBytes)
	if l.QueueDatagrams > 0 {
		fmt.Fprintf(w, "queue      %d datagrams kept of a burst, drains %d Mbps; paced as %d bytes at %d Mbps\n",
			l.QueueDatagrams, l.DrainMbps, l.ReceiveQueue.Bytes, l.ReceiveQueue.DrainMbps)
	} else {
		fmt.Fprintf(w, "queue      no burst dropped\n")
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tcandidate\tstream\tencode\tnetwork\tdecode\ttotal p50/p99\tMbps\tframes\tbad\tscore")
	for i, c := range r.Candidates {
		mark := ""
		if i == r.Best {
			mark = "*"
		}
		if c.Err != "" {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, c.Name, c.Err)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s %d-bit %dx%d@%d\t%.2f\t%.2f\t%.2f\t%.2f/%.2f\t%.1f\t%d\t%.1f%%\t%.2f\n", mark, c.Name, c.Codec, c.BitDepth,
			c.Width, c.Height, c.FPS, c.Encode.P50, c.Network.P50, c.Decode.P50, c.Total.P50, c.Total.P99, c.BitrateMbps, c.Frames, c.BadPct(), c.Score)
	}
	tw.Flush()
	dp := r.Display
	fmt.Fprintf(w, "display    %s", dp.Status)
	if dp.Note != "" {
		fmt.Fprintf(w, ": %s", dp.Note)
	}
	fmt.Fprintln(w)
	for i, v := range dp.Variants {
		mark := " "
		if i == dp.Best {
			mark = "*"
		}
		fmt.Fprintf(w, "         %s %-16s glass %.2f/%.2f ms, total %.2f ms, %s, locked %v\n", mark, v.Name, v.Glass.P50, v.Glass.P99, v.Total.P50, v.ScanOut, v.Locked)
	}
	rec, _ := json.Marshal(r.Recommended)
	fmt.Fprintf(w, "picked     %s, %.2f ms before, %.2f ms after\n", rec, r.BeforeMs, r.AfterMs)
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "warning    %s\n", warn)
	}
	if out.Applied {
		fmt.Fprintf(w, "applied    kept in client.json for %s on %s\n", out.HostKey, l.Route)
	}
}
