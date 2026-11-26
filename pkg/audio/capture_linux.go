package audio

// CanExcludeProcesses is false: a sink's monitor carries every stream on
// it, Planck's own and a voice chat's included.
const CanExcludeProcesses = false

// NewCapturer records what the host plays: the monitor of the default
// output device, or of opts.DeviceID when that device is there. Planck's
// own sound is not left out, and ExcludePIDs are not honoured.
func NewCapturer(opts CaptureOptions) (Capturer, error) {
	if err := probePulse(); err != nil {
		return nil, err
	}
	return &pulseCapturer{deviceID: presentDevice(OutputDevices, opts.DeviceID)}, nil
}
