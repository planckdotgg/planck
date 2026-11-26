package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/calibrate"
	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/gpu"
)

func TestCalibrateFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"-calibrate"}, "need one of -connect or -discover"},
		{[]string{"-reset"}, "need one of -connect or -discover"},
		{[]string{"-apply", "-headless", "-connect", "h"}, "-apply needs -calibrate"},
		{[]string{"-calibrate", "-connect", "h", "-discover", "h"}, "need one of -connect or -discover"},
		{[]string{"-calibrate", "-apply", "-json", "-connect", "h", "-path-mode", "lan", "-pin", "123456"}, ""},
		{[]string{"-reset", "-discover", "bigboi", "-path-mode", "tailscale"}, ""},
	} {
		_, err := parseOptions(tc.args, io.Discard)
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%q: %v, want %q", tc.args, err, tc.err)
		}
	}
}

func calibrationFakeHost(t *testing.T) *fakeHost {
	return startFakeHost(t, "off", func(c *session.HostConfig) {
		c.Capabilities = func() defaults.HostCapabilities {
			a := gpu.Adapter{LUID: 7, Vendor: gpu.VendorNVIDIA, Name: "Test GPU"}
			return defaults.HostCapabilities{OS: "windows", Encoders: []defaults.EncoderCap{{Adapter: a, API: defaults.APINVENC, Engines: 1,
				Codecs: []defaults.CodecCap{{Codec: defaults.CodecHEVC, TenBit: true}}}}}
		}
	})
}

var cliTiming = calibrate.Timing{Pings: 5, PingEvery: 2 * time.Millisecond, Settle: 5 * time.Millisecond, Bandwidth: 30 * time.Millisecond,
	Warmup: 3 * time.Second, Measure: 500 * time.Millisecond, DisplayMeasure: 500 * time.Millisecond}

// TestCalibrateCLI runs -calibrate -apply against an in-process host,
// checks what client.json kept, and -reset forgets it.
func TestCalibrateCLI(t *testing.T) {
	h := calibrationFakeHost(t)
	dir := t.TempDir()
	deps := calibrateDeps{headlessDeps: headlessDeps{openSink: (&countSink{}).open(), tailscale: noTailnet{}, logf: t.Logf}, timing: &cliTiming}
	o, err := parseOptions([]string{"-calibrate", "-apply", "-json", "-config", dir, "-connect", h.Addr().String(), "-pin", "246810", "-path-mode", "lan"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	out, err := calibrateCLI(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if r == nil || !out.Applied || r.Link.Route != "lan-direct" || r.Best < 0 || r.Display.Status != calibrate.DisplaySkipped {
		t.Fatalf("output %+v", out)
	}
	var printed bytes.Buffer
	if err := json.NewEncoder(&printed).Encode(out); err != nil || !strings.Contains(printed.String(), `"queueDatagrams"`) {
		t.Fatalf("json: %v %s", err, printed.String())
	}
	printCalibration(&printed, out)
	if !strings.Contains(printed.String(), "applied    kept in client.json") {
		t.Fatalf("summary:\n%s", printed.String())
	}
	stored, _ := config.LoadClientConfig(dir)
	if got, ok := stored.Calibration(out.HostKey, "lan-direct"); !ok || got.Overrides != r.Recommended {
		t.Fatalf("client.json kept %+v, %v for %s", got, ok, out.HostKey)
	}

	o, _ = parseOptions([]string{"-reset", "-config", dir, "-connect", h.Addr().String(), "-path-mode", "lan"}, io.Discard)
	out, err = calibrateCLI(context.Background(), o, deps)
	if err != nil || out.Result != nil || len(out.Reset) != 1 {
		t.Fatalf("reset: %+v, %v", out, err)
	}
	stored, _ = config.LoadClientConfig(dir)
	if _, ok := stored.Calibration(out.HostKey, "lan-direct"); ok {
		t.Fatal("-reset left the calibration")
	}
}

// TestCalibrateCLICancel: an interrupt mid-run ends the calibration at
// once, and keeps nothing.
func TestCalibrateCLICancel(t *testing.T) {
	h := calibrationFakeHost(t)
	dir := t.TempDir()
	slow := cliTiming
	slow.Measure = time.Hour
	deps := calibrateDeps{headlessDeps: headlessDeps{openSink: (&countSink{}).open(), tailscale: noTailnet{}, logf: t.Logf}, timing: &slow}
	o, _ := parseOptions([]string{"-calibrate", "-apply", "-config", dir, "-connect", h.Addr().String(), "-pin", "246810"}, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := calibrateCLI(ctx, o, deps)
		errc <- err
	}()
	time.Sleep(1500 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the cancelled calibration did not end")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cancel took %v", d)
	}
	stored, _ := config.LoadClientConfig(dir)
	for _, p := range stored.PinnedHosts {
		if p.Calibrated != nil {
			t.Fatalf("a cancelled run kept %+v", p.Calibrated)
		}
	}
	waitFor := time.Now().Add(3 * time.Second)
	for h.ConnectedClient() != "" {
		if time.Now().After(waitFor) {
			t.Fatalf("the host still streams to %s", h.ConnectedClient())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
