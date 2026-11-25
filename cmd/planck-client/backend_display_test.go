package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// TestBackendSwitchesHostDisplays: a stream from a two-display host lists
// both, says so once, and the Switch Display hotkey cycles the stream
// between them in place, the session never restarting.
func TestBackendSwitchesHostDisplays(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateHostConfig(dir, func(c *config.HostConfig) error {
		c.ControlPort, c.MediaPort, c.DiscoveryPort, c.FixedPIN = 0, 0, 0, "246802"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, t.TempDir(), nil)
	app := hostingFromAppConfig(t, dir)
	b.hostConfig = func(s ui.Settings) session.HostConfig {
		c := app(s)
		c.Capabilities = func() defaults.HostCapabilities {
			a := gpu.Adapter{LUID: 7, Vendor: gpu.VendorNVIDIA, Name: "Test GPU"}
			return defaults.HostCapabilities{OS: "windows",
				Encoders: []defaults.EncoderCap{{Adapter: a, API: defaults.APINVENC, Engines: 1, DrivesDisplay: true,
					Codecs: []defaults.CodecCap{{Codec: defaults.CodecHEVC, TenBit: true}}}},
				Displays: []defaults.HostDisplay{
					{ID: "1", Name: "Left", Width: 1920, Height: 1080, RefreshHz: 120, Adapter: a.LUID, Primary: true},
					{ID: "2", Name: "Right", Width: 2560, Height: 1440, RefreshHz: 144, Adapter: a.LUID, HDR: true},
				}}
		}
		return c
	}
	var heard atomic.Int64
	var heardStreamed atomic.Value
	b.onHostDisplays = func(host string, ds []defaults.HostDisplay, streamed string) {
		heard.Add(1)
		heardStreamed.Store(streamed)
	}
	ctx := context.Background()
	if err := b.SwitchDisplay(); !errors.Is(err, session.ErrNotConnected) {
		t.Fatalf("SwitchDisplay without a stream: %v", err)
	}
	if err := b.SetHosting(ctx, true); err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Hosting().Using.Control))
	if err := b.Connect(ctx, addr); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("first connect: %v", err)
	}
	if err := b.SubmitPIN(ctx, "246802"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stream", func() bool { return b.Telemetry().Connected })
	key := b.Telemetry().HostID
	waitUntil(t, "the host's displays", func() bool { return len(b.HostDisplays(key)) == 2 })
	ds := b.HostDisplays(key)
	if ds[0] != (ui.DisplayInfo{ID: "1", Name: "Left", Width: 1920, Height: 1080, RefreshHz: 120, Primary: true}) || !ds[1].HDR {
		t.Errorf("host displays %+v", ds)
	}
	waitUntil(t, "the displays announced", func() bool { return heard.Load() == 1 })
	if s := heardStreamed.Load(); s != "1" {
		t.Errorf("announced streaming %q, want the primary", s)
	}

	started := b.SessionStats().Started
	streamed := func() string { return streamedDisplay(b.Telemetry().Profile) }
	for _, want := range []string{"2", "1", "2"} {
		if err := b.SwitchDisplay(); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the stream on display "+want, func() bool { return streamed() == want })
	}
	if p := b.Telemetry().Profile; p.Width != 2560 || p.Height != 1440 {
		t.Errorf("display 2 streams %dx%d", p.Width, p.Height)
	}
	if s := b.SessionStats(); !s.Started.Equal(started) {
		t.Error("a display switch restarted the stream")
	}
	if err := b.SelectHostDisplay("9"); err == nil {
		t.Error("selected a display the host does not have")
	}
	if heard.Load() != 1 {
		t.Errorf("displays announced %d times in one stream", heard.Load())
	}
}

// TestKeepHostDisplay: a picked display is kept in the host's overrides,
// which its next stream starts from, beside the overrides it had.
func TestKeepHostDisplay(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	if err := b.SaveOverrides("h-bigboi", defaults.UserOverrides{Codec: "av1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.keepHostDisplay("h-bigboi", `\\.\DISPLAY2`); err != nil {
		t.Fatal(err)
	}
	if o := b.Overrides("h-bigboi"); o.Display != `\\.\DISPLAY2` || o.Codec != "av1" {
		t.Fatalf("overrides %+v", o)
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if o := stored.OverridesFor("h-bigboi"); o.Display != `\\.\DISPLAY2` {
		t.Fatalf("client.json overrides %+v", o)
	}
	if o := b.overridesFor("h-bigboi"); o.Display != `\\.\DISPLAY2` {
		t.Fatalf("a new stream's overrides %+v", o)
	}
}

func TestNextDisplay(t *testing.T) {
	ds := []defaults.HostDisplay{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for cur, want := range map[string]string{"a": "b", "c": "a", "": "a", "gone": "a"} {
		if got, ok := nextDisplay(ds, cur); !ok || got != want {
			t.Errorf("after %q: %q %v, want %q", cur, got, ok, want)
		}
	}
	if _, ok := nextDisplay(ds[:1], "a"); ok {
		t.Error("one display has a next")
	}
	if got, ok := nextDisplay(ds[:1], ""); !ok || got != "a" {
		t.Errorf("from a virtual display to the only one: %q %v", got, ok)
	}
	if _, ok := nextDisplay(nil, ""); ok {
		t.Error("no display has a next")
	}
}

// virtualDisplay stands in for a SudoVDA display.
type virtualDisplay struct{ closed atomic.Bool }

func (v *virtualDisplay) Name() string          { return `\\.\DISPLAY40` }
func (v *virtualDisplay) Resize(int, int) error { return nil }
func (v *virtualDisplay) Close() error          { v.closed.Store(true); return nil }

// TestBackendSwitchesFromVirtualToOnlyDisplay: a stream on a virtual
// display moves to the host's one physical display on the Switch Display
// hotkey, in place; there it has no other display to go to.
func TestBackendSwitchesFromVirtualToOnlyDisplay(t *testing.T) {
	dir := t.TempDir()
	err := config.UpdateHostConfig(dir, func(c *config.HostConfig) error {
		c.ControlPort, c.MediaPort, c.DiscoveryPort, c.FixedPIN = 0, 0, 0, "246802"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBackend(t, t.TempDir(), nil)
	app := hostingFromAppConfig(t, dir)
	vd := &virtualDisplay{}
	b.hostConfig = func(s ui.Settings) session.HostConfig {
		c := app(s)
		caps := c.Capabilities
		c.Capabilities = func() defaults.HostCapabilities {
			h := caps()
			h.VDD = defaults.VDDCap{Available: true, Protocol: "0.2.1", CanSetRenderAdapter: true}
			return h
		}
		c.OpenVirtualDisplay = func(session.VideoParams, []byte, bool) (session.VirtualDisplay, error) { return vd, nil }
		return c
	}
	ctx := context.Background()
	if err := b.SetHosting(ctx, true); err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(b.Hosting().Using.Control))
	if err := b.Connect(ctx, addr); !errors.Is(err, ui.ErrPairingRequired) {
		t.Fatalf("first connect: %v", err)
	}
	if err := b.SubmitPIN(ctx, "246802"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the virtual stream", func() bool { p := b.Telemetry().Profile; return p != nil && p.VirtualDisplay })
	key := b.Telemetry().HostID
	waitUntil(t, "the host's display", func() bool { return len(b.HostDisplays(key)) == 1 })
	started := b.SessionStats().Started
	if err := b.SwitchDisplay(); err != nil {
		t.Fatalf("Switch Display on a virtual display: %v", err)
	}
	waitUntil(t, "the stream on display 1", func() bool { return streamedDisplay(b.Telemetry().Profile) == "1" })
	waitUntil(t, "the virtual display removed", vd.closed.Load)
	if s := b.SessionStats(); !s.Started.Equal(started) {
		t.Error("the switch restarted the stream")
	}
	if err := b.SwitchDisplay(); !errors.Is(err, ui.ErrOneDisplay) {
		t.Errorf("Switch Display on the only display: %v, want ErrOneDisplay", err)
	}
}

// TestSettingsKeepDisplayWindows: Open all host displays and its window
// cap survive a restart; the default cap is not written.
func TestSettingsKeepDisplayWindows(t *testing.T) {
	dir := t.TempDir()
	b, _ := newTestBackend(t, dir, nil)
	s := b.Settings()
	if s.OpenEveryHostDisplay || s.MaxStreamWindows != config.DefaultStreamWindows {
		t.Fatalf("defaults %+v", s)
	}
	s.OpenEveryHostDisplay, s.MaxStreamWindows = true, 4
	if err := b.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	again, _ := newTestBackend(t, dir, nil)
	if s := again.Settings(); !s.OpenEveryHostDisplay || s.MaxStreamWindows != 4 {
		t.Fatalf("after a restart: %+v", s)
	}
	s.MaxStreamWindows = config.DefaultStreamWindows
	if err := again.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if stored, _ := config.LoadClientConfig(dir); stored.MaxStreamWindows != 0 {
		t.Errorf("the default cap stored as %d", stored.MaxStreamWindows)
	}
}
