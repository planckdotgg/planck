package ui

import (
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/version"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
)

func settingsAt(t *testing.T, f *fakeBackend, cat settingsCategory) (*App, *mui.Tester) {
	t.Helper()
	a, tt := newTester(t, f)
	a.screen, a.set.cat = ScreenSettings, cat
	tt.Frame()
	return a, tt
}

// navCount is the result count a category row shows while searching, -1
// when its row is disabled.
func navCount(t *testing.T, tt *mui.Tester, cat settingsCategory) bool {
	t.Helper()
	info, ok := tt.AccessByRole(mui.RoleButton, categoryNames[cat])
	if !ok {
		t.Fatalf("no %s row", categoryNames[cat])
	}
	return !info.Disabled
}

// TestSettingsSearch: the field filters every category by title, help
// and alias; categories without a hit go dim and cannot be picked; Esc
// clears it, and Enter opens the first category with a hit.
func TestSettingsSearch(t *testing.T) {
	f := newFake()
	a, tt := settingsAt(t, f, catGeneral)
	click(t, tt, "Search settings")
	tt.Type("4:4:4")
	tt.Frame()
	if !tt.HasText("Chroma") || !tt.HasText("Host policy") && !tt.HasText("Allowed colour") {
		t.Fatalf("4:4:4 does not find Chroma and the host policy: %q", tt.Texts())
	}
	if tt.HasText("Open streams in full screen") || tt.HasText("Stream audio") {
		t.Fatalf("rows that do not match show: %q", tt.Texts())
	}
	for cat, want := range map[settingsCategory]bool{catHost: true, catVideo: true, catGeneral: false, catAudio: false, catNetwork: false} {
		if got := navCount(t, tt, cat); got != want {
			t.Errorf("%s enabled = %v, want %v", categoryNames[cat], got, want)
		}
	}
	snapshot(t, tt, "settings-search")

	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	if a.set.query != "" || !tt.HasText("Open streams in full screen") {
		t.Fatalf("Esc did not restore General: query %q, %q", a.set.query, tt.Texts())
	}

	click(t, tt, "Search settings")
	tt.Type("mic")
	tt.Frame()
	// The title shows the match apart: "Mic" in full text, "rophone" not.
	if !tt.HasText("Mic") || !tt.HasText("rophone") || !navCount(t, tt, catAudio) {
		t.Fatalf("mic does not find the microphone: %q", tt.Texts())
	}
	tt.Key(0, mui.KeyEnter)
	tt.Frame()
	if a.set.cat != catAudio || a.set.query != "" {
		t.Fatalf("Enter opened %v with query %q, want Audio", a.set.cat, a.set.query)
	}

	click(t, tt, "Search settings")
	tt.Type("port")
	tt.Frame()
	if !tt.HasText("Ports") || navCount(t, tt, catVideo) {
		t.Fatalf("port: %q", tt.Texts())
	}
}

// TestSettingsHostControls: Settings → Host writes host.json's switches,
// policy, pairing code, clipboard and ports, and its Hosting switch is
// the rail's.
func TestSettingsHostControls(t *testing.T) {
	f := newFake()
	f.encoders = []EncoderInfo{{GPU: "Apple M5 Pro", API: "VideoToolbox", EncodeMs: 5.01}}
	f.hostSettings = HostSettings{Ports: HostPorts{Control: 47701, Media: 47702, Discovery: 47700}}
	a, tt := settingsAt(t, f, catHost)

	click(t, tt, "Require a code on the LAN")
	clickScrolled(t, tt, "Trust my own Tailscale devices with no code")
	clickScrolled(t, tt, "Let clients control this machine")
	clickScrolled(t, tt, "Clipboard when hosting: Both ways")
	clickScrolled(t, tt, "Allow 4:4:4")
	hs := f.HostSettings()
	if !hs.RequirePINForLAN || !hs.AllowTailscaleSameUser || !hs.AllowInput || hs.Clipboard != ClipboardBoth || hs.Policy.Chroma != "420" {
		t.Fatalf("host settings = %+v", hs)
	}
	if hs.Policy.BitDepth != defaults.Auto || hs.Policy.Codec != defaults.Auto {
		t.Fatalf("untouched policy changed: %+v", hs.Policy)
	}

	typeInto(t, tt, "Policy bitrate", "150")
	if got := f.HostSettings().Policy.BitrateMbps; got != 150 {
		t.Fatalf("policy bitrate = %d", got)
	}

	click(t, tt, "Pairing code: Fixed")
	typeInto(t, tt, "Fixed code", "12345")
	if f.HostSettings().FixedPIN != "" || !tt.HasText("Could not save settings: a fixed code is six digits") {
		t.Fatalf("a five-digit code was kept: %+v %q", f.HostSettings(), tt.Texts())
	}
	typeInto(t, tt, "Fixed code", "418207")
	if got := f.HostSettings().FixedPIN; got != "418207" {
		t.Fatalf("fixed code = %q", got)
	}

	f.set(func(f *fakeBackend) { f.ports = PortStatus{Media: "Another app is using UDP port 47702."} })
	clickScrolled(t, tt, "Ports")
	if !tt.HasText("Media port: Another app is using UDP port 47702.") {
		t.Fatalf("port warning missing: %q", tt.Texts())
	}
	typeInto(t, tt, "Control port", "41000")
	if got := f.HostSettings().Ports; got.Control != 41000 || got.Media != 47702 {
		t.Fatalf("ports = %+v", got)
	}

	click(t, tt, "Measure again")
	eventually(t, tt, func() bool { return !a.measuring() })
	f.mu.Lock()
	measures := slices.Clone(f.measures)
	f.mu.Unlock()
	if !slices.Equal(measures, []string{"Apple M5 Pro"}) {
		t.Fatalf("measures = %q", measures)
	}

	click(t, tt, "Hosting")
	eventually(t, tt, func() bool { return f.Hosting().On })
	if info, ok := tt.Access("Host this " + machineWord(a.goos)); !ok || !info.Checked {
		t.Fatalf("the rail's hosting button does not show hosting: %+v", info)
	}
	snapshot(t, tt, "settings-host")
}

// TestSettingsRevoke: Revoke asks first, in its row; Revoke all asks too.
func TestSettingsRevoke(t *testing.T) {
	f := newFake()
	f.paired = []PairedClient{
		{Name: "iPad Pro", Fingerprint: "3c9a000000000000000000000000000000000000000000000000000000022f1"},
		{Name: "cabin-pc", Fingerprint: "7b4d00000000000000000000000000000000000000000000000000000000c219", TrustedViaTailnet: true},
	}
	_, tt := settingsAt(t, f, catHost)
	if !tt.HasText("3C9A…22F1") {
		t.Fatalf("fingerprint missing: %q", tt.Texts())
	}
	if _, ok := tt.Access("trusted through your tailnet"); !ok {
		t.Fatal("tailnet-trusted device has no shield")
	}
	clickScrolled(t, tt, "Revoke iPad Pro")
	f.mu.Lock()
	n := len(f.revokes)
	f.mu.Unlock()
	if n != 0 {
		t.Fatal("revoked without asking")
	}
	click(t, tt, "Keep iPad Pro")
	clickScrolled(t, tt, "Revoke iPad Pro")
	click(t, tt, "Confirm revoke iPad Pro")
	clickScrolled(t, tt, "Revoke all")
	click(t, tt, "Confirm revoke all")
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.revokes, []string{f.revokes[0], "*"}) || f.revokes[0][:4] != "3c9a" {
		t.Fatalf("revokes = %q", f.revokes)
	}
}

// TestSettingsVideoSetByHost: a knob the streaming host's policy sets
// shows its value as Set by host in that host's scope, with no control;
// All hosts keeps the controls.
func TestSettingsVideoSetByHost(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	f.overrides["h-bigboi"] = defaults.UserOverrides{FPS: "240"}
	f.stats = SessionStats{Live: true, HostName: "bigboi", HostID: "h-bigboi", PathKind: discovery.PathLANDirect,
		HostPolicy: defaults.UserOverrides{BitrateMbps: 150, Codec: "hevc", Chroma: "420"}}
	a, tt := settingsAt(t, f, catVideo)
	if tt.HasText("Set by host") {
		t.Fatal("All hosts shows a host's policy")
	}
	if _, ok := tt.Access("Bitrate: Manual"); !ok {
		t.Fatal("All hosts has no bitrate control")
	}
	click(t, tt, "Stream format for: bigboi")
	if !tt.HasText("Set by host") {
		t.Fatalf("no Set by host: %q", tt.Texts())
	}
	for _, name := range []string{"Bitrate: 150 Mbps, set by host", "Codec: HEVC, set by host", "Chroma: 4:2:0, set by host"} {
		if _, ok := tt.Access(name); !ok {
			t.Errorf("no %q", name)
		}
	}
	for _, name := range []string{"Bitrate: Manual", "Codec: AV1", "Chroma: 4:4:4"} {
		if _, ok := tt.Access(name); ok {
			t.Errorf("%q still offered under the host's policy", name)
		}
	}
	if !tt.HasText("overridden for bigboi") {
		t.Fatalf("panel with bigboi's own frame rate has no tag: %q", tt.Texts())
	}
	snapshot(t, tt, "settings-video-locked")
	_ = a
}

// TestSettingsGeneral: the device name, the metrics default and the
// clipboard switches save where they belong.
func TestSettingsGeneral(t *testing.T) {
	f := newFake()
	_, tt := settingsAt(t, f, catGeneral)
	typeInto(t, tt, "Device name", "studio-mac")
	click(t, tt, "Metrics at stream start: Compact")
	click(t, tt, "Share this device’s clipboard")
	if got := f.ClientName(); got != "studio-mac" {
		t.Fatalf("client name = %q", got)
	}
	if l := f.OverlayLayout(); !l.Metrics || !l.MetricsCompact {
		t.Fatalf("layout = %+v", l)
	}
	if !f.Settings().ClipboardSend {
		t.Fatal("clipboard send not saved")
	}
}

// TestPortsRecheckedOnReopen: reopening the ports with the same numbers
// tests them again, since another app may have taken one meanwhile.
func TestPortsRecheckedOnReopen(t *testing.T) {
	f := newFake()
	f.hostSettings.Ports = HostPorts{Control: 41000, Media: 41001}
	_, tt := settingsAt(t, f, catHost)
	clickScrolled(t, tt, "Ports")
	if tt.HasText("Media port: Another app is using UDP port 41001.") {
		t.Fatal("warning shown before the port was taken")
	}
	clickScrolled(t, tt, "Ports")
	f.set(func(f *fakeBackend) { f.ports = PortStatus{Media: "Another app is using UDP port 41001."} })
	clickScrolled(t, tt, "Ports")
	if !tt.HasText("Media port: Another app is using UDP port 41001.") {
		t.Fatalf("reopened ports kept a stale test: %q", tt.Texts())
	}
}

// TestPortsRecheckedOnReturn: ports left open while another page shows are
// tested again when Settings comes back.
func TestPortsRecheckedOnReturn(t *testing.T) {
	for _, away := range []string{"Hosts", "Statistics"} {
		t.Run(away, func(t *testing.T) {
			f := newFake()
			f.hostSettings.Ports = HostPorts{Control: 41000, Media: 41001}
			_, tt := settingsAt(t, f, catHost)
			clickScrolled(t, tt, "Ports")
			if tt.HasText("Media port: Another app is using UDP port 41001.") {
				t.Fatal("warning shown before the port was taken")
			}
			click(t, tt, away)
			f.set(func(f *fakeBackend) { f.ports = PortStatus{Media: "Another app is using UDP port 41001."} })
			click(t, tt, "Settings")
			scrollTo(t, tt, "Ports")
			if !tt.HasText("Media port: Another app is using UDP port 41001.") {
				t.Fatalf("ports kept a stale test after leaving Settings: %q", tt.Texts())
			}
		})
	}
}

func TestSettingsShowsVersion(t *testing.T) {
	_, tt := settingsAt(t, newFake(), catGeneral)
	if want := "Planck " + version.String(); !tt.HasText(want) {
		t.Fatalf("Settings does not show %q: %q", want, tt.Texts())
	}
	snapshot(t, tt, "settings-version")
}
