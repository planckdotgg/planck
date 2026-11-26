package audio

import (
	"slices"
	"strings"
	"testing"
)

func TestModuleArgs(t *testing.T) {
	sink := nullSinkArgs()
	for _, want := range []string{"sink_name=planck_virtual_mic", "rate=48000", "channels=1", "channel_map=mono",
		`sink_properties='device.description="Planck Virtual Audio"'`} {
		if !strings.Contains(sink, want) {
			t.Errorf("null sink args %q lack %q", sink, want)
		}
	}
	src := virtualSourceArgs()
	for _, want := range []string{"source_name=planck_virtual_mic_source", "master=planck_virtual_mic.monitor",
		`source_properties='device.description="Planck Virtual Microphone"'`} {
		if !strings.Contains(src, want) {
			t.Errorf("virtual source args %q lack %q", src, want)
		}
	}
	if quoteProp(`a"b`) != `"a\"b"` {
		t.Errorf("quoteProp = %s", quoteProp(`a"b`))
	}
}

func TestMicDevices(t *testing.T) {
	sinks := []Device{{ID: "alsa_output.pci", Name: "Speakers", IsDefault: true}}
	mics := micDevices(sinks)
	if len(mics) != 2 || mics[1].ID != virtualMicSink || mics[1].Name != PlanckVirtualAudio {
		t.Fatalf("without the null sink: %+v", mics)
	}
	if i := pickVirtualMic(namesOf(mics), ""); i != 1 {
		t.Errorf("auto pick = %d, want Planck's device", i)
	}
	sinks = append(sinks, Device{ID: virtualMicSink, Name: "Planck Virtual Audio"})
	if mics := micDevices(sinks); len(mics) != 2 {
		t.Errorf("with the null sink loaded: %+v", mics)
	}
	if out := outputsWithoutVirtualMic(sinks); len(out) != 1 || out[0].ID != "alsa_output.pci" {
		t.Errorf("outputs %+v", out)
	}
}

func namesOf(mics []VirtualMic) []string {
	names := make([]string, len(mics))
	for i, m := range mics {
		names[i] = m.Name
	}
	return names
}

func TestInputsWithoutMonitors(t *testing.T) {
	ids := []string{"alsa_output.pci.monitor", "alsa_input.usb", "bluez_input.x"}
	names := []string{"Monitor of Speakers", "USB Mic", "Headset"}
	got := inputsWithoutMonitors(ids, names, "bluez_input.x")
	want := []Device{{ID: "alsa_input.usb", Name: "USB Mic"}, {ID: "bluez_input.x", Name: "Headset", IsDefault: true}}
	if !slices.Equal(got, want) {
		t.Errorf("inputs %+v, want %+v", got, want)
	}
}
