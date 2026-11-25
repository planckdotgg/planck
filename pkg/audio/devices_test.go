package audio

import (
	"os"
	"runtime"
	"slices"
	"testing"
)

func TestPickDevice(t *testing.T) {
	devs := []Device{{ID: "a", Name: "Speakers"}, {ID: "b", Name: "Headphones", IsDefault: true}}
	if d, gone := PickDevice(devs, "a"); d.ID != "a" || gone {
		t.Errorf("picked %+v gone %v, want a", d, gone)
	}
	if d, gone := PickDevice(devs, ""); d.ID != "b" || gone {
		t.Errorf("default: %+v gone %v, want b", d, gone)
	}
	if d, gone := PickDevice(devs, "unplugged"); d.ID != "b" || !gone {
		t.Errorf("unplugged: %+v gone %v, want the default and gone", d, gone)
	}
	if d, gone := PickDevice(nil, "x"); d != (Device{}) || !gone {
		t.Errorf("no devices: %+v gone %v", d, gone)
	}
}

func TestMatchApps(t *testing.T) {
	apps := []App{{1, "Discord"}, {2, "Discord Helper (Renderer)"}, {3, "Music"}, {4, "discordant"}, {5, "Teams"}, {6, "Finder"}}
	got := MatchApps(apps, []string{"discord", " Teams ", ""})
	if !slices.Equal(got, []int{1, 2, 4, 5}) {
		t.Errorf("MatchApps = %v", got)
	}
	if MatchApps(apps, nil) != nil {
		t.Error("no names matched something")
	}
}

// TestListDevicesLive reads this machine's devices and processes; it
// changes nothing.
func TestListDevicesLive(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("no audio backend")
	}
	for name, list := range map[string]func() ([]Device, error){"output": OutputDevices, "input": InputDevices} {
		devs, err := list()
		if err != nil {
			t.Fatalf("%s devices: %v", name, err)
		}
		defaults := 0
		for _, d := range devs {
			if d.ID == "" {
				t.Errorf("%s device %q has no ID", name, d.Name)
			}
			if d.IsDefault {
				defaults++
			}
		}
		t.Logf("%s devices: %+v", name, devs)
		if len(devs) > 0 && defaults > 1 {
			t.Errorf("%s devices: %d defaults", name, defaults)
		}
	}
	apps, err := RunningApps()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(apps, func(a App) bool { return a.PID == os.Getpid() }) {
		t.Errorf("RunningApps lacks this test (pid %d) among %d", os.Getpid(), len(apps))
	}
}

// TestMuteOutputLive mutes and restores this machine's speakers; it runs
// only when asked.
func TestMuteOutputLive(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_MUTE_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_MUTE_TEST=1 to mute this machine for a moment")
	}
	restore, err := MuteOutput()
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
}
