package ui

import (
	"context"
	"errors"
	"slices"
	"testing"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/pkg/discovery"
)

// gridHosts has one host in each card state.
func gridHosts() []HostInfo {
	return []HostInfo{
		{ID: "h-old", Name: "old-rig", AdvertisedName: "old-rig", OS: "Windows 10", Paired: true,
			PathKind: discovery.PathLANDirect, Address: "192.168.188.40:47700"},
		{ID: "h-gone", Name: "guest-pc", AdvertisedName: "guest-pc", PathKind: discovery.PathTailscaleDirect},
		{ID: "h-cabin", Name: "cabin-pc", AdvertisedName: "cabin-pc", OS: "Windows 11", GPU: "RX 6700 XT",
			PathKind: discovery.PathTailscaleDERP, Online: true, TailscaleReachable: true},
		{ID: "h-laptop", Name: "laptop", AdvertisedName: "laptop", OS: "macOS 27", GPU: "M5 Pro",
			PathKind: discovery.PathTailscaleDirect, RTTMs: 18, Online: true, Paired: true, TailscaleReachable: true},
		{ID: "h-studio", Name: "Studio", AdvertisedName: "studio-mac", Alias: "Studio", OS: "macOS 27", GPU: "M5 Pro",
			PathKind: discovery.PathLANDirect, RTTMs: 0.4, Online: true, Paired: true,
			LANReachable: true, TailscaleReachable: true},
		{ID: "h-bigboi", Name: "bigboi", AdvertisedName: "bigboi", OS: "Windows 11", GPU: "RTX 4080",
			PathKind: discovery.PathLANDirect, RTTMs: 0.2, Online: true, Paired: true, Connected: true,
			LANReachable: true, Height: 1080, RefreshHz: 60, Address: "192.168.188.20:47700"},
	}
}

func count(texts []string, s string) int {
	n := 0
	for _, t := range texts {
		if t == s {
			n++
		}
	}
	return n
}

func TestHostsGridStates(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	f.tailnet = TailnetStatus{Running: true, User: "watzon@"}
	_, tt := newTester(t, f)
	for _, s := range []string{
		"Connected", "Resume", "Disconnect", "RTX 4080 · 1080p 60",
		"advertised: studio-mac", "0.4 ms", "TS", "18.0 ms",
		"Not paired", "Pair & Connect", "Relay",
		"Offline", "Reconnect", "2 remembered hosts offline", "4 online on your network and tailnet",
	} {
		if !tt.HasText(s) {
			t.Errorf("missing %q", s)
		}
	}
	texts := tt.Texts()
	if n := count(texts, "+TS"); n != 1 {
		t.Errorf("+TS shown %d times, want once, on the host reachable over LAN and Tailscale", n)
	}
	if n := count(texts, "Online"); n != 2 {
		t.Errorf("Online status shown %d times, want 2", n)
	}
	// Offline: the paired host keeps a way to try its last address; the
	// other has none.
	if _, ok := tt.Access("Connect to old-rig"); !ok {
		t.Error("remembered offline host cannot reconnect")
	}
	if _, ok := tt.Access("Connect to guest-pc"); ok {
		t.Error("offline host with no address offers to connect")
	}

	// Active first, then by round trip, then unpaired, then offline.
	order := []string{"Rename bigboi", "Rename Studio", "Rename laptop", "Rename cabin-pc", "Rename old-rig", "Rename guest-pc"}
	var prev mui.Rect
	for i, name := range order {
		r, ok := tt.Find(name)
		if !ok {
			t.Fatalf("%q not found", name)
		}
		if i > 0 && (r.Y < prev.Y || r.Y == prev.Y && r.X <= prev.X) {
			t.Errorf("%q is not after %q", name, order[i-1])
		}
		prev = r
	}
	snapshot(t, tt, "hosts-grid")

	click(t, tt, "Disconnect from bigboi")
	if f.disconnects != 1 {
		t.Fatalf("disconnects = %d", f.disconnects)
	}
	if err := tt.Click("Pair & Connect"); err != nil {
		t.Fatal(err)
	}
	eventually(t, tt, func() bool { return tt.HasText("Connecting to cabin-pc") })
	f.mu.Lock()
	got := slices.Clone(f.connects)
	f.mu.Unlock()
	if !slices.Equal(got, []string{"h-cabin"}) {
		t.Fatalf("connects = %q", got)
	}
	f.connectResult <- errors.New("unreachable")
}

func TestHostsGridWraps(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	_, tt := newTester(t, f)
	tt.SetSize(1000, 800)
	tt.Frame()
	a, _ := tt.Find("Rename bigboi")
	b, _ := tt.Find("Rename laptop")
	d, _ := tt.Find("Rename cabin-pc")
	if a.Y != b.Y || d.Y <= a.Y {
		t.Fatalf("at 1000 px want three cards a row: bigboi %v, laptop %v, cabin-pc %v", a, b, d)
	}
	if _, ok := tt.Find("Rename guest-pc"); !ok {
		t.Fatal("last card missing")
	}
	snapshot(t, tt, "hosts-grid-narrow")
}

func TestHostsEmptyQuickConnectLink(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	snapshot(t, tt, "hosts-scanning")
	click(t, tt, "Use quick connect")
	tt.Frame()
	tt.Type("10.0.0.9")
	tt.Frame()
	if a.quickConnect != "10.0.0.9" {
		t.Fatalf("quick connect = %q: the link did not focus it", a.quickConnect)
	}
}

func renames(f *fakeBackend) [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.renames)
}

func TestRenameSave(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	a, tt := newTester(t, f)
	click(t, tt, "Rename laptop")
	if !tt.HasText("Local name") || a.renameText != "laptop" {
		t.Fatalf("rename not open with the name: %q, %q", a.renameText, tt.Texts())
	}
	snapshot(t, tt, "hosts-rename")
	// The whole name is selected, so typing replaces it.
	tt.Type("Couch laptop")
	tt.Frame()
	click(t, tt, "Save")
	if got := renames(f); !slices.Equal(got, [][2]string{{"h-laptop", "Couch laptop"}}) {
		t.Fatalf("renames = %q", got)
	}
	if tt.HasText("Local name") {
		t.Fatal("rename still open after Save")
	}

	click(t, tt, "Rename laptop")
	tt.Type("Den")
	tt.Key(0, mui.KeyEnter)
	tt.Frame()
	if got := renames(f); len(got) != 2 || got[1] != [2]string{"h-laptop", "Den"} {
		t.Fatalf("Enter: renames = %q", got)
	}

	click(t, tt, "Rename laptop")
	click(t, tt, "Save")
	if got := renames(f); len(got) != 2 {
		t.Fatalf("an unchanged name was stored: %q", got)
	}

	click(t, tt, "Rename laptop")
	tt.Key(0, mui.KeyBackspace)
	tt.Frame()
	click(t, tt, "Save")
	if got := renames(f); len(got) != 3 || got[2] != [2]string{"h-laptop", ""} {
		t.Fatalf("empty name: renames = %q, want a reset", got)
	}
}

func TestRenameCancel(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	_, tt := newTester(t, f)
	click(t, tt, "Rename laptop")
	tt.Type("nope")
	tt.Frame()
	click(t, tt, "Cancel")
	if tt.HasText("Local name") {
		t.Fatal("rename still open after Cancel")
	}

	click(t, tt, "Rename laptop")
	tt.Type("nope")
	tt.Key(0, mui.KeyEscape)
	tt.Frame()
	if tt.HasText("Local name") {
		t.Fatal("rename still open after Esc")
	}
	if got := renames(f); len(got) != 0 {
		t.Fatalf("cancel stored a name: %q", got)
	}
}

func TestRenameReset(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	_, tt := newTester(t, f)
	click(t, tt, "Rename laptop")
	if tt.HasText("Reset") {
		t.Fatal("Reset shown for a host with no alias")
	}
	click(t, tt, "Cancel")

	click(t, tt, "Rename Studio")
	snapshot(t, tt, "hosts-rename-alias")
	click(t, tt, "Reset")
	if got := renames(f); !slices.Equal(got, [][2]string{{"h-studio", ""}}) {
		t.Fatalf("renames = %q", got)
	}
}

func TestRenameError(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	f.renameErr = errors.New("a host name is at most 63 bytes")
	_, tt := newTester(t, f)
	click(t, tt, "Rename laptop")
	tt.Type("way too long")
	tt.Frame()
	click(t, tt, "Save")
	if !tt.HasText("a host name is at most 63 bytes") || !tt.HasText("Local name") {
		t.Fatalf("rename error not shown in the open editor: %q", tt.Texts())
	}
}

func TestSettingsHostName(t *testing.T) {
	f := newFake()
	a, tt := newTester(t, f)
	a.screen, a.set.cat = ScreenSettings, catHost
	tt.Frame()
	field, ok := tt.AccessByRole(mui.RoleTextField, "Host name")
	if !ok {
		t.Fatalf("no Host name field: %q", tt.Texts())
	}
	tt.ClickAt(field.Bounds.X+field.Bounds.W/2, field.Bounds.Y+field.Bounds.H/2)
	tt.Frame()
	tt.Type(" living-room ")
	tt.Frame()
	f.mu.Lock()
	n := len(f.saved)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("saved %d times while typing", n)
	}
	tt.Key(0, mui.KeyEnter)
	tt.Frame()
	if got := f.Settings(); got.HostName != "living-room" {
		t.Fatalf("host name = %q", got.HostName)
	}
	if got := f.Settings(); got.AudioEnabled != newFake().settings.AudioEnabled || f.ovSaves != 0 {
		t.Fatalf("other settings changed: %+v, %d override saves", got, f.ovSaves)
	}
}

// TestRemoveHost: a paired host's × asks first, in the card's own body,
// without growing the card or connecting; a host gone and never paired
// goes at once; an online unpaired host and the live one offer no ×.
func TestRemoveHost(t *testing.T) {
	f := newFake()
	f.hosts = gridHosts()
	_, tt := newTester(t, f)
	for _, name := range []string{"Remove bigboi", "Remove cabin-pc"} {
		if _, ok := tt.Access(name); ok {
			t.Errorf("%q offered", name)
		}
	}
	reconnect, ok := tt.Find("Connect to old-rig")
	if !ok {
		t.Fatal("no Reconnect on old-rig")
	}

	click(t, tt, "Remove old-rig")
	if !tt.HasText("Remove old-rig?") || !tt.HasText("Connecting again needs its PIN.") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	confirm, ok := tt.Find("Confirm remove old-rig")
	if !ok || confirm.Y != reconnect.Y {
		t.Fatalf("confirm button at %+v, want the card's action row at y %v", confirm, reconnect.Y)
	}
	snapshot(t, tt, "hosts-remove-confirm")
	click(t, tt, "Keep old-rig")
	if tt.HasText("Remove old-rig?") || len(f.removes) != 0 {
		t.Fatalf("cancel: removes %q, texts %q", f.removes, tt.Texts())
	}

	click(t, tt, "Remove old-rig")
	click(t, tt, "Confirm remove old-rig")
	click(t, tt, "Remove guest-pc")
	tt.Frame()
	if !slices.Equal(f.removes, []string{"h-old", "h-gone"}) {
		t.Fatalf("removes %q", f.removes)
	}
	for _, name := range []string{"Rename old-rig", "Rename guest-pc"} {
		if _, ok := tt.Access(name); ok {
			t.Errorf("%q still listed", name)
		}
	}
	if len(f.connects) != 0 {
		t.Fatalf("removing connected to %q", f.connects)
	}
}

// TestConnectReplacesPending: Pair & Connect leaves the launcher on Hosts,
// so a connect that never ends is invisible there; Connect on another host
// replaces it rather than doing nothing.
func TestConnectReplacesPending(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	_, sw, launcher, _ := newSplitTester(t, f)
	click(t, launcher, "Connect to cabin-pc")
	eventually(t, launcher, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.connects) == 1 })
	click(t, launcher, "Connect to bigboi")
	eventually(t, launcher, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.connects) == 2 })
	f.mu.Lock()
	got, ctxErr := slices.Clone(f.connects), f.connectCtxErr
	f.mu.Unlock()
	if !slices.Equal(got, []string{"h-cabin", "h-bigboi"}) || !errors.Is(ctxErr, context.Canceled) {
		t.Fatalf("connects %q, first one's context %v", got, ctxErr)
	}
	if !slices.Equal(sw.opens, []string{"bigboi"}) {
		t.Fatalf("stream windows opened %q", sw.opens)
	}
	click(t, launcher, "Connect to bigboi")
	if f.mu.Lock(); len(f.connects) != 2 {
		t.Fatalf("a second click on the same host connected again: %q", f.connects)
	}
	f.mu.Unlock()
	f.connectResult <- errors.New("host refused the session")
	eventually(t, launcher, func() bool { return launcher.HasText("Could not connect: host refused the session") })
}
