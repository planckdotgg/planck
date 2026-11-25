//go:build darwin

package gamepad

func VirtualPadStatus() PadDriverStatus {
	return PadDriverStatus{InstallHint: "Virtual gamepads are not available on macOS hosts (requires DriverKit)."}
}

func OpenVirtualPads(PadType) (*VirtualPads, error) { return nil, ErrUnsupported }
