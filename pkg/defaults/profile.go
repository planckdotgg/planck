package defaults

import "github.com/planckdotgg/planck/pkg/gpu"

type Source uint8

const (
	SourceAuto     Source = iota + 1
	SourceUser            // client override applied
	SourceHost            // host Forced applied
	SourceFallback        // override asked for something impossible; auto value used
)

type Decision struct {
	Knob   string `json:"knob"` // "codec", "fps", ...
	Value  string `json:"value"`
	Source Source `json:"source"`
	Why    string `json:"why"` // one line for the UI: "serialization 9.1 ms > 1.7 ms budget"
}

type FECProfile struct {
	Adaptive              bool `json:"adaptive"`
	MinParityPercent      int  `json:"minParityPercent"`
	KeyframeParityPercent int  `json:"keyframeParityPercent"`
	CriticalParityPercent int  `json:"criticalParityPercent"` // PyroWave critical slices; at least 2 shards
}

type ResolvedProfile struct {
	Route       RouteKind `json:"route"`
	MaxDatagram int       `json:"maxDatagram"` // 1452 or 1152
	LinkMbps    int       `json:"linkMbps"`    // min of both ends, 0 = unknown
	Wired       bool      `json:"wired"`       // both ends

	EncodeAdapter  gpu.Adapter `json:"encodeAdapter"`
	EncoderAPI     string      `json:"encoderAPI"`
	VirtualDisplay bool        `json:"virtualDisplay"`
	CaptureDisplay string      `json:"captureDisplay,omitempty"` // physical capture only

	Codec     Codec `json:"codec"`
	Chroma444 bool  `json:"chroma444"`
	BitDepth  int   `json:"bitDepth"` // 8 | 10
	Width     int   `json:"width"`
	Height    int   `json:"height"`
	FPS       int   `json:"fps"`

	BitrateKbps    int `json:"bitrateKbps"`    // start
	MaxBitrateKbps int `json:"maxBitrateKbps"` // ratectl ceiling

	Slices   int  `json:"slices"` // codec slices; PyroWave: 0 (its packetizer slices itself)
	SubFrame bool `json:"subFrame"`
	Split    bool `json:"split"` // NVENC 2-engine split

	DirectDisplay bool `json:"directDisplay"` // allowed; the client applies it only in full screen with the drawer closed
	VSync         bool `json:"vsync"`

	FEC FECProfile `json:"fec"`

	Decisions []Decision `json:"decisions"` // in resolve order
	Warnings  []string   `json:"warnings"`  // overrides that fell back
}
