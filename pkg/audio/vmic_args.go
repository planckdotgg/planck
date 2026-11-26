package audio

import (
	"fmt"
	"strings"
)

// The null sink and virtual source Planck makes on a Linux host: a client's
// microphone plays into the sink, and the source, which apps list as a
// microphone, reads the sink's monitor.
const (
	virtualMicSink     = "planck_virtual_mic"
	virtualMicSource   = "planck_virtual_mic_source"
	virtualMicSinkName = PlanckVirtualAudio
	virtualMicInput    = "Planck Virtual Microphone"
)

// quoteProp quotes a PulseAudio module argument value that holds spaces.
func quoteProp(v string) string { return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"` }

func nullSinkArgs() string {
	return fmt.Sprintf("sink_name=%s rate=%d channels=%d channel_map=mono sink_properties=%s",
		virtualMicSink, SampleRate, MicChannels,
		"'device.description="+quoteProp(virtualMicSinkName)+"'")
}

func virtualSourceArgs() string {
	return fmt.Sprintf("source_name=%s master=%s.monitor rate=%d channels=%d channel_map=mono source_properties=%s",
		virtualMicSource, virtualMicSink, SampleRate, MicChannels,
		"'device.description="+quoteProp(virtualMicInput)+"'")
}

// micDevices builds the virtual mic candidates from the sound server's
// output devices: each as it is, and Planck's own null sink as a device
// that is made on first use when the server does not have it yet.
func micDevices(sinks []Device) []VirtualMic {
	mics := make([]VirtualMic, 0, len(sinks)+1)
	have := false
	for _, s := range sinks {
		if s.ID == virtualMicSink {
			have = true
		}
		mics = append(mics, VirtualMic{ID: s.ID, Name: s.Name})
	}
	if !have {
		mics = append(mics, VirtualMic{ID: virtualMicSink, Name: virtualMicSinkName})
	}
	return mics
}

// outputsWithoutVirtualMic is the output devices a person can pick to
// play to or record: Planck's own null sink is not one of them.
func outputsWithoutVirtualMic(devs []Device) []Device {
	out := make([]Device, 0, len(devs))
	for _, d := range devs {
		if d.ID != virtualMicSink {
			out = append(out, d)
		}
	}
	return out
}

// inputsWithoutMonitors is the sources that are microphones: a sink's
// monitor, named "<sink>.monitor", is not one.
func inputsWithoutMonitors(ids, names []string, def string) []Device {
	out := make([]Device, 0, len(ids))
	for i, id := range ids {
		if isMonitor(id) {
			continue
		}
		out = append(out, Device{ID: id, Name: names[i], IsDefault: id == def})
	}
	return out
}

func isMonitor(sourceID string) bool { return strings.HasSuffix(sourceID, ".monitor") }
