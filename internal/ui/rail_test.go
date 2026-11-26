package ui

import (
	"image"
	"strings"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// TestHostingTooltipShowsOnce: the hosting button's status card is its
// only tooltip; the rail's plain name tooltip drew under it too.
func TestHostingTooltipShowsOnce(t *testing.T) {
	f := newFake()
	f.hosting = HostingState{Encoder: "Apple GPU VideoToolbox", Err: "Another app is using UDP port 41000."}
	a, tt := newTester(t, f)
	name := "Host this " + machineWord(a.goos)
	r, ok := tt.Find(name)
	if !ok {
		t.Fatalf("no %q button", name)
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	snapshot(t, tt, "hosting-tooltip")
	n := 0
	for _, s := range tt.Texts() {
		if s == name {
			n++
		}
	}
	// The button's own label, and the card's title.
	if n != 2 {
		t.Fatalf("%q shows %d times, want 2 (button and one tooltip): %q", name, n, tt.Texts())
	}
	for _, s := range []string{"off", "Apple GPU VideoToolbox", "Another app is using UDP port 41000."} {
		if !tt.HasText(s) {
			t.Errorf("tooltip misses %q", s)
		}
	}
}

// inkSpan is the first and last row inside r that differ from bg.
func inkSpan(img *image.RGBA, r image.Rectangle, bg [4]uint8) (top, bot int, ok bool) {
	top, bot = -1, -1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := img.RGBAAt(x, y)
			if [4]uint8{p.R, p.G, p.B, p.A} != bg {
				if top < 0 {
					top = y
				}
				bot = y
				break
			}
		}
	}
	return top, bot, top >= 0
}

type railScreen struct {
	screen Screen
	sub    string
	name   string
}

func railScreens(a *App) []railScreen {
	return []railScreen{
		{ScreenHosts, "Searching your network and tailnet", "hosts"},
		{ScreenStatistics, "Nothing is streaming right now.", "statistics"},
		{ScreenSettings, "How this " + machineWord(a.goos) + " opens streams and what it shares with them.", "settings"},
	}
}

// firstInk is the first row at or below y0 where column x differs from bg.
func firstInk(img *image.RGBA, x, y0 int, bg [4]uint8) int {
	for y := y0; y < img.Bounds().Max.Y; y++ {
		p := img.RGBAAt(x, y)
		if [4]uint8{p.R, p.G, p.B, p.A} != bg {
			return y
		}
	}
	return -1
}

// railGeometry is what one screen measures: the header block (title top to
// subtitle bottom), the first row below it, the Hosts button and the logo ink.
type railGeometry struct {
	headTop, headBot, rowTop float64
	btnTop, btnBot           float64
	logoTop, logoBot         int
}

func measureRail(t *testing.T, a *App, tt *mui.Tester, s railScreen) railGeometry {
	t.Helper()
	a.screen = s.screen
	a.Notify()
	tt.Frame()
	snapshot(t, tt, "rail-"+s.name)
	sub, ok := tt.Find(s.sub)
	if !ok {
		t.Fatalf("%s: subtitle %q not found", s.name, s.sub)
	}
	btn, ok := tt.Find("Hosts")
	if !ok {
		t.Fatalf("%s: no Hosts rail button", s.name)
	}
	img := tt.Image()
	pg := img.RGBAAt(int(sub.X)-8, 2)
	bg := [4]uint8{pg.R, pg.G, pg.B, pg.A}
	g := railGeometry{
		headTop: float64(sub.Y) - pageHeaderGap - pageTitleLine,
		headBot: float64(sub.Y + sub.H),
		btnTop:  float64(btn.Y), btnBot: float64(btn.Y + btn.H),
	}
	y0 := int(sub.Y+sub.H) + 1
	g.rowTop = float64(firstInk(img, int(sub.X)+10, y0, bg))
	if s.screen == ScreenSettings {
		sb := img.RGBAAt(144, y0)
		sbg := [4]uint8{sb.R, sb.G, sb.B, sb.A}
		side := float64(firstInk(img, 144, y0, sbg))
		t.Logf("settings: search field top %.0f, first panel top %.0f", side, g.rowTop)
		if side != g.rowTop {
			t.Errorf("settings: search field top %.0f, first panel top %.0f", side, g.rowTop)
		}
	}
	rl := img.RGBAAt(4, 2)
	var okLogo bool
	g.logoTop, g.logoBot, okLogo = inkSpan(img, image.Rect(20, 0, 68, 90), [4]uint8{rl.R, rl.G, rl.B, rl.A})
	if !okLogo {
		t.Fatalf("%s: no logo ink", s.name)
	}
	return g
}

// TestRailLogoAlignsWithPageTitle: the mark's ink is centred on the whole
// header block, title and subtitle, on every rail screen.
func TestRailLogoAlignsWithPageTitle(t *testing.T) {
	a, tt := newTester(t, newFake())
	for _, s := range railScreens(a) {
		g := measureRail(t, a, tt, s)
		hc, lc := (g.headTop+g.headBot)/2, float64(g.logoTop+g.logoBot)/2
		t.Logf("%s: header %.0f..%.0f (centre %.1f), logo ink %d..%d (centre %.1f)", s.name, g.headTop, g.headBot, hc, g.logoTop, g.logoBot, lc)
		if d := hc - lc; d > 1 || d < -1 {
			t.Errorf("%s: logo centre %.1f is %.1f px from header centre %.1f", s.name, lc, d, hc)
		}
		if g.headTop != pagePadTop || g.headBot != pagePadTop+pageHeaderH {
			t.Errorf("%s: header %.0f..%.0f, want %d..%d", s.name, g.headTop, g.headBot, pagePadTop, pagePadTop+pageHeaderH)
		}
	}
}

// TestRailHostsAlignsWithFirstRow: the Hosts button is centred on the first
// row under the header: the quick-connect field on Hosts, the first panel
// on Statistics and Settings, and the Settings search field beside it.
func TestRailHostsAlignsWithFirstRow(t *testing.T) {
	a, tt := newTester(t, newFake())
	for _, s := range railScreens(a) {
		g := measureRail(t, a, tt, s)
		bc := (g.btnTop + g.btnBot) / 2
		t.Logf("%s: first row top %.0f, Hosts button %.0f..%.0f (centre %.1f)", s.name, g.rowTop, g.btnTop, g.btnBot, bc)
		if g.rowTop != pageRowTop {
			t.Errorf("%s: first row top %.0f, want %d", s.name, g.rowTop, pageRowTop)
		}
		if want := g.rowTop + pageFirstRowH/2; bc != want {
			t.Errorf("%s: Hosts button centre %.1f, want the first row's centre %.1f", s.name, bc, want)
		}
	}
	a.screen = ScreenHosts
	a.Notify()
	tt.Frame()
	in, ok := tt.Find("Connect")
	btn, _ := tt.Find("Hosts")
	if !ok {
		t.Fatal("no Connect button")
	}
	if ic, bc := in.Y+in.H/2, btn.Y+btn.H/2; ic != bc {
		t.Errorf("quick-connect row centre %.1f, Hosts button centre %.1f", ic, bc)
	}
}

// TestSettingsHeaderStaysOneLineAtMinimumWindow: at the 960x600 minimum
// every Settings category keeps its one-line header (24..76, centre 50),
// so the rail logo stays centred, and its first panel and the sidebar's
// search field both start at 100, centred on the Hosts button.
func TestSettingsHeaderStaysOneLineAtMinimumWindow(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		for cat := settingsCategory(0); cat < numCategories; cat++ {
			name := goos + "-" + categoryNames[cat]
			f := newFake()
			if err := theme.RegisterFonts(); err != nil {
				t.Fatal(err)
			}
			a := NewApp(f)
			a.goos = goos
			tt := mui.NewTester(a.View, 960, 600)
			a.screen, a.set.cat = ScreenSettings, cat
			a.Notify()
			tt.Frame()
			snapshot(t, tt, "minimum-"+goos+"-"+strings.NewReplacer(" ", "", "&", "").Replace(categoryNames[cat]))

			var sub string
			for _, s := range tt.Texts() {
				if sub == "" && strings.HasSuffix(s, ".") && (strings.HasPrefix(s, "This ") || strings.HasPrefix(s, "How ") || strings.HasPrefix(s, "What ") || strings.HasPrefix(s, "Controllers ") || strings.HasPrefix(s, "Chords ")) {
					sub = s
				}
			}
			r, ok := tt.Find(sub)
			if !ok || sub == "" {
				t.Fatalf("%s: no subtitle found", name)
			}
			// Layout sums in float32; a millipixel is noise, not a move.
			const eps = 1e-3
			if r.H < pageSubLine-eps || r.H > pageSubLine+1 || r.Y+r.H < pagePadTop+pageHeaderH-eps || r.Y+r.H > pagePadTop+pageHeaderH+1 {
				t.Errorf("%s: subtitle %.2f..%.2f (wrapped or moved), want bottom %d, height %d", name, r.Y, r.Y+r.H, pagePadTop+pageHeaderH, pageSubLine)
			}
			img := tt.Image()
			pg := img.RGBAAt(int(r.X)-8, 2)
			bg := [4]uint8{pg.R, pg.G, pg.B, pg.A}
			y0 := int(r.Y+r.H) + 1
			row := firstInk(img, int(r.X)+10, y0, bg)
			sb := img.RGBAAt(144, y0)
			side := firstInk(img, 144, y0, [4]uint8{sb.R, sb.G, sb.B, sb.A})
			if row != pageRowTop || side != pageRowTop {
				t.Errorf("%s: first panel top %d, search field top %d, want %d", name, row, side, pageRowTop)
			}
			btn, _ := tt.Find("Hosts")
			if bc := float64(btn.Y + btn.H/2); bc != float64(pageRowTop+pageFirstRowH/2) {
				t.Errorf("%s: Hosts button centre %.1f, want %d", name, bc, pageRowTop+pageFirstRowH/2)
			}
			rl := img.RGBAAt(4, 2)
			top, bot, ok := inkSpan(img, image.Rect(20, 0, 68, 90), [4]uint8{rl.R, rl.G, rl.B, rl.A})
			if !ok {
				t.Fatalf("%s: no logo ink", name)
			}
			if d := float64(top+bot)/2 - float64(pagePadTop+pageHeaderH/2); d > 1 || d < -1 {
				t.Errorf("%s: logo centre off the header centre by %.1f", name, d)
			}
		}
	}
}
