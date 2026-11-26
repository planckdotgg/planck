package ui

import (
	"slices"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/defaults"
)

// streamProbe stands in for the client's input surface: it counts the
// presses that reach the stream under the overlay.
type streamProbe struct {
	presses int
	pressed bool
}

func (p *streamProbe) view(c *mui.Context) {
	c.Key("probe-stream-input")
	el := mui.Box(c).Fill().Label("Stream")
	_, pressed := el.PressedButton()
	if pressed && !p.pressed {
		p.presses++
	}
	p.pressed = pressed
}

// newStreamTester connects at the tester's start, with a probe as the
// stream view and the keys printed as on Windows.
func newStreamTester(t *testing.T, f *fakeBackend) (*App, *mui.Tester, *streamProbe) {
	t.Helper()
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	a := NewApp(f)
	a.goos = "windows"
	p := &streamProbe{}
	a.SetStreamView(p.view)
	return a, mui.NewTester(a.View, testWidth, testHeight), p
}

func streamFake() *fakeBackend {
	f := newFake()
	f.tel = sampleTelemetry()
	f.tel.Profile = sampleProfile()
	return f
}

func bounds(t *testing.T, tt *mui.Tester, name string) mui.Rect {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("no %q: %q", name, tt.Texts())
	}
	return info.Bounds
}

func shows(tt *mui.Tester, name string) bool {
	_, ok := tt.Access(name)
	return ok
}

func centre(r mui.Rect) (float32, float32) { return r.X + r.W/2, r.Y + r.H/2 }

// drag presses at (x0, y0), moves to (x1, y1) in steps, a frame each,
// slowly enough that nothing is thrown, and lets go.
func drag(tt *mui.Tester, x0, y0, x1, y1 float32) {
	tt.Move(x0, y0)
	tt.Press(x0, y0)
	tt.Frame()
	const steps = 12
	for i := 1; i <= steps; i++ {
		k := float32(i) / steps
		tt.Move(x0+(x1-x0)*k, y0+(y1-y0)*k)
		tt.Advance(50 * time.Millisecond)
	}
	tt.Advance(100 * time.Millisecond)
	tt.Release(x1, y1)
	tt.Frame()
}

// dockTo drags the button so that its centre lets go at (x, y), and lets
// the melt finish.
func dockTo(t *testing.T, tt *mui.Tester, x, y float32) {
	t.Helper()
	bx, by := centre(bounds(t, tt, "Stream controls"))
	drag(tt, bx, by, x, y)
	tt.Advance(time.Second)
}

func TestOverlayFirstStreamFloatsByTheLeftEdge(t *testing.T) {
	f := streamFake()
	_, tt, _ := newStreamTester(t, f)
	b := bounds(t, tt, "Stream controls")
	if b.X != 24 || b.Y != testHeight/2-btnSize/2 || b.W != btnSize || b.H != btnSize {
		t.Fatalf("first stream: button at %+v, want 44 x 44 at 24 off the left edge, centred", b)
	}
	if !tt.HasText("Ctrl Alt O") {
		t.Fatalf("first stream: tip missing: %q", tt.Texts())
	}
	snapshot(t, tt, "overlay-first-stream")
	tt.Advance(tipFirst + 100*time.Millisecond)
	tt.Move(900, 700)
	tt.Frame()
	if tt.HasText("Ctrl Alt O") {
		t.Fatal("first-stream tip still shows after 4 s")
	}
}

// TestOverlayDragDocksWithin56: let go with its centre 60 px from an edge
// the button floats; at 50 px it docks, melting into an 8 x 40 tab flush
// with the edge, and the dock comes back on the next stream.
func TestOverlayDragDocksWithin56(t *testing.T) {
	f := streamFake()
	a, tt, _ := newStreamTester(t, f)
	dockTo(t, tt, 700, 400)
	if a.ovl.dock != DockNone {
		t.Fatalf("let go mid-view: docked %v", a.ovl.dock)
	}
	if b := bounds(t, tt, "Stream controls"); b.X+b.W/2 != 700 || b.Y+b.H/2 != 400 {
		t.Fatalf("floating button rests at %+v, want centred on 700, 400", b)
	}

	dockTo(t, tt, testWidth-60, 300)
	if a.ovl.dock != DockNone {
		t.Fatalf("let go 60 px from the right edge: docked %v", a.ovl.dock)
	}

	bx, by := centre(bounds(t, tt, "Stream controls"))
	tt.Move(bx, by)
	tt.Press(bx, by)
	tt.Frame()
	for i := 1; i <= 8; i++ {
		tt.Move(bx+float32(i)*(testWidth-50-bx)/8, by)
		tt.Advance(50 * time.Millisecond)
	}
	if !shows(tt, "Dock target") {
		t.Fatal("no dock target lit while dragging within 56 px of the edge")
	}
	snapshot(t, tt, "overlay-dock-target")
	tt.Advance(100 * time.Millisecond)
	tt.Release(testWidth-50, by)
	tt.Frame()
	if a.ovl.dock != DockRight {
		t.Fatalf("let go 50 px from the right edge: dock %v, want right", a.ovl.dock)
	}
	if shows(tt, "Stream controls") {
		t.Fatal("round button still shows as it melts")
	}
	tt.Advance(meltTime + 50*time.Millisecond)
	tt.Move(700, 400)
	tt.Frame()
	tab, hit, _ := a.ovl.dockGeom()
	if tab.X != testWidth-tabShort || tab.W != tabShort || tab.H != tabLong {
		t.Fatalf("tab %+v, want 8 x 40 flush with the right edge", tab)
	}
	if h := bounds(t, tt, "Show stream controls"); h != hit || h.W != hitShort || h.H != hitLong {
		t.Fatalf("hit zone %+v, want 24 x 72 at %+v", h, hit)
	}
	snapshot(t, tt, "overlay-docked-right")
	if f.layout.Dock != DockRight || !f.layout.Placed {
		t.Fatalf("stored layout %+v, want docked right", f.layout)
	}

	again, tt2, _ := newStreamTester(t, f)
	tt2.Frame()
	if again.ovl.dock != DockRight || again.ovl.dockFrac != a.ovl.dockFrac {
		t.Fatalf("next stream: dock %v at %v, want right at %v", again.ovl.dock, again.ovl.dockFrac, a.ovl.dockFrac)
	}
}

func TestOverlayLeftBeatsTopInACorner(t *testing.T) {
	if e, _ := dockFor(30, 30, 1440, 900); e != DockLeft {
		t.Fatalf("corner: %v, want left", e)
	}
	if e, frac := dockFor(700, 880, 1440, 900); e != DockBottom || frac*1440 != 700 {
		t.Fatalf("bottom: %v at %v", e, frac)
	}
	if _, frac := dockFor(10, 5, 1440, 900); frac*900 != dockCorner+tabLong/2 {
		t.Fatalf("tab centre %v, want %d from the corner", frac*900, dockCorner+tabLong/2)
	}
}

// dockedLeft docks the button to the left edge and moves the pointer
// away.
func dockedLeft(t *testing.T, f *fakeBackend) (*App, *mui.Tester, *streamProbe) {
	t.Helper()
	f.layout = OverlayLayout{Placed: true, Dock: DockLeft, DockFrac: 0.5}
	a, tt, p := newStreamTester(t, f)
	tt.Move(700, 400)
	tt.Frame()
	return a, tt, p
}

// TestOverlayEdgeDwellPopsOut: a pointer crossing the hit zone does
// nothing; 80 ms in it pops the button out in 140 ms; it melts back 900 ms
// after the pointer leaves, in 110 ms.
func TestOverlayEdgeDwellPopsOut(t *testing.T) {
	a, tt, _ := dockedLeft(t, streamFake())
	hx, hy := centre(bounds(t, tt, "Show stream controls"))
	tt.Move(hx, hy)
	tt.Advance(50 * time.Millisecond)
	tt.Move(700, 400)
	tt.Advance(time.Second)
	if a.ovl.popped {
		t.Fatal("a 50 ms crossing popped the button out")
	}

	tt.Move(hx, hy)
	tt.Advance(dwellTime - 20*time.Millisecond)
	if a.ovl.popped {
		t.Fatal("popped out before the 80 ms dwell")
	}
	tt.Advance(30 * time.Millisecond)
	if !a.ovl.popped {
		t.Fatal("not popped out after 80 ms in the hit zone")
	}
	if shows(tt, "Stream controls") {
		t.Fatal("round button shows before its pop-out ends")
	}
	tt.Advance(popOutTime)
	b := bounds(t, tt, "Stream controls")
	if b.X != popGap || b.W != btnSize {
		t.Fatalf("popped-out button at %+v, want 6 off the edge", b)
	}
	snapshot(t, tt, "overlay-popped-out")

	tt.Move(700, 400)
	tt.Advance(lingerTime - 100*time.Millisecond)
	if !shows(tt, "Stream controls") {
		t.Fatal("melted back before 900 ms")
	}
	tt.Advance(200 * time.Millisecond)
	if a.ovl.popped {
		t.Fatal("still out 900 ms after the pointer left")
	}
	tt.Advance(popBack + 20*time.Millisecond)
	if shows(tt, "Stream controls") || !shows(tt, "Show stream controls") {
		t.Fatalf("after melting back: %q", tt.Texts())
	}
}

// TestOverlayHitZonePressOpensMenu: a press on the tab pops the button out
// and opens the menu; a press on the button closes it.
func TestOverlayHitZonePressOpensMenu(t *testing.T) {
	a, tt, p := dockedLeft(t, streamFake())
	hx, hy := centre(bounds(t, tt, "Show stream controls"))
	tt.ClickAt(hx, hy)
	tt.Frame()
	tt.Advance(popOutTime + 20*time.Millisecond)
	if !a.ovl.menuOpen || !tt.HasText("Immersive mode") {
		t.Fatalf("hit zone press: menu open %v: %q", a.ovl.menuOpen, tt.Texts())
	}
	if p.presses != 0 {
		t.Fatalf("hit zone press reached the stream %d times", p.presses)
	}
	tt.Move(700, 400)
	tt.Advance(3 * time.Second)
	if !a.ovl.popped {
		t.Fatal("button melted back while its menu is open")
	}
	bx, by := centre(bounds(t, tt, "Stream controls"))
	tt.ClickAt(bx, by)
	tt.Frame()
	if a.ovl.menuOpen || tt.HasText("Immersive mode") {
		t.Fatal("a press on the button left the menu open")
	}
	tt.ClickAt(bx, by)
	tt.Frame()
	if !a.ovl.menuOpen {
		t.Fatal("a press on the button did not open the menu")
	}
}

// TestOverlayHideShortcut: Ctrl+Alt+P takes everything away, toast aside,
// and the stream gets presses where the tab was; pressed again it all
// comes back, and hidden does not outlast the stream.
func TestOverlayHideShortcut(t *testing.T) {
	f := streamFake()
	a, tt, p := dockedLeft(t, f)
	hit := bounds(t, tt, "Show stream controls")
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyM)
	tt.Frame()
	if !shows(tt, "Metrics") {
		t.Fatal("metrics window not shown")
	}
	var shown []bool
	a.OnOverlayChange(func(s OverlayState) { shown = append(shown, s.Shown) })

	tt.Key(mui.Ctrl|mui.Alt, mui.KeyP)
	tt.Frame()
	for _, name := range []string{"Show stream controls", "Stream controls", "Metrics"} {
		if shows(tt, name) {
			t.Errorf("%q shows while hidden", name)
		}
	}
	if !tt.HasText("Overlay hidden") || !tt.HasText("Ctrl Alt P") {
		t.Fatalf("no recall toast: %q", tt.Texts())
	}
	snapshot(t, tt, "overlay-hidden-toast")
	hx, hy := centre(hit)
	tt.ClickAt(hx, hy)
	tt.Frame()
	if p.presses != 1 {
		t.Fatalf("press where the hidden tab was: stream got %d presses, want 1", p.presses)
	}
	toast := bounds(t, tt, "Overlay hidden")
	tt.ClickAt(centre(toast))
	tt.Frame()
	if p.presses != 2 {
		t.Fatalf("press on the toast: stream got %d presses, want 2", p.presses)
	}
	tt.Advance(toastIn + toastHold + toastOut)
	if tt.HasText("Overlay hidden") {
		t.Fatal("toast outlived 2.4 s")
	}
	a.mu.Lock()
	drawn := a.overlayShown
	a.mu.Unlock()
	if drawn {
		t.Fatal("overlay reported shown with nothing drawn")
	}

	tt.Key(mui.Ctrl|mui.Alt, mui.KeyP)
	tt.Frame()
	if !shows(tt, "Show stream controls") || !shows(tt, "Metrics") {
		t.Fatalf("Ctrl+Alt+P did not bring it back: %q", tt.Texts())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyP)
	tt.Frame()
	if shows(tt, "Show stream controls") {
		t.Fatal("Ctrl+Alt+P did not hide it again")
	}
	if want := []bool{false, true}; !slices.Equal(shown, want) {
		t.Fatalf("overlay changes %v, want %v: off once the toast went, on as it came back", shown, want)
	}

	f.Disconnect()
	tt.Frame()
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	tt.Frame()
	if !shows(tt, "Show stream controls") {
		t.Fatal("hidden lasted into the next stream")
	}
}

// TestMenuOpensInward: docked left the menu opens to the right of the
// button, docked right to its left, and its flyouts open further inward;
// Escape closes the flyout, then the menu.
func TestMenuOpensInward(t *testing.T) {
	for _, dock := range []DockEdge{DockLeft, DockRight} {
		f := streamFake()
		f.layout = OverlayLayout{Placed: true, Dock: dock, DockFrac: 0.3}
		a, tt, _ := newStreamTester(t, f)
		tt.Key(mui.Ctrl|mui.Alt, mui.KeyO)
		tt.Frame()
		tt.Advance(popOutTime + 20*time.Millisecond)
		btn := bounds(t, tt, "Stream controls")
		menu := bounds(t, tt, "Immersive mode")
		switch {
		case dock == DockLeft && menu.X < btn.X+btn.W+menuGap:
			t.Errorf("docked left: menu row at %+v, button %+v", menu, btn)
		case dock == DockRight && menu.X+menu.W > btn.X-menuGap:
			t.Errorf("docked right: menu row at %+v, button %+v", menu, btn)
		}
		if dock == DockRight {
			snapshot(t, tt, "menu-docked-right")
		}
		click(t, tt, "VSync")
		if !shows(tt, "VSync: Off") {
			t.Fatalf("VSync flyout did not open: %q", tt.Texts())
		}
		fly := bounds(t, tt, "VSync: Off")
		switch {
		case dock == DockLeft && fly.X < menu.X+menu.W:
			t.Errorf("docked left: flyout item at %+v, menu row %+v", fly, menu)
		case dock == DockRight && fly.X+fly.W > menu.X:
			t.Errorf("docked right: flyout item at %+v, menu row %+v", fly, menu)
		}
		tt.Key(0, mui.KeyEscape)
		tt.Frame()
		if shows(tt, "VSync: Off") || !a.ovl.menuOpen {
			t.Fatalf("first Escape: flyout shown %v, menu open %v; want the flyout closed alone", shows(tt, "VSync: Off"), a.ovl.menuOpen)
		}
		tt.Key(0, mui.KeyEscape)
		tt.Frame()
		if a.ovl.menuOpen {
			t.Fatal("second Escape left the menu open")
		}
	}
}

// TestMenuEscapeClosesFlyoutFirst: a flyout closes before its menu.
func TestMenuEscapeClosesFlyoutFirst(t *testing.T) {
	a, tt, _ := newStreamTester(t, streamFake())
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyO)
	tt.Frame()
	click(t, tt, "Codec")
	if !shows(tt, "Codec: PyroWave") {
		t.Fatalf("codec flyout did not open: %q", tt.Texts())
	}
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	if shows(tt, "Codec: PyroWave") || !a.ovl.menuOpen {
		t.Fatal("first Escape did not close the flyout alone")
	}
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	if a.ovl.menuOpen {
		t.Fatal("second Escape left the menu open")
	}
}

func openMenu(t *testing.T, tt *mui.Tester) {
	t.Helper()
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyO)
	tt.Frame()
	tt.Advance(popOutTime + 20*time.Millisecond)
	if !tt.HasText("Immersive mode") {
		t.Fatalf("menu did not open: %q", tt.Texts())
	}
}

// inForce reports whether the 2 px ember slice marks the choice named
// name, inside its bounds.
func inForce(t *testing.T, tt *mui.Tester, name string) bool {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("no %q: %q", name, tt.Texts())
	}
	r := info.Bounds
	img := tt.Image()
	for x := int(r.X); x < int(r.X+16); x++ {
		cr, cg, cb, _ := img.At(x, int(r.Y+r.H/2)).RGBA()
		if cr>>8 > 0xf0 && cg>>8 > 0x60 && cg>>8 < 0x90 && cb>>8 < 0x40 {
			return info.Checked
		}
	}
	return false
}

// TestMenuFlyoutPicksForThisHost: a flyout of four or more choices marks
// the one in force with the ember slice, opens away from the button with
// its first item level with its row, and its choice is this host's.
func TestMenuFlyoutPicksForThisHost(t *testing.T) {
	f := streamFake()
	f.overrides[""] = defaults.UserOverrides{Codec: "hevc"}
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	row := bounds(t, tt, "Codec")
	click(t, tt, "Codec")
	first := bounds(t, tt, "Codec: Auto")
	if first.X < row.X+row.W || first.Y != row.Y {
		t.Fatalf("flyout item %+v, want right of the menu and level with its row %+v", first, row)
	}
	if !inForce(t, tt, "Codec: Auto") || inForce(t, tt, "Codec: AV1") {
		t.Fatal("the slice does not mark Auto alone")
	}
	if !tt.HasText(rebuildNote) {
		t.Fatal("flyout footnote missing")
	}
	snapshot(t, tt, "menu-flyout")
	click(t, tt, "Codec: AV1")
	if got := f.override("h-bigboi"); got.Codec != "av1" {
		t.Fatalf("bigboi codec %q, want av1", got.Codec)
	}
	if got := f.override(""); got.Codec != "hevc" {
		t.Fatalf("the menu changed every host's codec: %q", got.Codec)
	}
}

// TestMenuColorFlyoutDisables444: the colour flyout holds three choices,
// with 4:4:4 unavailable, and why, when the host resolved it out.
func TestMenuColorFlyoutDisables444(t *testing.T) {
	f := streamFake()
	f.tel.Profile.EncoderAPI = "AMF"
	f.tel.Profile.Decisions = []defaults.Decision{{Knob: "chroma", Value: "420", Source: defaults.SourceAuto, Why: "4:4:4 not on both ends"}}
	a, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	click(t, tt, "Color")
	for _, name := range []string{"Color: 8-bit 4:2:0", "Color: 10-bit 4:2:0", "Color: 10-bit 4:4:4"} {
		if !shows(tt, name) {
			t.Fatalf("choice %q missing: %q", name, tt.Texts())
		}
	}
	if !inForce(t, tt, "Color: 10-bit 4:2:0") {
		t.Fatal("the resolved pair is not marked")
	}
	if !tt.HasText("4:4:4 needs an encoder and a decoder that both do it. This host is encoding on AMF, without 4:4:4 here.") {
		t.Fatalf("reason missing: %q", tt.Texts())
	}
	snapshot(t, tt, "menu-color-flyout")
	click(t, tt, "Color: 10-bit 4:4:4")
	if got := f.override("h-bigboi"); got.Chroma == "444" {
		t.Fatal("an unavailable choice took the press")
	}
	click(t, tt, "Color: 8-bit 4:2:0")
	if got := f.override("h-bigboi"); got.BitDepth != "8" || got.Chroma != "420" {
		t.Fatalf("color: %+v", got)
	}
	if !a.ovl.menuOpen || !shows(tt, "Color: 8-bit 4:2:0") {
		t.Fatal("picking a choice closed the flyout or the menu")
	}

	f.set(func(f *fakeBackend) { f.overrides["h-bigboi"] = defaults.UserOverrides{BitDepth: "8", Chroma: "444"} })
	tt.Frame()
	if !shows(tt, "Color: 8-bit 4:4:4") || !inForce(t, tt, "Color: 8-bit 4:4:4") {
		t.Fatalf("a pair outside the three is not a fourth choice in force: %q", tt.Texts())
	}
}

// TestMenuSwitchesStayOpen: switches and the sound panel change settings
// without closing the menu.
func TestMenuSwitchesStayOpen(t *testing.T) {
	f := streamFake()
	f.tel.MicMuted, f.tel.MicDevice = true, steamMic
	a, tt, _ := newStreamTester(t, f)
	win := &fakeWindow{}
	a.SetWindow(win)
	openMenu(t, tt)
	click(t, tt, "Fullscreen")
	click(t, tt, "Immersive mode")
	click(t, tt, "Shared clipboard")
	click(t, tt, "Sound & mic")
	click(t, tt, "Mute")
	click(t, tt, "Microphone")
	waitMuted(t, f, tt, false)
	s := f.Settings()
	if !win.full || !s.SystemKeysCapture || !s.ClipboardSend || !s.ClipboardReceive || !s.AudioMuted {
		t.Fatalf("full screen %v, settings %+v", win.full, s)
	}
	if !a.ovl.menuOpen {
		t.Fatal("a switch closed the menu")
	}
	if !tt.HasText("muted · mic on") {
		t.Fatalf("sound row value: %q", tt.Texts())
	}
	snapshot(t, tt, "menu-sound-panel")
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyF)
	tt.Frame()
	if win.full {
		t.Fatal("Ctrl+Alt+F did not leave full screen")
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyI)
	tt.Frame()
	if f.Settings().SystemKeysCapture {
		t.Fatal("Ctrl+Alt+I did not turn immersive mode off")
	}
	click(t, tt, "Bitrate")
	slider := bounds(t, tt, "Video bitrate")
	tt.ClickAt(slider.X+slider.W/2, slider.Y+slider.H/2)
	tt.Frame()
	if got := f.override("h-bigboi").BitrateMbps; got < 400 || got > 600 || got%5 != 0 {
		t.Fatalf("bitrate from the slider's middle: %d", got)
	}
	click(t, tt, "Bitrate: Auto")
	if got := f.override("h-bigboi").BitrateMbps; got != 0 {
		t.Fatalf("bitrate after Auto: %d", got)
	}
}

type fakeWindow struct{ full bool }

func (w *fakeWindow) Fullscreen() bool      { return w.full }
func (w *fakeWindow) SetFullscreen(on bool) { w.full = on }

func TestMenuDisconnect(t *testing.T) {
	f := streamFake()
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	click(t, tt, "Disconnect")
	if f.disconnects != 1 || !tt.HasText("Looking for hosts") {
		t.Fatalf("disconnects %d: %q", f.disconnects, tt.Texts())
	}
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	tt.Frame()
	tt.Key(mui.Ctrl|mui.Alt|mui.Shift, mui.KeyD)
	tt.Frame()
	if f.disconnects != 2 {
		t.Fatalf("Ctrl+Alt+Shift+D: disconnects %d", f.disconnects)
	}
}

// TestOverlayPressesIsolated: presses on the button, the menu, a flyout
// and the metrics window, body included, stay with the overlay; presses
// anywhere else reach the stream.
func TestOverlayPressesIsolated(t *testing.T) {
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, ButtonX: 0.5, ButtonY: 0.5, Metrics: true}
	a, tt, p := newStreamTester(t, f)
	tt.Frame()
	press := func(what string, x, y float32, want int) {
		t.Helper()
		tt.ClickAt(x, y)
		tt.Frame()
		if p.presses != want {
			t.Fatalf("press on %s at %v, %v: stream has %d presses, want %d", what, x, y, p.presses, want)
		}
	}
	press("the stream", 300, 800, 1)
	m := bounds(t, tt, "Metrics")
	press("the metrics body", m.X+m.W/2, m.Y+m.H-60, 1)
	press("the metrics figure", m.X+30, m.Y+120, 1)
	b := bounds(t, tt, "Stream controls")
	press("the button", b.X+b.W/2, b.Y+b.H/2, 1)
	if !a.ovl.menuOpen {
		t.Fatal("button press did not open the menu")
	}
	menu := bounds(t, tt, "Stream controls menu")
	press("the menu head", menu.X+menu.W/2, menu.Y+20, 1)
	row := bounds(t, tt, "Display")
	press("a flyout row", row.X+20, row.Y+row.H/2, 1)
	note, ok := tt.Find(rebuildNote)
	if !ok {
		t.Fatalf("flyout footnote missing: %q", tt.Texts())
	}
	press("the flyout's footnote", note.X+10, note.Y+note.H/2, 1)
	if !a.ovl.menuOpen {
		t.Fatal("a press inside the menu closed it")
	}
	if inside(menu, 150, 850) {
		t.Fatalf("menu %+v covers the point meant to be outside it", menu)
	}
	press("the stream beside the open menu", 150, 850, 2)
	if a.ovl.menuOpen {
		t.Fatal("a press on the stream left the menu open")
	}
}

// TestOverlayDragOffTheEdge: a popped-out button held and dragged away
// stays out however long the drag takes, and let go mid-view it floats
// there.
func TestOverlayDragOffTheEdge(t *testing.T) {
	a, tt, _ := dockedLeft(t, streamFake())
	hx, hy := centre(bounds(t, tt, "Show stream controls"))
	tt.Move(hx, hy)
	tt.Advance(dwellTime + popOutTime + 20*time.Millisecond)
	bx, by := centre(bounds(t, tt, "Stream controls"))
	tt.Press(bx, by)
	tt.Frame()
	for i := 1; i <= 30; i++ {
		tt.Move(bx+float32(i)*20, by)
		tt.Advance(60 * time.Millisecond)
	}
	if !shows(tt, "Stream controls") {
		t.Fatal("the button melted back into its dock while dragged")
	}
	tt.Advance(100 * time.Millisecond)
	tt.Release(bx+600, by)
	tt.Advance(time.Second)
	if a.ovl.dock != DockNone {
		t.Fatalf("let go mid-view: dock %v", a.ovl.dock)
	}
	if b := bounds(t, tt, "Stream controls"); b.X+b.W/2 != bx+600 {
		t.Fatalf("floats at %+v, want centred on x %v", b, bx+600)
	}
}

func TestOverlayReducedMotionPopsAtOnce(t *testing.T) {
	f := streamFake()
	f.layout = OverlayLayout{Placed: true, Dock: DockLeft, DockFrac: 0.5}
	a := NewApp(f)
	tt := mui.NewTesterWith(a.View, testWidth, testHeight, mui.TesterOptions{Preferences: mui.Preferences{ReduceMotion: true}})
	hx, hy := centre(bounds(t, tt, "Show stream controls"))
	tt.Move(hx, hy)
	tt.Advance(dwellTime + time.Millisecond)
	tt.Frame()
	if !shows(tt, "Stream controls") {
		t.Fatal("reduced motion: the button did not pop out at once")
	}
}

// TestMenuFlyoutsKeepMenuHeight: Color, Bitrate, VSync and Sound & mic
// open beside the menu, inside the window, and the menu keeps its height;
// the sound panel holds the mic's hint, wrapped in it.
func TestMenuFlyoutsKeepMenuHeight(t *testing.T) {
	for _, dock := range []DockEdge{DockLeft, DockRight} {
		f := streamFake()
		f.layout = OverlayLayout{Placed: true, Dock: dock, DockFrac: 0.8}
		f.tel.MicMuted, f.tel.MicHint = true, "Virtual mic not detected — install Steam Streaming Microphone / virtual audio device"
		_, tt, _ := newStreamTester(t, f)
		openMenu(t, tt)
		menu := bounds(t, tt, "Stream controls menu")
		for _, c := range []struct{ row, panel, inside string }{
			{"Color", "Color options", "Color: 8-bit 4:2:0"},
			{"Bitrate", "Bitrate options", "Video bitrate"},
			{"VSync", "VSync options", "VSync: Auto"},
			{"Sound & mic", "Sound & mic options", "Microphone"},
		} {
			click(t, tt, c.row)
			if got := bounds(t, tt, "Stream controls menu"); got.H != menu.H || got.Y != menu.Y {
				t.Errorf("docked %d, %s open: menu %+v, was %+v", dock, c.row, got, menu)
			}
			p := bounds(t, tt, c.panel)
			if p.X < 0 || p.Y < 0 || p.X+p.W > testWidth || p.Y+p.H > testHeight {
				t.Errorf("docked %d: %s panel %+v leaves the %dx%d window", dock, c.row, p, testWidth, testHeight)
			}
			if p.X < menu.X+menu.W && p.X+p.W > menu.X {
				t.Errorf("docked %d: %s panel %+v overlaps the menu %+v", dock, c.row, p, menu)
			}
			in := bounds(t, tt, c.inside)
			if in.X < p.X || in.X+in.W > p.X+p.W || in.Y < p.Y || in.Y+in.H > p.Y+p.H {
				t.Errorf("docked %d: %q %+v outside its panel %+v", dock, c.inside, in, p)
			}
		}
		hint := bounds(t, tt, f.tel.MicHint)
		sound := bounds(t, tt, "Sound & mic options")
		if hint.X+hint.W > sound.X+sound.W || hint.H < 20 {
			t.Errorf("docked %d: mic hint %+v not wrapped inside %+v", dock, hint, sound)
		}
		if dock == DockLeft {
			snapshot(t, tt, "menu-sound-flyout")
		}
	}
}

// TestCycleHUDShortcut: Ctrl+Alt+H steps the metrics HUD from off to the
// full window, to the compact bar, and off again, leaving the overlay
// button where it is; Ctrl+Alt+P alone hides the button.
func TestCycleHUDShortcut(t *testing.T) {
	_, tt, _ := dockedLeft(t, streamFake())
	mode := func() string {
		switch {
		case shows(tt, "Compact metrics"):
			return "full"
		case shows(tt, "Expand metrics"):
			return "compact"
		case shows(tt, "Metrics window bar"):
			return "shown"
		}
		return "off"
	}
	var got []string
	for range 4 {
		tt.Key(mui.Ctrl|mui.Alt, mui.KeyH)
		tt.Frame()
		got = append(got, mode())
		if !shows(tt, "Show stream controls") {
			t.Fatalf("Ctrl+Alt+H hid the overlay button (step %d)", len(got))
		}
	}
	if want := []string{"full", "compact", "off", "full"}; !slices.Equal(got, want) {
		t.Fatalf("HUD modes %v, want %v", got, want)
	}
}
