package player

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/gpusurface"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// fakeSink records the overlays set, copying them as the surface does.
type fakeSink struct {
	mu      sync.Mutex
	w, h    int
	scale   float64
	atlases int
	sets    int
	last    [gpusurface.MaxOverlayRects]gpusurface.OverlayRect
	n       int
}

func (s *fakeSink) SetOverlay(r []gpusurface.OverlayRect) {
	s.mu.Lock()
	s.n = copy(s.last[:], r)
	s.sets++
	s.mu.Unlock()
}

func (s *fakeSink) SetOverlayAtlas(_ []byte, w, h int) error {
	s.mu.Lock()
	s.atlases++
	s.mu.Unlock()
	return nil
}

func (s *fakeSink) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h
}

func (s *fakeSink) Scale() float64 { return s.scale }

func (s *fakeSink) rects() []gpusurface.OverlayRect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gpusurface.OverlayRect(nil), s.last[:s.n]...)
}

func hudPlayer(sink *fakeSink) *Player {
	p := &Player{}
	p.hud.sink = sink
	return p
}

var goldenState = HUDState{Visible: true, Signal: SignalDirect, Route: "LAN", FPS: 119.6, LatencyMs: 4.24, Mbps: 85.3, Codec: "HEVC 10-bit"}

// TestHUDGolden lays the pill out on a 3840x2160 drawable at scale 2 and
// compares every rect with the golden file (-update rewrites it).
func TestHUDGolden(t *testing.T) {
	sink := &fakeSink{w: 3840, h: 2160, scale: 2}
	p := hudPlayer(sink)
	p.SetHUD(goldenState)
	got := sink.rects()
	path := filepath.Join("testdata", "hud_golden.json")
	if *update {
		b, _ := json.MarshalIndent(got, "", " ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []gpusurface.OverlayRect
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d rects, golden has %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("rect %d: %+v, golden %+v", i, got[i], want[i])
		}
	}
}

// TestHUDGeometry checks the pill against the shared constants: centred,
// HUDPillTopPt from the top, HUDPillHeightPt high, every glyph inside it,
// the dot in the signal's colour.
func TestHUDGeometry(t *testing.T) {
	for _, c := range []struct {
		w, h  int
		scale float64
	}{{3840, 2160, 2}, {5120, 1440, 1}, {3024, 1964, 2}} {
		sink := &fakeSink{w: c.w, h: c.h, scale: c.scale}
		st := goldenState
		st.Signal = SignalRelay
		hudPlayer(sink).SetHUD(st)
		rs := sink.rects()
		if len(rs) < 3 {
			t.Fatalf("%v: %d rects", c, len(rs))
		}
		pill := rs[0]
		s := float32(c.scale)
		if pill.Y != HUDPillTopPt*s || pill.H != HUDPillHeightPt*s || pill.Radius != HUDPillRadiusPt*s {
			t.Errorf("%v: pill %+v", c, pill)
		}
		if l, r := pill.X, float32(c.w)-pill.X-pill.W; l-r > 1 || r-l > 1 {
			t.Errorf("%v: pill not centred, %v left, %v right", c, l, r)
		}
		if rs[1].Color != hudSignal[SignalRelay] {
			t.Errorf("%v: dot colour %v", c, rs[1].Color)
		}
		for i, g := range rs[2:] {
			if g.UV == [4]float32{} {
				t.Errorf("%v: rect %d after the dot is not a glyph", c, i+2)
			}
			if g.X < pill.X+HUDPillPadPt*s-1 || g.X+g.W > pill.X+pill.W-HUDPillPadPt*s+1 || g.Y < pill.Y || g.Y+g.H > pill.Y+pill.H {
				t.Errorf("%v: glyph %+v outside the pill %+v", c, g, pill)
			}
		}
	}
}

// TestHUDLayoutAllocsNothing: once the font is made, laying the pill out
// again allocates nothing.
func TestHUDLayoutAllocsNothing(t *testing.T) {
	sink := &fakeSink{w: 3840, h: 2160, scale: 2}
	p := hudPlayer(sink)
	p.SetHUD(goldenState)
	h := &p.hud
	fps := 0.0
	if n := testing.AllocsPerRun(100, func() {
		fps++
		h.mu.Lock()
		h.state.FPS = fps
		h.layout()
		h.mu.Unlock()
	}); n != 0 {
		t.Fatalf("layout allocates %.1f times", n)
	}
}

// TestHUDCoalesces: states set faster than four a second reach the surface
// at most once a quarter second, the newest last; hiding shows at once.
func TestHUDCoalesces(t *testing.T) {
	sink := &fakeSink{w: 1920, h: 1080, scale: 1}
	p := hudPlayer(sink)
	st := goldenState
	for i := range 20 {
		st.FPS = float64(i)
		p.SetHUD(st)
	}
	sink.mu.Lock()
	sets := sink.sets
	sink.mu.Unlock()
	if sets != 1 {
		t.Fatalf("%d overlays set at once, want 1", sets)
	}
	time.Sleep(hudInterval + 100*time.Millisecond)
	sink.mu.Lock()
	sets = sink.sets
	sink.mu.Unlock()
	if sets != 2 {
		t.Fatalf("%d overlays set after a quarter second, want 2", sets)
	}
	p.hud.mu.Lock()
	text, _ := p.hud.compose(&p.hud.state)
	p.hud.mu.Unlock()
	if want := "LAN · 4.2 ms · 19 fps · 85 Mbps · HEVC 10-bit"; string(text) != want {
		t.Fatalf("text %q, want %q", text, want)
	}
	st.Visible = false
	p.SetHUD(st)
	if n := len(sink.rects()); n != 0 {
		t.Fatalf("%d rects right after hiding", n)
	}
	p.hud.stop()
}

// TestHUDFollowsResize: when the drawables change size, as direct mode
// begins, the present thread lays the pill out again for the new width.
func TestHUDFollowsResize(t *testing.T) {
	sink := &fakeSink{w: 1920, h: 1080, scale: 1}
	p := hudPlayer(sink)
	p.SetHUD(goldenState)
	before := sink.rects()[0]
	sink.mu.Lock()
	sink.w, sink.h = 5120, 1440
	sink.mu.Unlock()
	p.hudResized()
	after := sink.rects()[0]
	if after.X != before.X+(5120-1920)/2 {
		t.Fatalf("pill at x %v after the drawables went from 1920 to 5120 wide, was %v", after.X, before.X)
	}
	if n := testing.AllocsPerRun(100, p.hudResized); n != 0 {
		t.Fatalf("hudResized with no change allocates %.1f times", n)
	}
}

// TestHUDAtlasGlyphsDiffer: every glyph's place in the atlas holds ink,
// and no two glyphs hold the same ink at the same height.
func TestHUDAtlasGlyphsDiffer(t *testing.T) {
	f, err := newHUDFont(2)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]rune{}
	for _, r := range hudGlyphs {
		g := f.glyph(r)
		if !g.ok {
			t.Errorf("%q has no glyph", r)
			continue
		}
		x0, y0 := int(g.uv[0]*float32(f.aw)+0.5), int(g.uv[1]*float32(f.ah)+0.5)
		cell := make([]byte, 0, g.w*g.h)
		ink := 0
		for y := y0; y < y0+g.h; y++ {
			row := f.atlas[y*f.aw+x0 : y*f.aw+x0+g.w]
			cell = append(cell, row...)
			for _, c := range row {
				ink += int(c)
			}
		}
		if ink == 0 {
			t.Errorf("%q has no ink in the atlas", r)
		}
		key := fmt.Sprint(g.x, g.y, g.w, g.h, cell)
		if other, dup := seen[key]; dup {
			t.Errorf("%q looks the same as %q in the atlas", r, other)
		}
		seen[key] = r
	}
}
