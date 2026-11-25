//go:build windows

package audio

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OutputDevices lists the active render endpoints.
func OutputDevices() ([]Device, error) { return endpointDevices(eRender) }

// InputDevices lists the active capture endpoints, microphones among them.
func InputDevices() ([]Device, error) { return endpointDevices(eCapture) }

func endpointDevices(flow uintptr) ([]Device, error) {
	eps, err := endpoints(flow)
	if err != nil {
		return nil, err
	}
	var def string
	_ = onCOMThread(func() error {
		dev, err := openDevice(flow, "")
		if err != nil {
			return err
		}
		def, _ = deviceIDAndName(dev)
		dev.release()
		return nil
	})
	out := make([]Device, len(eps))
	for i, e := range eps {
		out[i] = Device{ID: e.ID, Name: e.Name, IsDefault: e.ID == def}
	}
	return out, nil
}

// MuteOutput mutes the default render endpoint and returns what puts its
// mute back as it was.
func MuteOutput() (restore func() error, err error) {
	var id string
	var was bool
	err = onCOMThread(func() error {
		dev, err := openDevice(eRender, "")
		if err != nil {
			return err
		}
		id, _ = deviceIDAndName(dev)
		dev.release()
		st, err := readEndpoint(id)
		if err != nil {
			return err
		}
		was = st.muted
		return setMute(id, true)
	})
	if err != nil {
		return nil, err
	}
	return func() error { return onCOMThread(func() error { return setMute(id, was) }) }, nil
}

// RunningApps lists the processes by their executable's name, without
// ".exe".
func RunningApps() ([]App, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("audio: list processes: %w", err)
	}
	defer windows.CloseHandle(snap)
	var out []App
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if i := strings.LastIndexByte(name, '.'); i > 0 && strings.EqualFold(name[i:], ".exe") {
			name = name[:i]
		}
		if e.ProcessID != 0 && name != "" {
			out = append(out, App{PID: int(e.ProcessID), Name: name})
		}
	}
	return out, nil
}
