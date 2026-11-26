package session

import (
	"github.com/planckdotgg/planck/pkg/decode/linuxdec"
	"github.com/planckdotgg/planck/pkg/defaults"
)

func probeEncoders(HostConfig) []defaults.EncoderCap { return nil }
func hostDisplays(HostConfig) []defaults.HostDisplay { return nil }
func vddCap() defaults.VDDCap                        { return defaults.VDDCap{} }

type vddManager struct{}

func (*vddManager) create(VideoParams, []byte, bool) (VirtualDisplay, error) {
	return nil, errNoBackend
}
func (*vddManager) close() error { return nil }

// clientDisplay leaves the display to the window: the desktop client
// reads its monitor from GTK (cmd/planck-client), a headless one has none.
func clientDisplay() defaults.ClientDisplay { return defaults.ClientDisplay{} }

// clientDecoders lists VA-API's and NVDEC's codecs: HEVC on either, AV1 on
// NVDEC.
func clientDecoders() []defaults.CodecCap { return linuxdec.Codecs() }

const (
	clientPresent10Bit  = false
	clientPresentHDR10  = false
	clientDirectDisplay = false
)
