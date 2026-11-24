package audio

import "testing"

func newEndpointLoopbackCapturer() *WASAPICapturer { return &WASAPICapturer{endpointOnly: true} }

func newLoopbackOn(id string) *WASAPICapturer {
	return &WASAPICapturer{endpointOnly: true, deviceID: id}
}

func newMicCapturerOn(id string) *WASAPICapturer { return &WASAPICapturer{mic: true, deviceID: id} }

func inputDevices() ([]VirtualMic, error) { return endpoints(eCapture) }

// startOtherTone does nothing on Windows: process loopback excludes this
// process's children too, so the other tone must come from a process
// started outside the test.
func startOtherTone(*testing.T, float64) func() { return func() {} }
