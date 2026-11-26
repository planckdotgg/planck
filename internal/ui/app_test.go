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
	"github.com/planckdotgg/planck/pkg/defaults"
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
	if !tt.HasText("Looking for hosts") || !tt.HasText("Searching your network and tailnet") {
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
	for _, s := range []string{"bigboi", "studio-mac", "cabin-pc", "LAN", "0.2 ms", "Relay",
		"RTX 4080 NVENC · 1080p 60", "Not paired", "Tailscale connected", "watzon@",
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
	tt.Frame()
	if _, ok := tt.Access("Stream controls"); !ok {
		t.Fatalf("overlay button missing after connect: %q", tt.Texts())
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

// TestPairingRequiredOpensPanel: a host that needs pairing opens the PIN
// panel under Quick connect; Pair & connect waits for six digits and the
// fingerprint tick, then the panel collapses.
func TestPairingRequiredOpensPanel(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	a, tt := newTester(t, f)
	f.set(func(f *fakeBackend) {
		f.pair = PairState{Status: PairPending, HostName: "cabin-pc", Address: "100.64.0.9:47701",
			Fingerprint: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
	})
	f.connectResult <- ErrPairingRequired
	if err := tt.Click("Connect to cabin-pc"); err != nil {
		t.Fatal(err)
	}
	eventually(t, tt, func() bool { return tt.HasText("Enter the six digits on cabin-pc’s screen") })
	if a.screen != ScreenHosts {
		t.Fatalf("screen %v, want Hosts", a.screen)
	}
	for _, s := range []string{"9F86 D081 884C 7D65 9A2F EAA0 C55A D015", "Pairing", "Waiting for the code"} {
		if !tt.HasText(s) {
			t.Errorf("missing %q: %q", s, tt.Texts())
		}
	}
	if info, ok := tt.AccessByRole(mui.RoleButton, "Connect"); !ok || info.Disabled {
		t.Fatalf("Connect missing beside the panel: %+v %v", info, ok)
	}
	snapshot(t, tt, "pairing-pin")

	click(t, tt, "PIN")
	tt.Type("48a12-07x9")
	tt.Frame()
	if a.pin != "481207" {
		t.Fatalf("pin = %q", a.pin)
	}
	click(t, tt, "Pair & connect")
	tt.Frame()
	f.mu.Lock()
	n := len(f.pins)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("paired before the fingerprints were ticked as matching")
	}
	click(t, tt, "They match on both screens")
	click(t, tt, "Pair & connect")
	eventually(t, tt, func() bool { _, ok := tt.Access("PIN"); return !ok })
	f.mu.Lock()
	pins := slices.Clone(f.pins)
	f.mu.Unlock()
	if !slices.Equal(pins, []string{"481207"}) {
		t.Fatalf("pins = %q, want [481207]", pins)
	}
	if !tt.HasText("Pair & Connect") {
		t.Fatalf("cards not back after pairing: %q", tt.Texts())
	}
}

func TestPairShortPINNotSubmitted(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "cabin-pc"}
	_, tt := newTester(t, f)
	click(t, tt, "PIN")
	tt.Type("123")
	tt.Frame()
	if info, ok := tt.Access("Pair & connect"); !ok || !info.Disabled {
		t.Fatalf("Pair & connect not disabled with three digits: %+v %v", info, ok)
	}
	tt.Key(0, mui.KeyEnter)
	tt.Frame()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pins) != 0 {
		t.Fatalf("short PIN submitted: %q", f.pins)
	}
}

// TestPairWrongCodeClears: a refused code clears the cells and says why.
func TestPairWrongCodeClears(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "cabin-pc"}
	a, tt := newTester(t, f)
	click(t, tt, "PIN")
	tt.Type("111111")
	tt.Frame()
	f.set(func(f *fakeBackend) {
		f.pair.Status, f.pair.Error = PairFailed, "The host did not accept that PIN."
	})
	tt.Frame()
	if a.pin != "" || !tt.HasText("The host did not accept that PIN.") {
		t.Fatalf("pin %q after a refusal: %q", a.pin, tt.Texts())
	}
	click(t, tt, "Cancel")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 {
		t.Fatalf("cancels = %d", f.cancels)
	}
}

// TestTailnetTrust: a tailnet peer pairs on its own card, with no code and
// no fingerprint check.
func TestTailnetTrust(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	f.tailnet = TailnetStatus{Running: true, User: "watzon@github"}
	f.pair = PairState{Status: PairPending, HostName: "studio-mac", TailscaleSameUser: true, Fingerprint: "7b4d00000000c219"}
	_, tt := newTester(t, f)
	if _, ok := tt.Access("PIN"); ok || tt.HasText("Host fingerprint · SHA-256") {
		t.Fatal("same-user tailnet peer asked for a code or a fingerprint check")
	}
	for _, s := range []string{"your tailnet · no code", "watzon@github · fp 7B4D…C219"} {
		if !tt.HasText(s) {
			t.Errorf("card misses %q: %q", s, tt.Texts())
		}
	}
	snapshot(t, tt, "pairing-tailnet")
	click(t, tt, "Trust studio-mac")
	eventually(t, tt, func() bool { return !tt.HasText("your tailnet · no code") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.trusts != 1 {
		t.Fatalf("trusts = %d", f.trusts)
	}
}

// TestTailnetTrustWithoutCard: a tailnet peer reached by address, with no
// card, pairs from the panel.
func TestTailnetTrustWithoutCard(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "studio-mac", TailscaleSameUser: true}
	_, tt := newTester(t, f)
	click(t, tt, "Not now")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancels != 1 {
		t.Fatalf("cancels = %d", f.cancels)
	}
}

func TestRailHasNoPairing(t *testing.T) {
	f := newFake()
	_, tt := newTester(t, f)
	if _, ok := tt.AccessByRole(mui.RoleButton, "Pairing"); ok {
		t.Fatal("the rail still has a Pairing button")
	}
}

// TestRailNavigation: the rail's three pages, each button where the
// design puts its marker (button top + 16 = 112, 176, 240).
func TestRailNavigation(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	for i, name := range []string{"Hosts", "Statistics", "Settings"} {
		info, ok := tt.AccessByRole(mui.RoleButton, name)
		if !ok {
			t.Fatalf("no %s button", name)
		}
		if want := float32(112 + 64*i); info.Bounds.Y+16 != want {
			t.Errorf("%s marker at %v, want %v", name, info.Bounds.Y+16, want)
		}
	}
	click(t, tt, "Statistics")
	if a.screen != ScreenStatistics || !tt.HasText("No slices in flight") {
		t.Fatalf("statistics not shown: %q", tt.Texts())
	}
	click(t, tt, "Settings")
	if a.screen != ScreenSettings || !tt.HasText("Open streams in full screen") {
		t.Fatalf("settings not shown: %q", tt.Texts())
	}
	click(t, tt, "Hosts")
	if !tt.HasText("Looking for hosts") {
		t.Fatalf("hosts not shown: %q", tt.Texts())
	}
}

func TestSettingsSave(t *testing.T) {
	f := newFake()
	f.tailnet = TailnetStatus{Running: true, User: "watzon@"}
	f.encoders = []EncoderInfo{{GPU: "GeForce RTX 4080", API: "NVENC", EncodeMs: 1.34}, {GPU: "UHD Graphics 770", API: "oneVPL"}}
	a, tt := newTester(t, f)
	a.screen, a.set.cat = ScreenSettings, catHost
	tt.Frame()
	if !tt.HasText("1.3 ms") {
		t.Fatalf("encoder reading missing: %q", tt.Texts())
	}
	snapshot(t, tt, "settings")
	clickScrolled(t, tt, "GeForce RTX 4080")

	click(t, tt, "Network")
	if !tt.HasText("Relay bitrate cap") || !tt.HasText(fmt.Sprint(derpCapMbps())) {
		t.Fatalf("DERP cap missing: %q", tt.Texts())
	}
	click(t, tt, "Video & Display")
	click(t, tt, "AV1")
	click(t, tt, "Audio")
	clickScrolled(t, tt, "Stream audio")
	got := f.Settings()
	if ov := f.override(""); ov.Codec != "av1" || got.AudioEnabled || got.Encoder != "GeForce RTX 4080" {
		t.Fatalf("settings = %+v, overrides %+v", got, ov)
	}
	if ov := f.override(""); ov.Chroma != defaults.Auto || ov.BitrateMbps != 0 {
		t.Fatalf("untouched overrides changed: %+v", ov)
	}

	f.set(func(f *fakeBackend) { f.saveErr = errors.New("disk full") })
	click(t, tt, "Input & Hotkeys")
	clickScrolled(t, tt, "Send system keys to the host")
	if !tt.HasText("Could not save settings: disk full") {
		t.Fatalf("save error missing: %q", tt.Texts())
	}
}

func TestSettingsNoSaveWithoutChange(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	a.screen = ScreenSettings
	for cat := range numCategories {
		a.set.cat = cat
		tt.Frame()
		tt.Frame()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saved) != 0 || f.hostSaves != 0 || f.layoutSaves != 0 || f.ovSaves != 0 {
		t.Fatalf("saved without a change: settings %d, host %d, layout %d, overrides %d times",
			len(f.saved), f.hostSaves, f.layoutSaves, f.ovSaves)
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
	// Start from a still grid: the empty state animates, so it always has
	// a frame pending.
	f.hosts = sampleHosts()[1:2]
	a, tt := newTester(t, f)
	if tt.FramePending() {
		t.Fatal("a still host grid asks for frames")
	}
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

// TestPairedButStreamFailed: the PIN paired but the stream did not start;
// the app goes back to Hosts with the error instead of asking for a PIN
// the host has rotated.
func TestPairedButStreamFailed(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPending, HostName: "bigboi"}
	f.pinErr = errors.New("session: video sink: no window")
	a, tt := newTester(t, f)
	click(t, tt, "PIN")
	tt.Type("623543")
	tt.Frame()
	click(t, tt, "Pair & connect")
	eventually(t, tt, func() bool { return tt.HasText("Could not connect: session: video sink: no window") })
	if a.screen != ScreenHosts || a.pin != "" {
		t.Fatalf("screen %v, pin %q after pairing then failing to stream", a.screen, a.pin)
	}
}

// TestStreamEndShowsHosts: a stream started from another page ends on
// Hosts.
func TestStreamEndShowsHosts(t *testing.T) {
	f := newFake()
	f.pair = PairState{Status: PairPaired, HostName: "bigboi"}
	a, tt := newTester(t, f)
	a.screen = ScreenStatistics
	f.set(func(f *fakeBackend) { f.tel = sampleTelemetry() })
	tt.Frame()
	f.set(func(f *fakeBackend) { f.tel = Telemetry{} })
	tt.Frame()
	if a.screen != ScreenHosts {
		t.Fatalf("screen %v after the stream ended, want Hosts", a.screen)
	}
}

// TestPairedHostNotFoundConnects: a paired host typed in by address, which
// discovery does not find, still connects from the list.
func TestPairedHostNotFoundConnects(t *testing.T) {
	f := newFake()
	f.hosts = []HostInfo{{ID: "addr:192.168.188.126:41001", Name: "192.168.188.126:41001", Paired: true,
		Address: "192.168.188.126:41001"}}
	_, tt := newTester(t, f)
	if tt.HasText("Looking for hosts") {
		t.Fatalf("paired host not listed: %q", tt.Texts())
	}
	if err := tt.Click("Connect to 192.168.188.126:41001"); err != nil {
		t.Fatalf("%v (texts: %q)", err, tt.Texts())
	}
	tt.Frame()
	eventually(t, tt, func() bool { return tt.HasText("Connecting to 192.168.188.126:41001") })
	f.mu.Lock()
	got := slices.Clone(f.connects)
	f.mu.Unlock()
	if !slices.Equal(got, []string{"addr:192.168.188.126:41001"}) {
		t.Fatalf("connects = %q", got)
	}
	f.connectResult <- errors.New("unreachable")
}
