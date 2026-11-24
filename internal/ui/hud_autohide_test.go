package ui

import (
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

// streamProbe stands in for the client's input surface: it counts the
// presses that reach the stream under the HUD.
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
// stream view.
func newStreamTester(t *testing.T) (*App, *mui.Tester, *streamProbe, *fakeBackend) {
	t.Helper()
	f := newFake()
	f.tel = sampleTelemetry()
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	a := NewApp(f)
	p := &streamProbe{}
	a.SetStreamView(p.view)
	return a, mui.NewTester(a.View, 1440, 900), p, f
}

func hudVisible(tt *mui.Tester, f *fakeBackend) bool {
	_, ok := tt.Access(hudSummary(f.Telemetry()))
	return ok
}

func TestHUDRevealOnConnectThenHides(t *testing.T) {
	_, tt, _, f := newStreamTester(t)
	if !hudVisible(tt, f) {
		t.Fatal("HUD not shown on connect")
	}
	tt.Advance(hudReveal - 100*time.Millisecond)
	if !hudVisible(tt, f) {
		t.Fatal("HUD hid before the reveal ended")
	}
	tt.Advance(200 * time.Millisecond)
	if hudVisible(tt, f) || tt.HasText("8.4") {
		t.Fatal("HUD still shown after the connect reveal")
	}
}

func TestHUDTopEdgeHoverRevealsAndLeaveHides(t *testing.T) {
	_, tt, _, f := newStreamTester(t)
	tt.Advance(hudReveal + time.Second)
	if hudVisible(tt, f) {
		t.Fatal("HUD shown before hovering")
	}
	tt.Move(300, 5)
	tt.Frame()
	if !hudVisible(tt, f) {
		t.Fatal("top edge did not reveal the HUD")
	}
	pill, ok := tt.Find("8.4")
	if !ok {
		t.Fatal("pill reading not found")
	}
	tt.Move(pill.X+pill.W/2, pill.Y+pill.H/2)
	tt.Frame()
	tt.Advance(5 * time.Second)
	if !hudVisible(tt, f) {
		t.Fatal("HUD hid under the pointer")
	}

	tt.Move(700, 600)
	tt.Frame()
	tt.Advance(hudLinger - 100*time.Millisecond)
	if !hudVisible(tt, f) {
		t.Fatal("HUD hid before lingering")
	}
	tt.Advance(200 * time.Millisecond)
	if hudVisible(tt, f) {
		t.Fatal("HUD still shown after the pointer left")
	}
}

func TestHUDDrawerKeepsVisible(t *testing.T) {
	a, tt, _, f := newStreamTester(t)
	click(t, tt, "Show stream controls")
	tt.Move(700, 850)
	tt.Frame()
	tt.Advance(10 * time.Second)
	if !hudVisible(tt, f) || !tt.HasText("Glass to glass, by stage") {
		t.Fatal("open drawer hid")
	}
	click(t, tt, "Hide stream controls")
	if a.hudMode() != HUDPill {
		t.Fatalf("mode = %v after closing the drawer", a.hudMode())
	}
	tt.Move(700, 850)
	tt.Frame()
	tt.Advance(hudReveal + 100*time.Millisecond)
	if hudVisible(tt, f) {
		t.Fatal("HUD still shown after the drawer closed and the pointer left")
	}
}

func TestHUDPinKeepsVisible(t *testing.T) {
	a, tt, _, f := newStreamTester(t)
	if a.hudPinned {
		t.Fatal("HUD pinned by default")
	}
	click(t, tt, "Show stream controls")
	click(t, tt, "Pin HUD")
	if !a.hudPinned {
		t.Fatal("Pin HUD did not pin")
	}
	click(t, tt, "Hide stream controls")
	tt.Move(700, 850)
	tt.Frame()
	tt.Advance(30 * time.Second)
	if !hudVisible(tt, f) {
		t.Fatal("pinned HUD hid")
	}
}

func TestHUDHotkeyRevealsHiddenPill(t *testing.T) {
	a, tt, _, f := newStreamTester(t)
	tt.Advance(hudReveal + time.Second)
	// The first press shows the hidden pill, the next three go round.
	for range 4 {
		tt.Key(mui.Ctrl|mui.Alt, mui.KeyH)
		tt.Frame()
	}
	if a.hudMode() != HUDPill || !hudVisible(tt, f) {
		t.Fatalf("cycling back to the pill did not show it (mode %v)", a.hudMode())
	}
	tt.Advance(hudReveal + 100*time.Millisecond)
	if hudVisible(tt, f) {
		t.Fatal("pill from the hotkey did not auto-hide")
	}
	a.CycleHUD()
	a.CycleHUD()
	a.CycleHUD()
	tt.Frame()
	tt.Move(300, 5)
	tt.Frame()
	if hudVisible(tt, f) {
		t.Fatal("top edge revealed a HUD turned off")
	}
}

// TestHUDHiddenPassesPresses: with the pill hidden, presses at the top
// edge and where the pill shows reach the stream; with it shown, presses
// beside it do too.
func TestHUDHiddenPassesPresses(t *testing.T) {
	_, tt, p, f := newStreamTester(t)
	pill, ok := tt.Find("8.4")
	if !ok {
		t.Fatal("pill reading not found")
	}
	tt.Advance(hudReveal + time.Second)
	if hudVisible(tt, f) {
		t.Fatal("HUD still shown")
	}
	tt.ClickAt(pill.X+pill.W/2, pill.Y+pill.H/2)
	tt.Frame()
	if p.presses != 1 {
		t.Fatalf("press where the hidden pill sits: stream got %d presses, want 1", p.presses)
	}
	tt.ClickAt(720, 3)
	tt.Frame()
	if p.presses != 2 {
		t.Fatalf("press in the top edge: stream got %d presses, want 2", p.presses)
	}
	if !hudVisible(tt, f) {
		t.Fatal("top edge press did not reveal the HUD")
	}
	tt.ClickAt(20, 40)
	tt.Frame()
	if p.presses != 3 {
		t.Fatalf("press beside the shown pill: stream got %d presses, want 3", p.presses)
	}
}

// TestHUDHotkeyFromAutoHiddenPill: the hotkey first shows a pill that hid
// itself, and only the next press opens the drawer.
func TestHUDHotkeyFromAutoHiddenPill(t *testing.T) {
	a, tt, _, f := newStreamTester(t)
	tt.Advance(hudReveal + time.Second)
	if hudVisible(tt, f) {
		t.Fatal("pill did not hide itself")
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyH)
	tt.Frame()
	if a.hudMode() != HUDPill || !hudVisible(tt, f) || tt.HasText("Glass to glass, by stage") {
		t.Fatalf("first press: mode %v, want the pill shown alone", a.hudMode())
	}
	tt.Key(mui.Ctrl|mui.Alt, mui.KeyH)
	tt.Frame()
	if a.hudMode() != HUDDrawer || !tt.HasText("Glass to glass, by stage") {
		t.Fatalf("second press: mode %v, want the drawer", a.hudMode())
	}

	a.CycleHUD()
	tt.Frame()
	tt.Advance(hudReveal + time.Second)
	a.CycleHUD()
	a.CycleHUD()
	tt.Frame()
	if a.hudMode() != HUDDrawer {
		t.Fatalf("two presses before a frame from the hidden HUD: mode %v, want the drawer", a.hudMode())
	}
}
