package audio

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

// OutputDevices lists the sound server's sinks; a Device ID is a sink name.
func OutputDevices() ([]Device, error) {
	devs, err := sinkDevices()
	if err != nil {
		return nil, err
	}
	return outputsWithoutVirtualMic(devs), nil
}

func sinkDevices() ([]Device, error) {
	return withPulse(func(c *pulse.Client) ([]Device, error) {
		sinks, err := c.ListSinks()
		if err != nil {
			return nil, fmt.Errorf("audio: list output devices: %w", err)
		}
		var def string
		if s, err := c.DefaultSink(); err == nil {
			def = s.ID()
		}
		out := make([]Device, len(sinks))
		for i, s := range sinks {
			out[i] = Device{ID: s.ID(), Name: s.Name(), IsDefault: s.ID() == def}
		}
		return out, nil
	})
}

// InputDevices lists the sound server's microphones, without the monitors
// of its sinks; a Device ID is a source name.
func InputDevices() ([]Device, error) {
	return withPulse(func(c *pulse.Client) ([]Device, error) {
		sources, err := c.ListSources()
		if err != nil {
			return nil, fmt.Errorf("audio: list input devices: %w", err)
		}
		var def string
		if s, err := c.DefaultSource(); err == nil {
			def = s.ID()
		}
		ids := make([]string, len(sources))
		names := make([]string, len(sources))
		for i, s := range sources {
			ids[i], names[i] = s.ID(), s.Name()
		}
		return inputsWithoutMonitors(ids, names, def), nil
	})
}

// MuteOutput mutes the default output device and returns the call that
// puts its mute back as it was.
func MuteOutput() (restore func() error, err error) {
	c, err := newPulseClient()
	if err != nil {
		return nil, err
	}
	sink, err := c.DefaultSink()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("audio: default output device: %w", err)
	}
	var info proto.GetSinkInfoReply
	if err := c.RawRequest(&proto.GetSinkInfo{SinkIndex: proto.Undefined, SinkName: sink.ID()}, &info); err != nil {
		c.Close()
		return nil, fmt.Errorf("audio: read output mute: %w", err)
	}
	setMute := func(mute bool) error {
		return c.RawRequest(&proto.SetSinkMute{SinkIndex: proto.Undefined, SinkName: sink.ID(), Mute: mute}, nil)
	}
	if info.Mute {
		c.Close()
		return func() error { return nil }, nil
	}
	if err := setMute(true); err != nil {
		c.Close()
		return nil, fmt.Errorf("audio: mute output: %w", err)
	}
	return func() error {
		defer c.Close()
		return setMute(false)
	}, nil
}

// RunningApps lists processes by the name the kernel keeps for them
// (/proc/<pid>/comm, at most 15 characters).
func RunningApps() ([]App, error) {
	dirs, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	apps := make([]App, 0, len(dirs))
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, "comm"))
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(string(b)); name != "" {
			apps = append(apps, App{PID: pid, Name: name})
		}
	}
	return apps, nil
}
