package audio

import "strings"

// Device is an audio device of this machine.
type Device struct {
	// ID is stable across runs: a Core Audio UID on macOS, an MMDevice
	// endpoint ID on Windows.
	ID        string
	Name      string
	IsDefault bool
}

// PickDevice is the device of devs with id, or the default one when id is
// empty or names none of them; gone reports that id named a device that
// is not there. An empty Device stands for the default when devs has
// none marked.
func PickDevice(devs []Device, id string) (dev Device, gone bool) {
	var def Device
	for _, d := range devs {
		if id != "" && d.ID == id {
			return d, false
		}
		if d.IsDefault && def.ID == "" {
			def = d
		}
	}
	return def, id != ""
}

// App is a running process, by its executable's name without a path or
// ".exe".
type App struct {
	PID  int
	Name string
}

// MatchApps are the PIDs of apps whose name starts with one of names, in
// any case: "Discord" takes "Discord Helper (Renderer)" too, where an
// Electron app plays its sound.
func MatchApps(apps []App, names []string) []int {
	var pids []int
	for _, a := range apps {
		n := strings.ToLower(a.Name)
		for _, want := range names {
			if w := strings.ToLower(strings.TrimSpace(want)); w != "" && strings.HasPrefix(n, w) {
				pids = append(pids, a.PID)
				break
			}
		}
	}
	return pids
}

// AppPIDs are the processes of the running apps named names (MatchApps).
func AppPIDs(names []string) ([]int, error) {
	if len(names) == 0 {
		return nil, nil
	}
	apps, err := RunningApps()
	if err != nil {
		return nil, err
	}
	return MatchApps(apps, names), nil
}
