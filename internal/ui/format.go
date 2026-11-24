package ui

import (
	"fmt"

	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

func pathLabel(k discovery.PathKind) string {
	switch k {
	case discovery.PathLANDirect:
		return "LAN direct"
	case discovery.PathTailscaleDirect:
		return "Tailscale direct"
	case discovery.PathTailscaleDERP:
		return "DERP relay"
	}
	return "Unknown path"
}

// pathBars is how many bars of the slice meter a path fills: the glyph,
// with the label, carries the state for readers who cannot tell the
// signal colors apart.
func pathBars(k discovery.PathKind) int {
	switch k {
	case discovery.PathLANDirect:
		return 3
	case discovery.PathTailscaleDirect:
		return 2
	}
	return 1
}

func formatMs(v float64) string { return fmt.Sprintf("%.1f", v) }

func formatMbps(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

func chromaLabel(c encode.Chroma) string {
	switch c {
	case encode.NV12:
		return "4:2:0 8-bit"
	case encode.P010:
		return "4:2:0 10-bit"
	case encode.YUV444:
		return "4:4:4 8-bit"
	case encode.YUV444P10:
		return "4:4:4 10-bit"
	}
	return ""
}

// codecLabel names the stream format briefly, as "HEVC 10-bit" or
// "AV1 4:4:4 10-bit".
func codecLabel(c encode.Codec, ch encode.Chroma) string {
	name := c.String()
	if c == 0 {
		return ""
	}
	switch {
	case ch.Full():
		return name + " " + chromaLabel(ch)
	case ch.TenBit():
		return name + " 10-bit"
	}
	return name
}

func resolutionLabel(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	return fmt.Sprintf("%d x %d", w, h)
}

// machineWord is what the hosting tooltip calls this computer.
func machineWord(goos string) string {
	if goos == "darwin" {
		return "Mac"
	}
	return "PC"
}

// derpCapMbps is the bitrate ceiling the transport applies on a DERP relay.
func derpCapMbps() int {
	return discovery.PathTailscaleDERP.RecommendedMaxBitrateKbps() / 1000
}
