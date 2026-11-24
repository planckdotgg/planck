package ui

import (
	"testing"

	mui "github.com/egoist/mygo/ui"
)

// resize sets the tester's window to w x h and draws it.
func resize(tt *mui.Tester, w, h int) {
	tt.SetSize(w, h)
	tt.Frame()
}

// toggleCompact clicks the window's chevron, whichever way it points.
func toggleCompact(t *testing.T, tt *mui.Tester) {
	t.Helper()
	if shows(tt, "Compact metrics") {
		click(t, tt, "Compact metrics")
		return
	}
	click(t, tt, "Expand metrics")
}

// TestMetricsTopRightCompactRoundTrip: compact and back, ten times, keep
// the top-right corner 16 px in.
func TestMetricsTopRightCompactRoundTrip(t *testing.T) {
	_, tt, _ := newStreamTester(t, streamFake())
	m := showMetrics(t, tt)
	for i := range 10 {
		toggleCompact(t, tt)
		c := bounds(t, tt, "Metrics")
		if c.X+c.W != testWidth-metricsMargin || c.Y != metricsMargin {
			t.Fatalf("toggle %d: at %+v, want the top-right corner 16 px in", i, c)
		}
	}
	if got := bounds(t, tt, "Metrics"); got != m {
		t.Fatalf("after ten toggles at %+v, want %+v", got, m)
	}
}

// TestMetricsCornersSurviveCompact: a window dragged into each corner,
// and one let go at the top centre, keeps that corner or centre through
// compact and back.
func TestMetricsCornersSurviveCompact(t *testing.T) {
	type pin func(r mui.Rect) (float32, float32)
	right := func(r mui.Rect) float32 { return testWidth - r.X - r.W }
	bottom := func(r mui.Rect) float32 { return testHeight - r.Y - r.H }
	cases := []struct {
		name   string
		dx, dy func(m mui.Rect) float32
		pin    pin
		want   [2]float32
	}{
		{"top-left", func(m mui.Rect) float32 { return -2000 }, func(m mui.Rect) float32 { return -2000 },
			func(r mui.Rect) (float32, float32) { return r.X, r.Y }, [2]float32{metricsInset, metricsInset}},
		{"bottom-left", func(m mui.Rect) float32 { return -2000 }, func(m mui.Rect) float32 { return 2000 },
			func(r mui.Rect) (float32, float32) { return r.X, bottom(r) }, [2]float32{metricsInset, metricsInset}},
		{"bottom-right", func(m mui.Rect) float32 { return 2000 }, func(m mui.Rect) float32 { return 2000 },
			func(r mui.Rect) (float32, float32) { return right(r), bottom(r) }, [2]float32{metricsInset, metricsInset}},
		{"top-center", func(m mui.Rect) float32 { return testWidth/2 - (m.X + m.W/2) + 5 }, func(m mui.Rect) float32 { return 100 },
			func(r mui.Rect) (float32, float32) { return r.X + r.W/2 - testWidth/2, r.Y }, [2]float32{5, metricsMargin + 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, tt, _ := newStreamTester(t, streamFake())
			m := showMetrics(t, tt)
			head := bounds(t, tt, "Metrics window bar")
			drag(tt, head.X+40, head.Y+10, head.X+40+tc.dx(m), head.Y+10+tc.dy(m))
			start := bounds(t, tt, "Metrics")
			for i := range 6 {
				x, y := tc.pin(bounds(t, tt, "Metrics"))
				if x != tc.want[0] || y != tc.want[1] {
					t.Fatalf("toggle %d: pinned at %v, %v, want %v (window %+v)", i, x, y, tc.want, bounds(t, tt, "Metrics"))
				}
				toggleCompact(t, tt)
			}
			if got := bounds(t, tt, "Metrics"); got != start {
				t.Fatalf("after six toggles at %+v, want %+v", got, start)
			}
		})
	}
}

// TestMetricsFollowsLiveResize: the top-right window stays 16 px in from
// the top-right corner as the window grows, shrinks below its size and
// grows back, and the layout is not stored again.
func TestMetricsFollowsLiveResize(t *testing.T) {
	f := streamFake()
	_, tt, _ := newStreamTester(t, f)
	showMetrics(t, tt)
	toggleCompact(t, tt)
	tt.Frame()
	saves := f.layoutSaves
	for _, s := range [][2]int{{1280, 720}, {2560, 1440}, {5120, 1440}, {2560, 1440}, {640, 480}, {300, 200}, {2560, 1440}, {testWidth, testHeight}} {
		resize(tt, s[0], s[1])
		r := bounds(t, tt, "Metrics")
		wantX := max(float32(s[0])-metricsMargin-r.W, metricsInset)
		if s[0] > 300 && (r.X != wantX || r.Y != metricsMargin) {
			t.Fatalf("%v: window at %+v, want x %v y 16", s, r, wantX)
		}
		if r.X < 0 || r.Y < 0 {
			t.Fatalf("%v: window off screen at %+v", s, r)
		}
	}
	if f.layoutSaves != saves {
		t.Fatalf("resizing stored the layout %d times", f.layoutSaves-saves)
	}
}

// TestButtonFollowsLiveResize: a floating button let go near the
// bottom-right corner keeps its distance from it across resizes, held
// inside a view too small for it, and lands back where it was.
func TestButtonFollowsLiveResize(t *testing.T) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	dockTo(t, tt, testWidth-200, testHeight-150)
	if a.ovl.dock != DockNone {
		t.Fatalf("docked %v", a.ovl.dock)
	}
	b0 := bounds(t, tt, "Stream controls")
	tt.Move(10, 10)
	tt.Frame()
	saves := f.layoutSaves
	for _, s := range [][2]int{{2560, 1440}, {5120, 1440}, {640, 480}, {200, 150}, {2560, 1440}, {testWidth, testHeight}} {
		resize(tt, s[0], s[1])
		b := bounds(t, tt, "Stream controls")
		if b.X < btnInset-1 || b.Y < btnInset-1 || b.X+b.W > float32(s[0])-btnInset+1 && s[0] > 100 || b.Y+b.H > float32(s[1])-btnInset+1 && s[1] > 100 {
			t.Fatalf("%v: button at %+v, off the view", s, b)
		}
		if s[0] >= 640 && (float32(s[0])-b.X != testWidth-b0.X || float32(s[1])-b.Y != testHeight-b0.Y) {
			t.Fatalf("%v: button at %+v, want %v, %v from the bottom-right", s, b, testWidth-b0.X, testHeight-b0.Y)
		}
	}
	if b := bounds(t, tt, "Stream controls"); b != b0 {
		t.Fatalf("back at %v x %v: button at %+v, want %+v", testWidth, testHeight, b, b0)
	}
	if f.layoutSaves != saves {
		t.Fatalf("resizing stored the layout %d times", f.layoutSaves-saves)
	}
}

// TestMetricsBottomRightSurvivesShrink: an expanded window in the
// bottom-right corner is held inside a view smaller than it and comes back
// to the corner when the view grows again.
func TestMetricsBottomRightSurvivesShrink(t *testing.T) {
	f := streamFake()
	_, tt, _ := newStreamTester(t, f)
	showMetrics(t, tt)
	head := bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+2040, head.Y+2010)
	saves := f.layoutSaves
	for _, s := range [][2]int{{2560, 1440}, {640, 480}, {320, 240}, {2560, 1440}} {
		resize(tt, s[0], s[1])
		r := bounds(t, tt, "Metrics")
		if s[0] >= 640 && (r.X+r.W != float32(s[0])-metricsInset || r.Y+r.H != float32(s[1])-metricsInset) {
			t.Fatalf("%v: window at %+v, want in the bottom-right corner", s, r)
		}
		if r.X < metricsInset || r.Y < metricsInset {
			t.Fatalf("%v: window at %+v, past the top-left", s, r)
		}
	}
	if f.layoutSaves != saves {
		t.Fatalf("resizing stored the layout %d times", f.layoutSaves-saves)
	}
}

// TestMetricsKeepOutIsNotKept: a window dragged against the edge the
// button is docked to stays 28 px off it, through compact and back, and
// sits 12 px off that edge once the button docks elsewhere.
func TestMetricsKeepOutIsNotKept(t *testing.T) {
	f := streamFake()
	a, tt, _ := dockedLeft(t, f)
	showMetrics(t, tt)
	head := bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+40-2000, head.Y+10+150)
	y := bounds(t, tt, "Metrics").Y
	for i := range 4 {
		if r := bounds(t, tt, "Metrics"); r.X != metricsKeepOut || r.Y != y {
			t.Fatalf("toggle %d: window at %+v, want 28 px off the docked edge at y %v", i, r, y)
		}
		toggleCompact(t, tt)
	}
	if p := a.ovl.metrics.at; p.AnchorX != AnchorStart || p.OffsetX != metricsInset {
		t.Fatalf("stored %+v, want 12 px from the start", p)
	}
	a.ovl.dock = DockRight
	tt.Frame()
	if r := bounds(t, tt, "Metrics"); r.X != metricsInset || r.Y != y {
		t.Fatalf("docked right: window at %+v, want 12 px off the left edge", r)
	}
	a.ovl.dock = DockLeft
	tt.Frame()
	if r := bounds(t, tt, "Metrics"); r.X != metricsKeepOut {
		t.Fatalf("docked left again: window at %+v, want 28 px off it", r)
	}
	f.layout.Dock = DockBottom
	_, tt2, _ := newStreamTester(t, f)
	tt2.Frame()
	if r := bounds(t, tt2, "Metrics"); r.X != metricsInset {
		t.Fatalf("next stream docked bottom: window at %+v, want 12 px off the left edge", r)
	}
}

// TestMetricsChevronNeverMoves: a press on the chevron or the head that
// wanders a few pixels before letting go moves nothing and stores nothing
// new.
func TestMetricsChevronNeverMoves(t *testing.T) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	m := showMetrics(t, tt)
	at := a.ovl.metrics.at
	for i := range 6 {
		name := "Compact metrics"
		if i%2 == 1 {
			name = "Expand metrics"
		}
		x, y := centre(bounds(t, tt, name))
		tt.Move(x, y)
		tt.Press(x, y)
		tt.Frame()
		tt.Move(x+2, y+2)
		tt.Frame()
		tt.Release(x+2, y+2)
		tt.Frame()
		if a.ovl.metrics.at != at {
			t.Fatalf("press %d on %q stored %+v, want %+v", i, name, a.ovl.metrics.at, at)
		}
	}
	if r := bounds(t, tt, "Metrics"); r.X+r.W != m.X+m.W || r.Y != m.Y {
		t.Fatalf("window at %+v, want its top-right corner at %v, %v", r, m.X+m.W, m.Y)
	}
	head := bounds(t, tt, "Metrics window bar")
	drag(tt, head.X+40, head.Y+10, head.X+42, head.Y+11)
	if a.ovl.metrics.at != at || a.ovl.metrics.dragging {
		t.Fatalf("a 2 px wobble on the head stored %+v", a.ovl.metrics.at)
	}
}

// TestLegacyLayoutMigrates: a layout stored as fractions of the view
// opens where it was, anchored, and is stored once as placements.
func TestLegacyLayoutMigrates(t *testing.T) {
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, ButtonX: 0.5, ButtonY: float32(testHeight-100) / testHeight,
		Metrics: true, MetricsX: float32(testWidth-16-metricsW) / testWidth, MetricsY: 16.0 / testHeight}
	_, tt, _ := newStreamTester(t, f)
	tt.Move(10, 10)
	tt.Frame()
	if r := bounds(t, tt, "Metrics"); r != (mui.Rect{X: testWidth - 16 - metricsW, Y: 16, W: metricsW, H: metricsH}) {
		t.Fatalf("metrics at %+v, want top-right 16 px in", r)
	}
	if b := bounds(t, tt, "Stream controls"); b.X != testWidth/2-btnSize/2 || b.Y != testHeight-100-btnSize/2 {
		t.Fatalf("button at %+v", b)
	}
	want := OverlayLayout{Placed: true, Metrics: true,
		ButtonPlace:  Placement{AnchorX: AnchorCenter, AnchorY: AnchorEnd, OffsetY: 100 - btnSize/2},
		MetricsPlace: Placement{AnchorX: AnchorEnd, AnchorY: AnchorStart, OffsetX: 16, OffsetY: 16}}
	if f.layout != want || f.layoutSaves != 1 {
		t.Fatalf("stored %+v after %d saves, want %+v once", f.layout, f.layoutSaves, want)
	}
	resize(tt, 2560, 1440)
	if r := bounds(t, tt, "Metrics"); r.X != 2560-16-metricsW || r.Y != 16 {
		t.Fatalf("2560 x 1440: metrics at %+v", r)
	}
	if b := bounds(t, tt, "Stream controls"); b.X != 1280-btnSize/2 || b.Y != 1440-100-btnSize/2 {
		t.Fatalf("2560 x 1440: button at %+v", b)
	}
	if f.layoutSaves != 1 {
		t.Fatalf("resize stored the layout again: %d saves", f.layoutSaves)
	}
}

// TestButtonCentredStaysCentred: a button let go near the middle keeps
// to the centre as the view resizes.
func TestButtonCentredStaysCentred(t *testing.T) {
	a, tt, _ := newStreamTester(t, streamFake())
	dockTo(t, tt, testWidth/2+10, testHeight/2-20)
	if p := a.ovl.btnAt; p != (Placement{AnchorX: AnchorCenter, AnchorY: AnchorCenter, OffsetX: 10, OffsetY: -20}) {
		t.Fatalf("placement %+v", p)
	}
	for _, s := range [][2]int{{2560, 1440}, {640, 480}, {testWidth, testHeight}} {
		resize(tt, s[0], s[1])
		if x, y := centre(bounds(t, tt, "Stream controls")); x != float32(s[0])/2+10 || y != float32(s[1])/2-20 {
			t.Fatalf("%v: button centred on %v, %v", s, x, y)
		}
	}
}

func TestPlacementAt(t *testing.T) {
	b := mui.Rect{X: 12, Y: 12, W: 1000 - 24 - 100, H: 800 - 24 - 50}
	cases := []struct {
		p    mui.Point
		want Placement
	}{
		{mui.Point{X: 12, Y: 12}, Placement{AnchorStart, AnchorStart, 12, 12}},
		{mui.Point{X: 888, Y: 738}, Placement{AnchorEnd, AnchorEnd, 12, 12}},
		{mui.Point{X: 100, Y: 600}, Placement{AnchorStart, AnchorEnd, 100, 150}},
		{mui.Point{X: 450 + 32, Y: 375 - 32}, Placement{AnchorCenter, AnchorCenter, 32, -32}},
		{mui.Point{X: 450 + 33, Y: 200}, Placement{AnchorEnd, AnchorStart, 417, 200}},
	}
	for _, tc := range cases {
		got := placementAt(tc.p, 100, 50, 1000, 800, b, 12)
		if got != tc.want {
			t.Errorf("at %v: %+v, want %+v", tc.p, got, tc.want)
		}
		if r := got.resolve(1000, 800, 100, 50); r != tc.p {
			t.Errorf("at %v: resolves to %v", tc.p, r)
		}
	}
	kept := mui.Rect{X: 28, Y: 12, W: 1000 - 12 - 28 - 100, H: 800 - 24 - 50}
	if got := placementAt(mui.Point{X: 28, Y: 300}, 100, 50, 1000, 800, kept, 12); got.AnchorX != AnchorStart || got.OffsetX != 12 {
		t.Errorf("against a keep-out: %+v, want 12 from the start", got)
	}
}
