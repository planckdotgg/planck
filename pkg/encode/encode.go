// Package encode defines the hardware video encoder contract shared by the
// vendor backends: NVENC, AMF, oneVPL (Quick Sync), and VideoToolbox.
package encode

import (
	"errors"
	"fmt"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/gpu"
)

type Codec uint8

const (
	H264 Codec = iota + 1
	HEVC
	AV1
	// PyroWave is the intra-only wavelet codec for LAN links (pkg/pyrowave).
	PyroWave
)

func (c Codec) String() string {
	switch c {
	case H264:
		return "H.264"
	case HEVC:
		return "HEVC"
	case AV1:
		return "AV1"
	case PyroWave:
		return "PyroWave"
	}
	return "unknown"
}

// Chroma is the coded sample layout of the bitstream, independent of what
// the capture surface holds.
type Chroma uint8

const (
	NV12      Chroma = iota + 1 // 8-bit 4:2:0
	P010                        // 10-bit 4:2:0, HDR10 capable
	YUV444                      // 8-bit 4:4:4
	YUV444P10                   // 10-bit 4:4:4
)

func (c Chroma) String() string {
	switch c {
	case NV12:
		return "NV12"
	case P010:
		return "P010"
	case YUV444:
		return "YUV444"
	case YUV444P10:
		return "YUV444P10"
	}
	return "unknown"
}

func (c Chroma) TenBit() bool { return c == P010 || c == YUV444P10 }
func (c Chroma) Full() bool   { return c == YUV444 || c == YUV444P10 }

// Config is what the caller asks for. Backends refuse what the hardware
// cannot do with ErrUnsupported rather than silently degrading.
type Config struct {
	Codec  Codec
	Chroma Chroma
	// Source is the surface format the encoder will be fed. Hardware fixes
	// the input bit depth and color conversion when the session opens.
	Source      capture.Format
	Width       int
	Height      int
	FPS         int
	BitrateKbps int
	// IntraRefreshPeriod > 0 replaces periodic IDR frames with a rolling
	// intra band that sweeps the picture every N frames.
	IntraRefreshPeriod int
	// Slices > 1 splits each picture so the packetizer can send the top of a
	// frame while the bottom is still being encoded.
	Slices int
	// SubFrame asks the backend to expose slices as they finish instead of
	// after the whole picture (NVENC subframe readback, VT not supported).
	SubFrame bool
	// Adapter selects the encoding GPU. Zero means the adapter that owns the
	// captured surface (zero copy); any other LUID means cross-adapter offload.
	Adapter gpu.LUID
	// Split chooses whether a GPU with two or more encode engines splits
	// each picture between them; see SplitFrame.
	Split Split
	// ChunkLen is the transport shard size PyroWave packets are framed in
	// (Session.ShardSize); PyroWave needs at least 256.
	ChunkLen int
}

// Split is a choice of split-frame encode: each picture divided between a
// GPU's encode engines finishes sooner, at about a third more bits for the
// same quality. Measured on an RTX 4080: 1440p120 encode p50 2.17 to 1.49
// ms, 1080p240 1.35 to 1.03 ms, frames 32 to 33% larger.
type Split uint8

const (
	// SplitAuto splits where it pays: see SplitFrame.
	SplitAuto Split = iota
	SplitOn
	SplitOff
)

func (s Split) String() string {
	switch s {
	case SplitOn:
		return "on"
	case SplitOff:
		return "off"
	}
	return "auto"
}

// SplitFrame reports whether the encoder should split each picture between
// its engines. SplitAuto splits at 1440 lines and up, and at 1080 lines
// from 200 fps, where one engine takes a large share of the frame interval;
// never with codec slices, which the engines do not split.
func (c Config) SplitFrame() bool {
	switch c.Split {
	case SplitOn:
		return true
	case SplitOff:
		return false
	}
	return c.Slices <= 1 && (c.Height >= 1440 || c.Height >= 1080 && c.FPS >= 200)
}

var ErrUnsupported = errors.New("encode: unsupported by this encoder")

// ErrDropped means the encoder discarded the frame to stay real time. The
// session is still usable.
var ErrDropped = errors.New("encode: frame dropped by the encoder")

func (c Config) Validate() error {
	switch {
	case c.Codec < H264 || c.Codec > PyroWave:
		return fmt.Errorf("encode: unknown codec %d", c.Codec)
	case c.Chroma < NV12 || c.Chroma > YUV444P10:
		return fmt.Errorf("encode: unknown chroma %d", c.Chroma)
	case c.Source == 0:
		return errors.New("encode: source format is required")
	case !c.Chroma.TenBit() && (c.Source == capture.FormatP010):
		return fmt.Errorf("encode: %s source cannot feed an 8-bit %s encode", c.Source, c.Chroma)
	case c.Width <= 0 || c.Height <= 0 || c.Width%2 != 0 || c.Height%2 != 0:
		return fmt.Errorf("encode: size %dx%d must be positive and even", c.Width, c.Height)
	case c.FPS <= 0:
		return fmt.Errorf("encode: fps %d must be positive", c.FPS)
	case c.BitrateKbps <= 0:
		return fmt.Errorf("encode: bitrate %d must be positive", c.BitrateKbps)
	case c.Slices < 0 || c.IntraRefreshPeriod < 0:
		return errors.New("encode: slices and intra refresh period must not be negative")
	case c.SubFrame && c.Slices < 2:
		return errors.New("encode: sub-frame output needs at least 2 slices")
	case c.Codec == PyroWave:
		return c.validatePyroWave()
	}
	return nil
}

// validatePyroWave: Chroma.Full picks 4:4:4 and Chroma.TenBit the output
// depth; the input is an RGB desktop surface. Every PyroWave picture is
// intra, so slices, sub-frame output and intra refresh do not apply.
func (c Config) validatePyroWave() error {
	switch {
	case c.Source != capture.FormatBGRA8 && c.Source != capture.FormatRGB10A2:
		return fmt.Errorf("encode: PyroWave takes BGRA8 or RGB10A2 surfaces, not %s", c.Source)
	case c.Slices > 1 || c.SubFrame || c.IntraRefreshPeriod > 0:
		return errors.New("encode: PyroWave has no codec slices, sub-frame output or intra refresh")
	case c.ChunkLen < 256:
		return fmt.Errorf("encode: PyroWave chunk length %d below 256", c.ChunkLen)
	}
	return nil
}

// Packet is one encoded picture. Data aliases the dst buffer given to Encode.
type Packet struct {
	Data     []byte
	Keyframe bool
	// SliceEnds holds the end offset of each slice in Data when the backend
	// reports slice boundaries.
	SliceEnds []int
	PTS       int64
	// Critical is the number of leading slices whose loss costs more than
	// local blur (PyroWave's coarse levels), which deserve more parity.
	Critical int
}

// Caps are what the opened device reports, not what the API documents.
type Caps struct {
	Adapter      gpu.Adapter
	Codecs       []Codec
	Chroma       map[Codec][]Chroma
	MaxWidth     int
	MaxHeight    int
	IntraRefresh bool
	SubFrame     bool
	// RefInvalidation means the encoder implements RefInvalidator.
	RefInvalidation bool
	Engines         int
}

// Encoder turns captured GPU surfaces into packets.
type Encoder interface {
	Caps() Caps
	// Encode consumes frame synchronously and appends the bitstream to dst.
	Encode(frame capture.Frame, forceKey bool, dst []byte) (Packet, error)
	SetBitrate(kbps int) error
	Close() error
}

// SliceEncoder is an Encoder with sub-frame output (Config.SubFrame): it
// hands each codec slice on as soon as the hardware finished it, so the
// first slices travel while the rest of the picture is still encoding.
type SliceEncoder interface {
	// EncodeSlices is Encode that also calls emit with each slice, in
	// order, as it becomes readable: its index, its bytes (a part of the
	// Packet's Data, valid until Encode or EncodeSlices runs again) and
	// whether the picture is a keyframe. An error from emit ends the
	// encode with that error.
	EncodeSlices(frame capture.Frame, forceKey bool, dst []byte, emit func(index int, slice []byte, keyframe bool) error) (Packet, error)
}

// RefInvalidator is an Encoder that can stop predicting from pictures the
// receiver lost, so the stream recovers without a keyframe.
type RefInvalidator interface {
	// InvalidateRefs marks the picture with this Packet.PTS, and every later
	// one, as unusable references.
	InvalidateRefs(pts int64) error
}
