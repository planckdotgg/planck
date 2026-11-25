package ui

import (
	"slices"
	"strings"
	"testing"

	mui "github.com/egoist/mygo/ui"
)

// settingsOn opens Settings at cat as a client on goos sees it.
func settingsOn(t *testing.T, f *fakeBackend, cat settingsCategory, goos string) (*App, *mui.Tester) {
	t.Helper()
	a, tt := newTester(t, f)
	a.goos = goos
	a.screen, a.set.cat = ScreenSettings, cat
	tt.Frame()
	return a, tt
}

func chordFor(f *fakeBackend, action HotkeyAction) string {
	for _, h := range f.Hotkeys() {
		if h.Action == action {
			return h.Chord
		}
	}
	return "?"
}

func press(tt *mui.Tester, mods mui.Modifiers, key mui.Key) {
	tt.Key(mods, key)
	tt.Frame()
}

// TestHotkeyCapture: clicking a chord pill records; a chord binds it and
// shows, its Reset gives the default back, Esc keeps the old chord and
// Backspace unbinds it.
func TestHotkeyCapture(t *testing.T) {
	f := newFake()
	a, tt := settingsOn(t, f, catInput, "darwin")
	if _, ok := tt.Access("Reset Metrics window"); ok {
		t.Fatal("Reset shows for a hotkey on its default")
	}
	click(t, tt, "Shortcut for Metrics window")
	if !tt.HasText("Press keys…") {
		t.Fatalf("no recording state: %q", tt.Texts())
	}
	press(tt, mui.Ctrl|mui.Alt, mui.KeyJ)
	if got := chordFor(f, HotkeyMetrics); got != "Ctrl+Alt+J" {
		t.Fatalf("metrics = %q, want Ctrl+Alt+J", got)
	}
	if tt.HasText("Press keys…") || !tt.HasText("⌃⌥J") || a.set.keys.recording != "" {
		t.Fatalf("recording did not end on a bound chord: %q", tt.Texts())
	}

	click(t, tt, "Reset Metrics window")
	if got := chordFor(f, HotkeyMetrics); got != "Ctrl+Alt+M" {
		t.Fatalf("after reset metrics = %q", got)
	}

	click(t, tt, "Shortcut for Full screen")
	press(tt, 0, mui.KeyEscape)
	if got := chordFor(f, HotkeyFullscreen); got != "Ctrl+Alt+F" || tt.HasText("Press keys…") {
		t.Fatalf("Esc: full screen = %q, texts %q", got, tt.Texts())
	}

	click(t, tt, "Shortcut for Microphone")
	press(tt, 0, mui.KeyBackspace)
	if got := chordFor(f, HotkeyMicrophone); got != "" {
		t.Fatalf("Backspace left microphone on %q", got)
	}
	if info, ok := tt.Access("Shortcut for Microphone"); !ok || info.Description != "Not set" {
		t.Fatalf("unbound pill: %+v", info)
	}
	snapshot(t, tt, "settings-hotkeys")
}

// TestHotkeyConflict: a chord another hotkey has, one the OS keeps, and
// one with no modifier each say why under the row and keep recording;
// Esc then keeps the old chord and clears the warning.
func TestHotkeyConflict(t *testing.T) {
	f := newFake()
	a, tt := settingsOn(t, f, catInput, "darwin")
	click(t, tt, "Shortcut for Switch host display")
	for _, c := range []struct {
		mods mui.Modifiers
		key  mui.Key
		want string
	}{
		{mui.Ctrl | mui.Alt, mui.KeyH, "⌃⌥H already belongs to Cycle HUD mode. Press another chord, or Esc to keep ⌃⌥]."},
		{mui.Super, mui.KeyQ, "⌘Q: macOS quits Planck with it. Press another chord, or Esc to keep ⌃⌥]."},
		{0, mui.KeyJ, "A shortcut needs at least one modifier. Press another chord, or Esc to keep ⌃⌥]."},
	} {
		press(tt, c.mods, c.key)
		if !tt.HasText(c.want) {
			t.Errorf("no %q: %q", c.want, tt.Texts())
		}
		if a.set.keys.recording != HotkeySwitchDisplay || !tt.HasText("Press keys…") {
			t.Errorf("a refused chord ended the recording")
		}
	}
	snapshot(t, tt, "settings-hotkeys-conflict")
	press(tt, 0, mui.KeyEscape)
	if got := chordFor(f, HotkeySwitchDisplay); got != "Ctrl+Alt+]" {
		t.Fatalf("switch display = %q after refusals", got)
	}
	if tt.HasText("Press keys…") || a.set.keys.err != "" {
		t.Fatalf("Esc left the warning or the recording: %q", tt.Texts())
	}
}

// TestHotkeyResetAll: Reset all to defaults gives every hotkey its
// default, the host's global ones too.
func TestHotkeyResetAll(t *testing.T) {
	f := newFake()
	for action, chord := range map[HotkeyAction]string{HotkeyMetrics: "Ctrl+Alt+J", HotkeyStopHosting: "", HotkeyKickClient: "Ctrl+Alt+Shift+K"} {
		if err := f.SetHotkey(action, chord); err != nil {
			t.Fatal(err)
		}
	}
	_, tt := settingsOn(t, f, catInput, "darwin")
	if !tt.HasText("WHEN HOSTING") {
		t.Fatalf("no host group: %q", tt.Texts())
	}
	clickScrolled(t, tt, "Reset all to defaults")
	for _, h := range f.Hotkeys() {
		if h.Chord != h.Default {
			t.Errorf("%s = %q, want its default %q", h.Action, h.Chord, h.Default)
		}
	}
}

// TestNeverSendList: + Record a chord adds the next chord, bare keys
// too; a hotkey's chord is refused with why; a chip's × removes it.
func TestNeverSendList(t *testing.T) {
	f := newFake()
	_, tt := settingsOn(t, f, catInput, "darwin")
	clickScrolled(t, tt, "Record a chord to never send")
	press(tt, mui.Alt|mui.Shift, mui.KeyQ)
	clickScrolled(t, tt, "Record a chord to never send")
	press(tt, 0, mui.KeyF12)
	if got := f.NeverSend(); !slices.Equal(got, []string{"Alt+Shift+Q", "F12"}) {
		t.Fatalf("never send = %q", got)
	}
	if !tt.HasText("⌥⇧Q") || !tt.HasText("F12") {
		t.Fatalf("chips missing: %q", tt.Texts())
	}

	clickScrolled(t, tt, "Record a chord to never send")
	press(tt, mui.Ctrl|mui.Alt, mui.KeyO)
	if !tt.HasText("⌃⌥O is the Stream menu hotkey, which never reaches the host already. Press another chord, or Esc.") {
		t.Fatalf("no refusal: %q", tt.Texts())
	}
	press(tt, 0, mui.KeyEscape)
	if len(f.NeverSend()) != 2 || tt.HasText("Press keys…") {
		t.Fatalf("Esc: %q, texts %q", f.NeverSend(), tt.Texts())
	}

	clickScrolled(t, tt, "Send ⌥⇧Q to the host again")
	if got := f.NeverSend(); !slices.Equal(got, []string{"F12"}) {
		t.Fatalf("after remove never send = %q", got)
	}
	snapshot(t, tt, "settings-never-send")
}

// TestSwapCmdCtrl: the swap switch saves to Settings, named for the
// other OS family.
func TestSwapCmdCtrl(t *testing.T) {
	for goos, name := range map[string]string{"darwin": "Swap ⌘ and Ctrl on Windows hosts", "windows": "Swap Ctrl and ⌘ on Mac hosts"} {
		f := newFake()
		_, tt := settingsOn(t, f, catInput, goos)
		clickScrolled(t, tt, name)
		if !f.Settings().SwapCmdCtrl {
			t.Errorf("%s: swap not saved", goos)
		}
		clickScrolled(t, tt, name)
		if f.Settings().SwapCmdCtrl {
			t.Errorf("%s: swap not turned off", goos)
		}
	}
}

func sampleAudioDevices() AudioDevices {
	return AudioDevices{
		Outputs: []AudioDevice{{ID: "o1", Name: "MacBook Pro Speakers", IsDefault: true}, {ID: "o2", Name: "AirPods Pro"}, {ID: "o3", Name: "BlackHole 2ch"}},
		Inputs:  []AudioDevice{{ID: "i1", Name: "MacBook Pro Microphone", IsDefault: true}, {ID: "i2", Name: "TONOR G11"}},
	}
}

// TestAudioDevicePickers: the output, microphone, capture and virtual mic
// dropdowns list the devices and save the one picked where it belongs.
func TestAudioDevicePickers(t *testing.T) {
	f := newFake()
	f.audioDevices = sampleAudioDevices()
	_, tt := settingsOn(t, f, catAudio, "windows")
	choose(t, tt, "Output device", "AirPods Pro")
	choose(t, tt, "Microphone device", "TONOR G11")
	choose(t, tt, "Capture device", "MacBook Pro Speakers")
	choose(t, tt, "Virtual mic sink", "BlackHole 2ch")
	s, hs := f.Settings(), f.HostSettings()
	if s.OutputDevice != "o2" || s.MicDevice != "i2" || hs.AudioDevice != "o1" || hs.VirtualMic != "o3" {
		t.Fatalf("settings %+v, host %+v", s, hs)
	}
	choose(t, tt, "Virtual mic sink", "Off")
	choose(t, tt, "Output device", "System default")
	if f.HostSettings().VirtualMic != VirtualMicOff || f.Settings().OutputDevice != "" {
		t.Fatalf("off / default not kept: %+v %+v", f.HostSettings(), f.Settings())
	}

	f.set(func(f *fakeBackend) { f.settings.MicDevice = "gone" })
	tt.Frame()
	if info, ok := tt.AccessByRole(mui.RolePopUpButton, "Microphone device"); !ok || !tt.HasText("Unplugged, records from the default") {
		t.Fatalf("an unplugged mic does not say so: %+v %q", info, tt.Texts())
	}
}

// TestAudioCaptureOnMac: a macOS host streams the system mix, with no
// device to pick.
func TestAudioCaptureOnMac(t *testing.T) {
	f := newFake()
	f.audioDevices = sampleAudioDevices()
	_, tt := settingsOn(t, f, catAudio, "darwin")
	if _, ok := tt.AccessByRole(mui.RolePopUpButton, "Capture device"); ok {
		t.Fatal("a macOS host offers capture devices")
	}
	if !tt.HasText("System mix · ScreenCaptureKit") {
		t.Fatalf("no system mix: %q", tt.Texts())
	}
}

// TestExcludeApps: a chip's × removes an app; + App adds one by a quick
// pick, a running app or a typed name, once each.
func TestExcludeApps(t *testing.T) {
	f := newFake()
	f.apps = []string{"Spotify", "Discord"}
	f.hostSettings.ExcludeApps = NewAppList("Discord", "Music")
	a, tt := settingsOn(t, f, catAudio, "darwin")
	clickScrolled(t, tt, "Stop leaving out Music")
	if got := f.HostSettings().ExcludeApps.Names(); !slices.Equal(got, []string{"Discord"}) {
		t.Fatalf("after remove: %q", got)
	}
	clickScrolled(t, tt, "Add an app to leave out")
	if _, ok := tt.Access("Leave out Discord"); ok {
		t.Fatal("offers an app already left out")
	}
	clickScrolled(t, tt, "Leave out Slack")
	clickScrolled(t, tt, "Leave out Spotify")
	typeInto(t, tt, "App name", "OBS")
	if got := f.HostSettings().ExcludeApps.Names(); !slices.Equal(got, []string{"Discord", "Slack", "Spotify", "OBS"}) {
		t.Fatalf("after adds: %q", got)
	}
	snapshot(t, tt, "settings-audio")
	clickScrolled(t, tt, "Done")
	if a.set.appAdding {
		t.Fatal("Done did not close the picker")
	}
}

// TestHostSessionSettings: the session & privacy switches and the idle
// disconnect save to host.json; privacy mode says why it cannot work;
// Kick shows while a client streams and disconnects it.
func TestHostSessionSettings(t *testing.T) {
	f := newFake()
	f.hosting = HostingState{On: true, Client: "iPad Pro"}
	_, tt := settingsOn(t, f, catHost, "windows")
	click(t, tt, "Stay awake while hosting")
	click(t, tt, "Lock this PC when the session ends")
	click(t, tt, "Mute this computer’s speakers while streaming")
	click(t, tt, "Privacy mode — blank the physical displays")
	choose(t, tt, "Disconnect idle clients after", "30 min")
	hs := f.HostSettings()
	if !hs.StayAwake || !hs.LockOnDisconnect || !hs.MuteSpeakers || !hs.PrivacyMode || hs.IdleTimeoutMinutes != 30 {
		t.Fatalf("host settings = %+v", hs)
	}
	clickScrolled(t, tt, "Kick iPad Pro")
	f.mu.Lock()
	kicks := f.kicks
	f.mu.Unlock()
	if kicks != 1 {
		t.Fatalf("%d kicks, want 1", kicks)
	}
	snapshot(t, tt, "settings-host-session")

	f.set(func(f *fakeBackend) { f.hosting.Client = "" })
	tt.Frame()
	if _, ok := tt.Access("Kick iPad Pro"); ok || !tt.HasText("No device is streaming from this PC.") {
		t.Fatalf("Kick shown with no client: %q", tt.Texts())
	}
}

// TestPrivacyModeUnavailable: where privacy mode cannot work, its row
// says why and cannot be turned on.
func TestPrivacyModeUnavailable(t *testing.T) {
	f := newFake()
	f.privacyProblem = "Needs a virtual display. Not available on macOS hosts yet."
	_, tt := settingsOn(t, f, catHost, "darwin")
	if !tt.HasText(f.privacyProblem) {
		t.Fatalf("no reason: %q", tt.Texts())
	}
	if err := tt.Click("Privacy mode — blank the physical displays"); err == nil {
		tt.Frame()
	}
	if f.HostSettings().PrivacyMode {
		t.Fatal("privacy mode turned on where it cannot work")
	}
}

// TestSettingsSearchPhase2: the search finds the new hotkey, device and
// host session settings under their categories.
func TestSettingsSearchPhase2(t *testing.T) {
	for _, c := range []struct {
		query string
		cat   settingsCategory
		text  string
	}{
		{"push-to-talk", catInput, "Never send these keys"},
		{"swap", catInput, "Swap ⌘ and Ctrl on Windows hosts"},
		{"reset", catInput, "Shortcut for Stream menu"},
		{"keyframe", catInput, "Shortcut for Ask the host for a keyframe"},
		{"headphones", catAudio, "Output device"},
		{"virtual mic", catAudio, "Virtual mic sink"},
		{"discord", catAudio, "Leave out"},
		{"stay awake", catHost, "Stay awake while hosting"},
		{"idle", catHost, "Disconnect idle clients after"},
		{"privacy", catHost, "Privacy mode — blank the physical displays"},
		{"kick", catHost, "Connected client"},
		{"kick", catInput, "Shortcut for Kick the connected client"},
	} {
		t.Run(c.query, func(t *testing.T) {
			f := newFake()
			_, tt := settingsOn(t, f, catGeneral, "darwin")
			click(t, tt, "Search settings")
			tt.Type(c.query)
			tt.Frame()
			if !navCount(t, tt, c.cat) {
				t.Errorf("%q finds nothing in %s", c.query, categoryNames[c.cat])
			}
			if _, ok := tt.Access(c.text); !ok && !tt.HasText(c.text) {
				t.Errorf("%q does not show %q: %q", c.query, c.text, tt.Texts())
			}
		})
	}
}

// TestHotkeyNotGlobal: a host hotkey on a key the desktop cannot take
// while Planck is not in front says so and keeps the old chord.
func TestHotkeyNotGlobal(t *testing.T) {
	f := newFake()
	_, tt := settingsOn(t, f, catInput, "darwin")
	clickScrolled(t, tt, "Shortcut for Stop hosting")
	press(tt, mui.Ctrl, mui.KeyContextMenu)
	if want := "cannot fire while Planck is not in front. Press another chord, or Esc to keep ⌃F3."; !slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasSuffix(s, want) }) {
		t.Errorf("no %q: %q", want, tt.Texts())
	}
	if got := chordFor(f, HotkeyStopHosting); got != "Ctrl+F3" {
		t.Errorf("stop hosting = %q after the refusal", got)
	}
}
