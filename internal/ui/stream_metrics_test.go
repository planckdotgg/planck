package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
)

func showMetrics(t *testing.T, tt *mui.Tester) mui.Rect {
	t.Helper()
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	tt.Frame()
	return bounds(t, tt, "Metrics")
}

// TestMetricsOpensTopRightCompactKeepsCorner: off by default, top-right
// 16 px in when shown, and compact and back keep its top-left corner.
func TestMetricsOpensTopRightCompactKeepsCorner(t *testing.T) {
	f := streamFake()
	_, tt, _ := newStreamTester(t, f)
	if shows(tt, "Metrics") {
		t.Fatal("metrics window shows by default")
	}
	m := showMetrics(t, tt)
	if want := (mui.Rect{X: testWidth - 16 - metricsW, Y: 16, W: metricsW, H: metricsH}); m != want {
		t.Fatalf("metrics at %+v, want %+v", m, want)
	}
	snapshot(t, tt, "metrics-expanded")
	click(t, tt, "Compact metrics")
	if c := bounds(t, tt, "Metrics"); c.X != m.X || c.Y != m.Y || c.W != compactW || c.H != compactH {
		t.Fatalf("compact at %+v, want 196 x 36 at %v, %v", c, m.X, m.Y)
	}
	snapshot(t, tt, "metrics-compact")
	click(t, tt, "Expand metrics")
	if e := bounds(t, tt, "Metrics"); e != m {
		t.Fatalf("expanded again at %+v, want %+v", e, m)
	}
	click(t, tt, "Close metrics")
	if shows(tt, "Metrics") {
		t.Fatal("close left the window")
	}
}

// TestMetricsDragsByHeadOnly: dragging the head moves the window, held
// inside the view; dragging its body moves nothing; a window let go near
// a corner goes into it.
func TestMetricsDragsByHeadOnly(t *testing.T) {
	f := streamFake()
	_, tt, p := newStreamTester(t, f)
	m := showMetrics(t, tt)
	drag(tt, m.X+m.W/2, m.Y+m.H/2, m.X+m.W/2-400, m.Y+m.H/2+200)
	if got := bounds(t, tt, "Metrics"); got != m {
		t.Fatalf("dragging the body moved the window from %+v to %+v", m, got)
	}
	if p.presses != 0 {
		t.Fatalf("dragging the body reached the stream %d times", p.presses)
	}
	head := bounds(t, tt, "Metrics window bar")
	hx, hy := head.X+40, head.Y+head.H/2
	drag(tt, hx, hy, hx-400, hy+200)
	got := bounds(t, tt, "Metrics")
	if got.X != m.X-400 || got.Y != m.Y+200 {
		t.Fatalf("dragging the head by -400, 200 moved the window from %+v to %+v", m, got)
	}
	head = bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+40+2000, head.Y+10+2000)
	got = bounds(t, tt, "Metrics")
	if got.X != testWidth-metricsInset-metricsW || got.Y != testHeight-metricsInset-metricsH {
		t.Fatalf("thrown past the corner: at %+v, want held 12 px in", got)
	}
	head = bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+40-(testWidth-2*metricsInset-metricsW-15), head.Y+10-15)
	got = bounds(t, tt, "Metrics")
	if got.X != metricsInset || got.Y != testHeight-metricsInset-metricsH {
		t.Fatalf("let go 15 px from the bottom-left corner: at %+v, want in it", got)
	}
	if p.presses != 0 {
		t.Fatalf("dragging the window reached the stream %d times", p.presses)
	}
	if !f.layout.Metrics || f.layout.MetricsX != float32(metricsInset)/testWidth {
		t.Fatalf("stored layout %+v", f.layout)
	}
	again, tt2, _ := newStreamTester(t, f)
	tt2.Frame()
	if b := bounds(t, tt2, "Metrics"); b != got {
		t.Fatalf("next stream: window at %+v, want %+v", b, got)
	}
	_ = again
}

// TestMetricsKeepsClearOfDockedEdge: with the button docked left, the
// window stops 28 px short of the left edge.
func TestMetricsKeepsClearOfDockedEdge(t *testing.T) {
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, Dock: DockLeft, DockFrac: 0.5}
	_, tt, _ := newStreamTester(t, f)
	tt.Move(700, 400)
	tt.Frame()
	showMetrics(t, tt)
	head := bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+40-2000, head.Y+10+300)
	if got := bounds(t, tt, "Metrics"); got.X != metricsKeepOut {
		t.Fatalf("window at %+v, want 28 px from the docked edge", got)
	}
}

func TestMetricsCompactUnder720(t *testing.T) {
	f := streamFake()
	a := NewApp(f)
	a.goos = "windows"
	tt := mui.NewTester(a.View, 1280, 700)
	tt.Frame()
	m := showMetrics(t, tt)
	if m.W != compactW || m.H != compactH {
		t.Fatalf("metrics under 720 px: %+v, want compact", m)
	}
	openMenu(t, tt)
	if !tt.HasText("Compact under a 720 px window") {
		t.Fatalf("the menu's Metrics row does not say so: %q", tt.Texts())
	}
}

// TestMetricsReadings: the five stages with capture and the round trip,
// formatted four times a second, and each degraded state named in words.
func TestMetricsReadings(t *testing.T) {
	f := streamFake()
	f.tel.CaptureMs, f.tel.RTTMs, f.tel.CeilingMbps = 2.9, 0.41, 200
	_, tt, _ := newStreamTester(t, f)
	showMetrics(t, tt)
	for _, s := range []string{"Capture", "2.90", "Encode", "1.50", "Network", "0.70", "Decode", "1.00", "Present", "2.50",
		"0.41", "80.0", "40 % of ceiling", "HEVC · 10-bit · 4:2:0", "RTX 4080 VDD", "LAN direct"} {
		if !tt.HasText(s) && !tt.HasText("drag the bar · "+s) {
			t.Errorf("missing %q", s)
		}
	}
	f.set(func(f *fakeBackend) { f.tel.CaptureMs = 3.5 })
	tt.Frame()
	if tt.HasText("3.50") {
		t.Fatal("reading changed before the 4 Hz tick")
	}
	tt.Advance(textEvery)
	if !tt.HasText("3.50") {
		t.Fatal("reading did not change on the 4 Hz tick")
	}

	f.set(func(f *fakeBackend) {
		f.tel.PathKind = discovery.PathTailscaleDERP
		f.tel.TotalMs = 30
		f.tel.LossPct = 1.2
	})
	tt.Advance(textEvery)
	if !tt.HasText("DERP relay") || !tt.HasText("late") {
		t.Fatalf("relay and late not named: %q", tt.Texts())
	}
	snapshot(t, tt, "metrics-degraded")
	f.set(func(f *fakeBackend) { f.tel.FramesDropped = 3 })
	tt.Advance(textEvery)
	if !tt.HasText("dropping") || tt.HasText("late") {
		t.Fatalf("dropping not named over late: %q", tt.Texts())
	}
	tt.Advance(1500 * time.Millisecond)
	if tt.HasText("dropping") {
		t.Fatal("dropping still named a second after the last drop")
	}
}

func TestReadMetricsSignals(t *testing.T) {
	tel := sampleTelemetry()
	tel.Profile = &defaults.ResolvedProfile{FPS: 120, FEC: defaults.FECProfile{Adaptive: true, MinParityPercent: 10}}
	if r := readMetrics(tel, false); r.late || r.lossy || r.fecRaised {
		t.Fatalf("healthy stream: %+v", r)
	}
	tel.TotalMs, tel.LossPct, tel.FECPct = 16.8, 0.6, 25
	r := readMetrics(tel, false)
	if !r.late || !r.lossy || !r.fecRaised {
		t.Fatalf("late, lossy, raised FEC: %+v", r)
	}
	if col, name := r.figureColor(); name != "late" || col == (mui.Color{}) {
		t.Fatalf("figure %v %q", col, name)
	}
	tel.TotalMs = 16.6
	if readMetrics(tel, false).late {
		t.Fatal("16.6 ms at 120 fps is within two frames")
	}
}

func TestSparklineAllocatesNothing(t *testing.T) {
	var w metricsWindow
	for k := range sparkLen {
		for i := range w.samples {
			w.samples[i][k] = float32(k % 7)
		}
	}
	w.n = sparkLen
	r := mui.Rect{W: 54, H: 12}
	w.sparkPath(r, 0)
	if n := testing.AllocsPerRun(100, func() { w.sparkPath(r, 0) }); n != 0 {
		t.Fatalf("sparkline allocates %.1f times a draw", n)
	}
}
