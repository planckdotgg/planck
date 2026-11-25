package ui

import (
	"slices"
	"testing"

	"github.com/planckdotgg/planck/pkg/defaults"
)

const (
	hostDisplay1 = `\\.\DISPLAY1`
	hostDisplay2 = `\\.\DISPLAY2`
)

func twoHostDisplays(f *fakeBackend) {
	f.hostDisplays = map[string][]DisplayInfo{"h-bigboi": {
		{ID: hostDisplay1, Name: "Dell U2723QE", Width: 3840, Height: 2160, RefreshHz: 60, Primary: true},
		{ID: hostDisplay2, Width: 2560, Height: 1440, RefreshHz: 165, HDR: true},
	}}
	p := *f.tel.Profile
	p.VirtualDisplay, p.CaptureDisplay = false, hostDisplay1
	f.tel.Profile = &p
}

// TestMenuHostDisplayPicker: a host with two displays gets a Host display
// flyout naming each, the one streaming in force; picking the other
// switches to it, and the flyout follows the stream there.
func TestMenuHostDisplayPicker(t *testing.T) {
	f := streamFake()
	twoHostDisplays(f)
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	click(t, tt, "Host display")
	for _, name := range []string{"Host display: Dell U2723QE", "Host display: Display 2"} {
		if !shows(tt, name) {
			t.Fatalf("choice %q missing: %q", name, tt.Texts())
		}
	}
	if !tt.HasText("2560×1440 165 Hz HDR") {
		t.Errorf("display 2's mode missing: %q", tt.Texts())
	}
	if !inForce(t, tt, "Host display: Dell U2723QE") || inForce(t, tt, "Host display: Display 2") {
		t.Fatal("the slice does not mark the display streaming")
	}
	snapshot(t, tt, "menu-host-display")
	click(t, tt, "Host display: Display 2")
	if !slices.Equal(f.selected, []string{hostDisplay2}) {
		t.Fatalf("selected %q, want display 2", f.selected)
	}
	openMenu(t, tt)
	click(t, tt, "Host display")
	if !inForce(t, tt, "Host display: Display 2") {
		t.Fatal("the flyout did not follow the stream to display 2")
	}
}

// TestMenuHostDisplayPickerSays: a refused switch says why, and a host
// with one display offers no picker.
func TestMenuHostDisplayPickerSays(t *testing.T) {
	f := streamFake()
	twoHostDisplays(f)
	f.selectErr = ErrNoDisplaySwitch
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	click(t, tt, "Host display")
	click(t, tt, "Host display: Display 2")
	tt.Frame()
	if !tt.HasText("Display not switched") || !tt.HasText(ErrNoDisplaySwitch.Error()) {
		t.Fatalf("no reason for the refused switch: %q", tt.Texts())
	}

	one := streamFake()
	one.hostDisplays = map[string][]DisplayInfo{"h-bigboi": {{ID: hostDisplay1, Width: 1920, Height: 1080, Primary: true}}}
	p := *one.tel.Profile
	p.VirtualDisplay, p.CaptureDisplay = false, hostDisplay1
	one.tel.Profile = &p
	_, tt2, _ := newStreamTester(t, one)
	openMenu(t, tt2)
	if _, ok := tt2.Access("Host display"); ok {
		t.Fatal("a one-display host streaming that display offers a display picker")
	}
}

// TestMenuHostDisplayPickerFromVirtual: a stream on a virtual display can
// move to the host's only physical display.
func TestMenuHostDisplayPickerFromVirtual(t *testing.T) {
	f := streamFake()
	f.hostDisplays = map[string][]DisplayInfo{"h-bigboi": {{ID: hostDisplay1, Width: 1920, Height: 1080, RefreshHz: 60, Primary: true}}}
	if !f.tel.Profile.VirtualDisplay {
		t.Fatal("the sample stream is not on a virtual display")
	}
	_, tt, _ := newStreamTester(t, f)
	openMenu(t, tt)
	click(t, tt, "Host display")
	if !shows(tt, "Host display: Display 1 · main") || inForce(t, tt, "Host display: Display 1 · main") {
		t.Fatalf("the only display is not offered as a switch: %q", tt.Texts())
	}
	click(t, tt, "Host display: Display 1 · main")
	if !slices.Equal(f.selected, []string{hostDisplay1}) {
		t.Fatalf("selected %q, want display 1", f.selected)
	}
}

// TestSettingsHDRAndDisplays: HDR10 output is Auto or Off, says why a
// live stream is SDR, and Open all host displays keeps its window cap.
func TestSettingsHDRAndDisplays(t *testing.T) {
	f := newFake()
	f.hosts = sampleHosts()
	f.stats = SessionStats{Live: true, HostName: "bigboi", HostID: "h-bigboi", Profile: &defaults.ResolvedProfile{
		Decisions: []defaults.Decision{{Knob: "hdr10", Value: "off", Source: defaults.SourceAuto, Why: "the host desktop is not in HDR"}}}}
	_, tt := settingsAt(t, f, catVideo)
	if !tt.HasText("bigboi streams SDR: the host desktop is not in HDR.") {
		t.Fatalf("no SDR reason: %q", tt.Texts())
	}
	click(t, tt, "HDR10 output: Off")
	if got := f.override(""); got.HDR10 != defaults.ToggleOff {
		t.Fatalf("HDR10 %q, want off", got.HDR10)
	}
	if _, ok := tt.Access("Most windows: 3"); ok {
		t.Fatal("the window cap shows while Open all host displays is off")
	}
	click(t, tt, "Open all host displays")
	if !f.Settings().OpenEveryHostDisplay {
		t.Fatal("Open all host displays did not turn on")
	}
	click(t, tt, "Most windows: 3")
	if s := f.Settings(); s.MaxStreamWindows != 3 {
		t.Fatalf("most windows %d, want 3", s.MaxStreamWindows)
	}
	snapshot(t, tt, "settings-video-hdr-displays")
}
