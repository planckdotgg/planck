package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/discovery"
)

// pairedBackend is a test backend paired with h, its stream ended.
func pairedBackend(t *testing.T, h *fakeHost) *backend {
	t.Helper()
	b, _ := newTestBackend(t, t.TempDir(), nil)
	ctx := context.Background()
	if err := b.Connect(ctx, h.Addr().String()); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("connect unpaired: %v", err)
	}
	if err := b.SubmitPIN(ctx, "246810"); err != nil {
		t.Fatal(err)
	}
	b.Disconnect()
	waitUntil(t, "the stream to end", func() bool { return b.current() == nil && h.ConnectedClient() == "" })
	return b
}

// TestBackendCalibration: a calibration of a paired host reports its
// phases, refuses a second one and a stream while it runs, finishes with
// a result that ApplyCalibration keeps for the route, which
// CalibrationStatus lists and ResetCalibration forgets.
func TestBackendCalibration(t *testing.T) {
	h := calibrationFakeHost(t)
	b := pairedBackend(t, h)
	b.calibrationTiming = &cliTiming
	id := b.Hosts()[0].ID
	if err := b.ApplyCalibration(id); !errors.Is(err, ui.ErrNoCalibration) {
		t.Fatalf("apply before a run: %v", err)
	}
	if err := b.StartCalibration(id); err != nil {
		t.Fatal(err)
	}
	if err := b.StartCalibration(id); !errors.Is(err, ui.ErrCalibrating) {
		t.Fatalf("a second calibration: %v", err)
	}
	if err := b.Connect(context.Background(), id); !errors.Is(err, ui.ErrCalibrating) {
		t.Fatalf("connect while calibrating: %v", err)
	}
	seen := map[ui.CalibrationPhase]bool{}
	waitUntil(t, "the calibration to finish", func() bool {
		st := b.Calibration()
		seen[st.Phase] = true
		return !st.Running
	})
	st := b.Calibration()
	if st.Err != "" || st.Result == nil || st.Percent != 100 || !seen[ui.CalibratingCodec] {
		t.Fatalf("state %+v, phases seen %v", st, seen)
	}
	r := st.Result
	if r.Link.PathKind != discovery.PathLANDirect || r.Link.RTT.P50 <= 0 || len(r.Candidates) == 0 {
		t.Fatalf("result %+v", r)
	}
	best := 0
	for _, c := range r.Candidates {
		if c.Best {
			best++
		}
	}
	if best != 1 {
		t.Fatalf("%d candidates marked best", best)
	}
	// The test sink cannot tell when pictures reach a screen.
	if r.Display.Status != ui.CalibrationDisplayUnavailable {
		t.Fatalf("display %+v", r.Display)
	}
	if b.CalibrationStatus(id) != nil {
		t.Fatal("a status before apply")
	}
	if err := b.ApplyCalibration(id); err != nil {
		t.Fatal(err)
	}
	sum := b.CalibrationStatus(id)
	if len(sum) != 1 || sum[0].PathKind != discovery.PathLANDirect || sum[0].Overrides != r.Recommended || sum[0].RTTMs <= 0 {
		t.Fatalf("status %+v", sum)
	}
	if err := b.ResetCalibration(id, discovery.PathTailscaleDirect); err != nil || len(b.CalibrationStatus(id)) != 1 {
		t.Fatalf("reset of another route: %v, %+v", err, b.CalibrationStatus(id))
	}
	if err := b.ResetCalibration(id); err != nil || b.CalibrationStatus(id) != nil {
		t.Fatalf("reset all: %v, %+v", err, b.CalibrationStatus(id))
	}
}

// TestBackendCalibrationCancel: CancelCalibration ends a run at once, and
// the state says so.
func TestBackendCalibrationCancel(t *testing.T) {
	h := calibrationFakeHost(t)
	b := pairedBackend(t, h)
	slow := cliTiming
	slow.Measure = time.Hour
	b.calibrationTiming = &slow
	id := b.Hosts()[0].ID
	if err := b.StartCalibration(id); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the codec phase", func() bool { return b.Calibration().Phase == ui.CalibratingCodec })
	start := time.Now()
	b.CancelCalibration()
	waitUntil(t, "the run to end", func() bool { return !b.Calibration().Running })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cancel took %v", d)
	}
	if st := b.Calibration(); !st.Cancelled || st.Result != nil || st.Err != "" {
		t.Fatalf("state %+v", st)
	}
	if err := b.ApplyCalibration(id); !errors.Is(err, ui.ErrNoCalibration) {
		t.Fatalf("apply after a cancel: %v", err)
	}
	waitUntil(t, "the host to be free", func() bool { return h.ConnectedClient() == "" })
}

func TestBackendCalibrationRefusesWhileStreaming(t *testing.T) {
	h := calibrationFakeHost(t)
	b := pairedBackend(t, h)
	if err := b.Connect(context.Background(), h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := b.StartCalibration(b.Hosts()[0].ID); !errors.Is(err, ui.ErrStreaming) {
		t.Fatalf("calibrate while streaming: %v", err)
	}
}
