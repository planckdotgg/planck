package audio

const (
	vdriverInstallHint  = "Planck Virtual Audio on Linux needs PulseAudio, or PipeWire with its pipewire-pulse service, running for this user."
	vdriverInactiveHint = vdriverInstallHint
)

// planckDriverInstalled: Planck makes its virtual device out of
// module-null-sink and module-virtual-source, which every PulseAudio and
// pipewire-pulse server has, so there is nothing to install beyond a
// running server.
func planckDriverInstalled() bool { return probePulse() == nil }
