package ui

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/encode"
)

// snapshot writes the tester's frame as a PNG when PLANCK_UI_SNAPSHOT_DIR
// is set, for looking at the screens without a window.
func snapshot(t *testing.T, tt *mui.Tester, name string) {
	dir := os.Getenv("PLANCK_UI_SNAPSHOT_DIR")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestHostsEmptyState(t *testing.T) {
	f := newFake()
	_, tt := newTester(t, f)
	if !tt.HasText("No hosts found yet") || !tt.HasText("Searching your network and tailnet") {
		t.Fatalf("empty state missing: %q", tt.Texts())
	}
	if !tt.HasText("Tailscale is not running. LAN hosts still show here.") {
		t.Fatalf("tailnet footer missing: %q", tt.Texts())
	}
	snapshot(t, tt, "hosts-empty")
	click(t, tt, "Refresh")
	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", f.refreshes)
	}
}

func TestHostsListAndConnect(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	f.tailnet = TailnetStatus{Running: true, User: "watzon@"}
	a, tt := newTester(t, f)
	for _, s := range []string{"bigboi", "studio-mac", "cabin-pc", "LAN direct", "0.2 ms", "DERP relay",
		"RTX 4080 NVENC", "1920 x 1080", "60 Hz", "not paired", "Tailscale connected as watzon@",
		"3 online on your network and tailnet"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q", s)
		}
	}
	snapshot(t, tt, "hosts")

	if err := tt.Click("Connect to bigboi"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	eventually(t, tt, func() bool { return tt.HasText("Connecting to bigboi") })
	f.mu.Lock()
	got := slices.Clone(f.connects)
	f.mu.Unlock()
	if !slices.Equal(got, []string{"h-bigboi"}) {
		t.Fatalf("connects = %q", got)
	}
	snapshot(t, tt, "connecting")

	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	f.connectResult <- nil
	eventually(t, tt, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.connecting == ""
	})
	if !tt.HasText("8.4") || !tt.HasText("LAN direct") {
		t.Fatalf("HUD missing after connect: %q", tt.Texts())
	}
}

func TestConnectCancel(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	_, tt := newTester(t, f)
	if err := tt.Click("Connect to studio-mac"); err != nil {
		t.Fatal(err)
	}
	eventually(t, tt, func() bool { return tt.HasText("Connecting to studio-mac") })
	click(t, tt, "Cancel")
	eventually(t, tt, func() bool { return tt.HasText("Hosts") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if !errors.Is(f.connectCtxErr, context.Canceled) {
		t.Fatalf("connect context error = %v, want canceled", f.connectCtxErr)
	}
	if tt.HasText("Could not connect: context canceled") {
		t.Fatal("a canceled connect shows as an error")
	}
}

func TestConnectErrorShown(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	_, tt := newTester(t, f)
	f.connectResult <- errors.New("host refused the session")
	if err := tt.Click("Connect to bigboi"); err != nil {
		t.Fatal(err)
	}
	eventually(t, tt, func() bool { return tt.HasText("Could not connect: host refused the session") })
}

func TestQuickConnect(t *testing.T) {
	f := newFake()
	_, tt := newTester(t, f)
	click(t, tt, "Quick connect")
	tt.Type(" 10.0.0.7 ")
	tt.Frame()
	click(t, tt, "Connect")
	eventually(t, tt, func() bool { return tt.HasText("Connecting to 10.0.0.7") })
	f.mu.Lock()
	got := slices.Clone(f.connects)
	f.mu.Unlock()
	if !slices.Equal(got, []string{"10.0.0.7"}) {
		t.Fatalf("connects = %q", got)
	}
	f.connectResult <- errors.New("unreachable")
}

func TestPairingRequiredOpensPairing(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	_, tt := newTester(t, f)
	f.set(func(f *fakeBackend) {
		f.pair = PairState{Status: PairPending, HostName: "cabin-pc",
			Fingerprint: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
	})
	f.connectResult <- ErrPairingRequired
	if err := tt.Click("Connect to cabin-pc"); err != nil {
		t.Fatal(err)
	}
	eventually(t, tt, func() bool { return tt.HasText("With a PIN") })
	if !tt.HasText("9F86 D081 884C 7D65 9A2F EAA0 C55A D015") {
		t.Fatalf("fingerprint missing: %q", tt.Texts())
	}
	snapshot(t, tt, "pairing-pin")

	click(t, tt, "PIN")
	tt.Type("48a12-07x9")
	tt.Frame()
	click(t, tt, "Pair")
	eventually(t, tt, func() bool { return tt.HasText("Paired with cabin-pc") })
	f.mu.Lock()
	pins := slices.Clone(f.pins)
	f.mu.Unlock()
	if !slices.Equal(pins, []string{"481207"}) {
		t.Fatalf("pins = %q, want [481207]", pins)
	}
	click(t, tt, "Back to hosts")
	if !tt.HasText("Also available") {
		t.Fatalf("not back on hosts: %q", tt.Texts())
	}
}

func TestPairShortPINNotSubmitted(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "cabin-pc"}
	a, tt := newTester(t, f)
	a.screen = ScreenPairing
	tt.Frame()
	click(t, tt, "PIN")
	tt.Type("123")
	tt.Frame()
	click(t, tt, "Pair")
	if !tt.HasText("Enter all six digits") {
		t.Fatalf("hint missing: %q", tt.Texts())
	}
	tt.Frame()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pins) != 0 {
		t.Fatalf("short PIN submitted: %q", f.pins)
	}
}

func TestTailnetTrust(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "studio-mac", TailscaleSameUser: true}
	a, tt := newTester(t, f)
	a.screen = ScreenPairing
	tt.Frame()
	if tt.HasText("Host fingerprint, SHA-256") {
		t.Fatal("same-user tailnet peer asked for a fingerprint check")
	}
	snapshot(t, tt, "pairing-tailnet")
	click(t, tt, "Trust studio-mac")
	eventually(t, tt, func() bool { return tt.HasText("Paired with studio-mac") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trusts != 1 {
		t.Fatalf("trusts = %d", f.trusts)
	}
}

func TestPairingIdle(t *testing.T) {
	f := newFake()
	_, tt := newTester(t, f)
	click(t, tt, "Pairing")
	if !tt.HasText("No pairing request") {
		t.Fatalf("idle pairing missing: %q", tt.Texts())
	}
}

func TestRailNavigation(t *testing.T) {
	f := newFake()
	_, tt := newTester(t, f)
	click(t, tt, "Settings")
	if !tt.HasText("Maximum bitrate") || !tt.HasText("Encoder and GPU") {
		t.Fatalf("settings not shown: %q", tt.Texts())
	}
	click(t, tt, "Hosts")
	if !tt.HasText("No hosts found yet") {
		t.Fatalf("hosts not shown: %q", tt.Texts())
	}
}

func TestSettingsSave(t *testing.T) {
	f := newFake()
	f.tailnet = TailnetStatus{Running: true, User: "watzon@"}
	f.encoders = []EncoderInfo{{GPU: "GeForce RTX 4080", API: "NVENC", EncodeMs: 1.34}, {GPU: "UHD Graphics 770", API: "oneVPL"}}
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	tt.Frame()
	if !tt.HasText("1.3 ms") || !tt.HasText("Relay bitrate cap") || !tt.HasText(fmt.Sprint(derpCapMbps())) {
		t.Fatalf("encoder reading or DERP cap missing: %q", tt.Texts())
	}
	snapshot(t, tt, "settings")

	click(t, tt, "AV1")
	click(t, tt, "Stream audio")
	click(t, tt, "GeForce RTX 4080")
	got := f.Settings()
	if got.Codec != encode.AV1 || got.AudioEnabled || got.Encoder != "GeForce RTX 4080" {
		t.Fatalf("settings = %+v", got)
	}
	if got.Chroma != encode.P010 || got.MaxBitrateMbps != 100 {
		t.Fatalf("untouched settings changed: %+v", got)
	}

	f.set(func(f *fakeBackend) { f.saveErr = errors.New("disk full") })
	click(t, tt, "Send system keys to the host")
	if !tt.HasText("Could not save settings: disk full") {
		t.Fatalf("save error missing: %q", tt.Texts())
	}
}

func TestSettingsNoSaveWithoutChange(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	for range 3 {
		tt.Frame()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saved) != 0 {
		t.Fatalf("saved without a change: %d times", len(f.saved))
	}
}

func TestHUDDrawerActions(t *testing.T) {
	f := newFake()
	f.tel = sampleTelemetry()
	a, tt := newTester(t, f)
	if _, ok := tt.Access(hudSummary(f.tel)); !ok {
		t.Fatalf("HUD pill label missing")
	}
	snapshot(t, tt, "hud-pill")
	click(t, tt, "Show stream controls")
	if !tt.HasText("Glass to glass, by stage") || !tt.HasText("1920 x 1080, 240 FPS, HEVC 10-bit") {
		t.Fatalf("drawer missing: %q", tt.Texts())
	}
	for _, s := range []string{"encode", "1.5 ms", "network", "0.7 ms", "decode", "1.0 ms", "present", "2.5 ms"} {
		if !tt.HasText(s) {
			t.Errorf("stage reading %q missing", s)
		}
	}
	snapshot(t, tt, "hud-drawer")

	click(t, tt, "Force keyframe")
	click(t, tt, "4:4:4 10-bit")
	click(t, tt, "150 Mbps")
	click(t, tt, "System keys")
	got := f.Settings()
	if got.Chroma != encode.YUV444P10 || got.MaxBitrateMbps != 150 || !got.SystemKeysCapture {
		t.Fatalf("drawer settings = %+v", got)
	}
	if f.keyframes != 1 {
		t.Fatalf("keyframes = %d", f.keyframes)
	}
	click(t, tt, "Disconnect")
	if f.disconnects != 1 {
		t.Fatalf("disconnects = %d", f.disconnects)
	}
	if a.hudMode() != HUDPill {
		t.Fatalf("HUD mode after disconnect = %v", a.hudMode())
	}
	if !tt.HasText("No hosts found yet") {
		t.Fatalf("not back on hosts: %q", tt.Texts())
	}
}

func TestCycleHUD(t *testing.T) {
	f := newFake()
	f.tel = sampleTelemetry()
	a, tt := newTester(t, f)
	want := []HUDMode{HUDDrawer, HUDHidden, HUDPill}
	for _, m := range want {
		tt.Key(mui.Ctrl|mui.Alt, mui.KeyH)
		tt.Frame()
		if a.hudMode() != m {
			t.Fatalf("after Ctrl+Alt+H mode = %v, want %v", a.hudMode(), m)
		}
	}
	a.CycleHUD()
	a.CycleHUD()
	tt.Frame()
	if tt.HasText("8.4") {
		t.Fatal("hidden HUD still shows")
	}
}

func TestHostingToggle(t *testing.T) {
	f := newFake()
	f.hosting = HostingState{Encoder: "M5 Pro VideoToolbox"}
	a, tt := newTester(t, f)
	name := "Host this " + machineWord(a.goos)
	click(t, tt, name)
	eventually(t, tt, func() bool { return f.Hosting().On })
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.hostingCalls, []bool{true}) {
		t.Fatalf("hosting calls = %v", f.hostingCalls)
	}
}

func TestBackendNotifyAfterFirstFrame(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	f.mu.Lock()
	notify := f.onChange
	f.mu.Unlock()
	if notify == nil {
		t.Fatal("NewApp did not subscribe")
	}
	f.set(func(f *fakeBackend) { f.hosts = sampleHosts()[:1] })
	notify()
	if !tt.FramePending() {
		t.Fatal("backend change did not ask for a frame")
	}
	tt.Frame()
	if !tt.HasText("bigboi") {
		t.Fatalf("new host not drawn: %q", tt.Texts())
	}
	_ = a
}
