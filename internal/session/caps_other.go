//go:build !darwin && !windows

package session

import "github.com/planckdotgg/planck/pkg/defaults"

func probeEncoders(HostConfig) []defaults.EncoderCap { return nil }
func hostDisplays(HostConfig) []defaults.HostDisplay { return nil }
func vddCap() defaults.VDDCap                        { return defaults.VDDCap{} }
func clientDisplay() defaults.ClientDisplay          { return defaults.ClientDisplay{} }
func clientDecoders() []defaults.CodecCap            { return nil }

type vddManager struct{}

func (*vddManager) create(VideoParams, []byte, bool) (VirtualDisplay, error) {
	return nil, errNoBackend
}
func (*vddManager) close() error { return nil }

const (
	clientPresent10Bit  = false
	clientPresentHDR10  = false
	clientDirectDisplay = false
)
