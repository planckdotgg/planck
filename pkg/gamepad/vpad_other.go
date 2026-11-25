//go:build !darwin && !windows

package gamepad

func VirtualPadStatus() PadDriverStatus {
	return PadDriverStatus{InstallHint: "Virtual gamepads are not available on this host's platform."}
}

func OpenVirtualPads(PadType) (*VirtualPads, error) { return nil, ErrUnsupported }
