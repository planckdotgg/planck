package audio

import (
	"errors"
	"strings"
)

// PlanckVirtualAudio is the name of the device the Planck Virtual Audio
// drivers in drivers/audio create, on Windows and macOS alike.
const PlanckVirtualAudio = "Planck Virtual Audio"

// VirtualAudioStatus is whether Planck Virtual Audio is on this machine,
// and which virtual mic a host on Auto plays a client's microphone into.
type VirtualAudioStatus struct {
	// Installed: the Planck Virtual Audio driver is installed, whether or
	// not its device is running.
	Installed bool
	// Active: a Planck Virtual Audio device is running now.
	Active bool
	// DeviceName is the output device Auto picks, "" when there is none.
	DeviceName string
	// DriverName is the virtual audio driver behind DeviceName, as
	// "Planck Virtual Audio" or "Steam Streaming Microphone".
	DriverName string
	// InstallHint says how to get Planck Virtual Audio running; "" when
	// it is.
	InstallHint string
}

// VirtualAudio reports Planck Virtual Audio's driver and device, and the
// virtual mic Auto would use.
func VirtualAudio() (VirtualAudioStatus, error) {
	devs, err := outputDevices()
	if err != nil && !errors.Is(err, ErrUnsupported) {
		return VirtualAudioStatus{}, err
	}
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Name
	}
	return virtualAudioStatus(planckDriverInstalled(), names), nil
}

func virtualAudioStatus(installed bool, outputs []string) VirtualAudioStatus {
	var st VirtualAudioStatus
	if i := pickVirtualMic(outputs, ""); i >= 0 {
		st.DeviceName = outputs[i]
		for _, k := range knownVirtualMics {
			if strings.Contains(outputs[i], k) {
				st.DriverName = k
				break
			}
		}
	}
	st.Active = st.DriverName == PlanckVirtualAudio
	st.Installed = installed || st.Active
	switch {
	case st.Active:
	case st.Installed:
		st.InstallHint = vdriverInactiveHint
	default:
		st.InstallHint = vdriverInstallHint
	}
	return st
}
