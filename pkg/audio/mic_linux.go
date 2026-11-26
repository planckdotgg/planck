package audio

// NewMicCapturer records the default microphone.
func NewMicCapturer() (MicCapturer, error) { return NewMicCapturerOn("") }

// NewMicCapturerOn records the input device with this Device ID, or the
// default one when id is empty or names a device that is gone.
func NewMicCapturerOn(id string) (MicCapturer, error) {
	if err := probePulse(); err != nil {
		return nil, err
	}
	return &pulseCapturer{mic: true, deviceID: presentDevice(InputDevices, id)}, nil
}
