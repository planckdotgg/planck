// Package defaults picks a session's stream settings from what the host,
// the client and the route between them can do, with user overrides on
// top. Resolve is pure: no I/O, no clock, no map iteration.
package defaults

import "github.com/planckdotgg/planck/pkg/gpu"

// Codec values equal pkg/wire Codec values.
type Codec uint8

const (
	CodecH264 Codec = iota + 1
	CodecHEVC
	CodecAV1
	CodecPyroWave
)

func (c Codec) String() string {
	switch c {
	case CodecH264:
		return "h264"
	case CodecHEVC:
		return "hevc"
	case CodecAV1:
		return "av1"
	case CodecPyroWave:
		return "pyrowave"
	}
	return "unknown"
}

type RouteKind uint8

const (
	RouteLAN             RouteKind = iota + 1 // discovery.PathLANDirect
	RouteTailscaleDirect                      // discovery.PathTailscaleDirect
	RouteWAN                                  // discovery.PathTailscaleDERP, or any other path
)

func (k RouteKind) String() string {
	switch k {
	case RouteLAN:
		return "lan"
	case RouteTailscaleDirect:
		return "tailscale-direct"
	}
	return "wan"
}

// Link is one end's interface toward the peer.
type Link struct {
	Wired     bool `json:"wired"`     // Ethernet or Thunderbolt Ethernet, not Wi-Fi
	SpeedMbps int  `json:"speedMbps"` // 0 = unknown
}

type CodecCap struct {
	Codec     Codec `json:"codec"`
	TenBit    bool  `json:"tenBit"`
	Chroma444 bool  `json:"chroma444"` // at the depths TenBit allows
	MaxWidth  int   `json:"maxWidth"`  // 0 = no limit
	MaxHeight int   `json:"maxHeight"` // 0 = no limit
}

type EncoderCap struct {
	Adapter  gpu.Adapter `json:"adapter"`
	API      string      `json:"api"`     // "NVENC" | "AMF" | "oneVPL" | "VideoToolbox" | "PyroWave"
	Codecs   []CodecCap  `json:"codecs"`  // sorted by Codec
	Engines  int         `json:"engines"` // hardware encode engines (NVENC split needs >= 2)
	SubFrame bool        `json:"subFrame"`
	// MaxPixelRate is width*height*fps the encoder sustains in real time.
	// The startup probe measures it. The seed table in resolve.go is used
	// until then.
	MaxPixelRate int64 `json:"maxPixelRate"`
	// EncodeP50Micros is the startup probe's 1080p p50 encode time. 0 = not measured.
	EncodeP50Micros int  `json:"encodeP50Micros"`
	DrivesDisplay   bool `json:"drivesDisplay"` // has an active physical output
}

const (
	APINVENC        = "NVENC"
	APIAMF          = "AMF"
	APIOneVPL       = "oneVPL"
	APIVAAPI        = "VA-API"
	APIVideoToolbox = "VideoToolbox"
	APIPyroWave     = "PyroWave"
)

type HostDisplay struct {
	ID string `json:"id"` // GDI name or CGDirectDisplayID
	// Name is what the host's OS calls the display, "" when it has no name.
	Name      string   `json:"name,omitempty"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	RefreshHz int      `json:"refreshHz"`
	Adapter   gpu.LUID `json:"adapter"`
	Primary   bool     `json:"primary"`
	// HDR: the desktop on this display is in HDR10 (BT.2020 PQ), which
	// capture can take as 10-bit.
	HDR bool `json:"hdr,omitempty"`
}

type VDDCap struct {
	Available           bool   `json:"available"` // driver present AND openable by this user
	Protocol            string `json:"protocol"`  // "0.2.1"
	CanSetRenderAdapter bool   `json:"canSetRenderAdapter"`
	RegistryGPU         string `json:"registryGPU"` // HKLM gpuName, information only
}

type HostCapabilities struct {
	OS       string        `json:"os"`
	Encoders []EncoderCap  `json:"encoders"` // one per adapter with a working backend; PyroWave is its own entry per adapter
	Displays []HostDisplay `json:"displays"`
	// PinnedDisplay is HostConfig.Display. If it is set, physical capture wins over VDD.
	PinnedDisplay string        `json:"pinnedDisplay,omitempty"`
	VDD           VDDCap        `json:"vdd"`
	Link          Link          `json:"link"`
	Forced        UserOverrides `json:"forced"` // host owner's policy; beats the client
}

type ClientDisplay struct {
	WidthPx  int     `json:"widthPx"` // native backing pixels (3024x1964 on a 14" MacBook Pro)
	HeightPx int     `json:"heightPx"`
	Scale    float64 `json:"scale"` // backing scale factor
	MaxHz    int     `json:"maxHz"` // ProMotion: 120
	MinHz    int     `json:"minHz"` // ProMotion: 24
	HDR      bool    `json:"hdr"`
	// ViewWidthPx and ViewHeightPx are the stream window's size in pixels;
	// 0 = no window, as a headless client. A virtual display with
	// Resolution auto takes this size.
	ViewWidthPx  int `json:"viewWidthPx,omitempty"`
	ViewHeightPx int `json:"viewHeightPx,omitempty"`
}

type ClientCapabilities struct {
	OS       string        `json:"os"`
	Display  ClientDisplay `json:"display"`
	Decoders []CodecCap    `json:"decoders"` // hardware only, from the startup probe
	// Present10Bit: the client surface can show BGR10A2 (WS2).
	Present10Bit  bool `json:"present10Bit"`
	DirectDisplay bool `json:"directDisplay"` // the platform has a full-screen direct path (macOS WS2)
	// PresentHDR10: the client presents BT.2020 PQ pictures as HDR on a
	// display whose HDR is set (macOS EDR).
	PresentHDR10 bool `json:"presentHDR10,omitempty"`
	Link         Link `json:"link"`
}

type RouteInfo struct {
	Kind RouteKind `json:"kind"`
	// LANDatagram: both ends agreed on wire.MaxLANDatagram (1452).
	LANDatagram bool `json:"lanDatagram"`
	RTTMicros   int  `json:"rttMicros"`
	ProbedMbps  int  `json:"probedMbps"` // 0 = not probed
	// ReceiveQueue is the client's path below its socket as calibration
	// measured it on this route; nil when not calibrated.
	ReceiveQueue *ReceiveQueue `json:"receiveQueue,omitempty"`
}

// ReceiveQueue is a receiver that keeps Bytes of a burst and drains the
// rest at no more than DrainMbps (transport.ReceiveQueue). Bytes 0 is a
// receiver calibration saw drop nothing of any burst it sent.
type ReceiveQueue struct {
	Bytes     int `json:"bytes"`
	DrainMbps int `json:"drainMbps"`
}
