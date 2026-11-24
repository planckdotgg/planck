package wire

import (
	"encoding/binary"
	"errors"
)

// MaxDatagram is the largest media UDP payload Planck sends by default: it
// fits Tailscale's 1280-byte tunnel MTU after IPv6, UDP and WireGuard
// overhead. Every sealed media datagram, header and AEAD tag included, must
// fit within it.
const MaxDatagram = 1152

// MaxLANDatagram is the media UDP payload on a LAN path with a 1500-byte
// MTU, after IPv6 and UDP headers: 22% fewer datagrams per frame than
// MaxDatagram. Hosts use it only when both ends agree (FeatureLANDatagram).
const MaxLANDatagram = 1452

// Media datagram kinds, carried in the first plaintext byte.
const (
	KindVideo    byte = 0x11
	KindAudio    byte = 0x12
	KindInput    byte = 0x13
	KindRumble   byte = 0x14
	KindPing     byte = 0x15
	KindPong     byte = 0x16
	KindFeedback byte = 0x17
)

// Video shard limits. A frame has up to MaxSlices codec slices; a slice is
// split into up to MaxBlocks Reed-Solomon blocks; a block has at most
// MaxShards data plus parity shards.
const (
	MaxSlices = 16
	MaxBlocks = 32
	MaxShards = 255
)

var ErrBadDatagram = errors.New("wire: bad media datagram")

const flagKeyframe = 1

// VideoHeaderLen is the plaintext header in front of every video shard.
const VideoHeaderLen = 36

// VideoInfo is the per-frame part of a video shard header.
type VideoInfo struct {
	FrameID  uint32
	Keyframe bool
	// CaptureTS and EncodeTS are the host's monotonic clock in nanoseconds
	// when the frame was captured and when its encode finished. The client
	// maps them to its clock with the ping/pong offset.
	CaptureTS  int64
	EncodeTS   int64
	SliceCount uint8
}

// VideoHeader describes one shard of one Reed-Solomon block of one slice.
type VideoHeader struct {
	VideoInfo
	SliceIndex   uint8
	BlockIndex   uint8
	BlockCount   uint8
	ShardIndex   uint8
	DataShards   uint8
	ParityShards uint8
	ShardLen     uint16
	// BlockLen is the number of slice bytes the block's data shards carry;
	// the last data shard is zero-padded to ShardLen.
	BlockLen uint32
}

func (h *VideoHeader) Put(b []byte) {
	_ = b[VideoHeaderLen-1]
	b[0] = KindVideo
	b[1] = 0
	if h.Keyframe {
		b[1] = flagKeyframe
	}
	b[2] = h.SliceIndex
	b[3] = h.SliceCount
	b[4] = h.BlockIndex
	b[5] = h.BlockCount
	b[6] = h.ShardIndex
	b[7] = h.DataShards
	b[8] = h.ParityShards
	b[9] = 0
	binary.BigEndian.PutUint16(b[10:], h.ShardLen)
	binary.BigEndian.PutUint32(b[12:], h.FrameID)
	binary.BigEndian.PutUint32(b[16:], h.BlockLen)
	binary.BigEndian.PutUint64(b[20:], uint64(h.CaptureTS))
	binary.BigEndian.PutUint64(b[28:], uint64(h.EncodeTS))
}

// Parse decodes a video header from a whole shard datagram and checks it
// against the datagram length.
func (h *VideoHeader) Parse(b []byte) error {
	if len(b) < VideoHeaderLen || b[0] != KindVideo {
		return ErrBadDatagram
	}
	h.Keyframe = b[1]&flagKeyframe != 0
	h.SliceIndex = b[2]
	h.SliceCount = b[3]
	h.BlockIndex = b[4]
	h.BlockCount = b[5]
	h.ShardIndex = b[6]
	h.DataShards = b[7]
	h.ParityShards = b[8]
	h.ShardLen = binary.BigEndian.Uint16(b[10:])
	h.FrameID = binary.BigEndian.Uint32(b[12:])
	h.BlockLen = binary.BigEndian.Uint32(b[16:])
	h.CaptureTS = int64(binary.BigEndian.Uint64(b[20:]))
	h.EncodeTS = int64(binary.BigEndian.Uint64(b[28:]))
	total := int(h.DataShards) + int(h.ParityShards)
	switch {
	case h.SliceCount == 0 || h.SliceIndex >= h.SliceCount || h.SliceCount > MaxSlices,
		h.BlockCount == 0 || h.BlockIndex >= h.BlockCount || h.BlockCount > MaxBlocks,
		h.DataShards == 0 || int(h.ShardIndex) >= total || total > MaxShards,
		h.ShardLen == 0 || int(h.ShardLen) != len(b)-VideoHeaderLen,
		int(h.BlockLen) > int(h.DataShards)*int(h.ShardLen),
		int(h.BlockLen) <= (int(h.DataShards)-1)*int(h.ShardLen):
		return ErrBadDatagram
	}
	return nil
}

// AudioHeaderLen precedes one audio packet.
const AudioHeaderLen = 16

// AudioFlow says which stream an audio datagram belongs to.
type AudioFlow uint8

const (
	// FlowDesktop is the host's desktop audio, host to client.
	FlowDesktop AudioFlow = iota
	// FlowMic is the client's microphone, client to host.
	FlowMic
)

type AudioCodec uint8

const (
	AudioOpus AudioCodec = iota
	// AudioPCM16 is raw little-endian int16 PCM.
	AudioPCM16
)

// AudioHeader describes one audio packet. Hosts before the mic flow wrote
// zeros where Flow, Codec and FrameQuarterMs go: desktop Opus at the
// default frame length.
type AudioHeader struct {
	Flow  AudioFlow
	Codec AudioCodec
	// FrameQuarterMs is the packet's frame length in quarter milliseconds;
	// zero means 10 ms.
	FrameQuarterMs uint8
	Seq            uint32
	Timestamp      int64
}

func (h *AudioHeader) Put(b []byte) {
	_ = b[AudioHeaderLen-1]
	b[0], b[1], b[2], b[3] = KindAudio, byte(h.Flow), byte(h.Codec), h.FrameQuarterMs
	binary.BigEndian.PutUint32(b[4:], h.Seq)
	binary.BigEndian.PutUint64(b[8:], uint64(h.Timestamp))
}

// ParseAudio returns the header and the payload, which aliases b.
func ParseAudio(b []byte) (AudioHeader, []byte, error) {
	if len(b) < AudioHeaderLen || b[0] != KindAudio {
		return AudioHeader{}, nil, ErrBadDatagram
	}
	h := AudioHeader{
		Flow:           AudioFlow(b[1]),
		Codec:          AudioCodec(b[2]),
		FrameQuarterMs: b[3],
		Seq:            binary.BigEndian.Uint32(b[4:]),
		Timestamp:      int64(binary.BigEndian.Uint64(b[8:])),
	}
	return h, b[AudioHeaderLen:], nil
}

type InputType uint8

const (
	InputKey InputType = iota + 1
	InputMouseRel
	InputMouseAbs
	InputMouseButton
	InputScroll
	InputGamepad
)

// InputLen is the fixed size of an input datagram.
const InputLen = 32

// Input is one input event from the client. Which fields count depends on
// Type: Key uses Code (USB HID usage), Mods and Down; MouseRel and Scroll use
// X and Y as deltas; MouseAbs uses X and Y in 0..65535 of the video frame;
// MouseButton uses Button and Down; Gamepad uses Pad and the stick, trigger
// and button state, which is absolute so a lost packet is healed by the next.
type Input struct {
	Type      InputType
	Seq       uint32
	Timestamp int64

	Code   uint16
	Mods   uint16
	Down   bool
	X, Y   int32
	Button uint8

	Pad            uint8
	Buttons        uint32
	LT, RT         uint8
	LX, LY, RX, RY int16
}

func (in *Input) Put(b []byte) []byte {
	b = b[:InputLen]
	clear(b)
	b[0] = KindInput
	b[1] = byte(in.Type)
	b[3] = boolByte(in.Down)
	binary.BigEndian.PutUint32(b[4:], in.Seq)
	binary.BigEndian.PutUint64(b[8:], uint64(in.Timestamp))
	p := b[16:]
	switch in.Type {
	case InputKey:
		binary.BigEndian.PutUint16(p, in.Code)
		binary.BigEndian.PutUint16(p[2:], in.Mods)
	case InputMouseRel, InputMouseAbs, InputScroll:
		binary.BigEndian.PutUint32(p, uint32(in.X))
		binary.BigEndian.PutUint32(p[4:], uint32(in.Y))
	case InputMouseButton:
		b[2] = in.Button
	case InputGamepad:
		b[2] = in.Pad
		binary.BigEndian.PutUint32(p, in.Buttons)
		p[4], p[5] = in.LT, in.RT
		binary.BigEndian.PutUint16(p[6:], uint16(in.LX))
		binary.BigEndian.PutUint16(p[8:], uint16(in.LY))
		binary.BigEndian.PutUint16(p[10:], uint16(in.RX))
		binary.BigEndian.PutUint16(p[12:], uint16(in.RY))
	}
	return b
}

func (in *Input) Parse(b []byte) error {
	if len(b) != InputLen || b[0] != KindInput {
		return ErrBadDatagram
	}
	*in = Input{
		Type:      InputType(b[1]),
		Down:      b[3] != 0,
		Seq:       binary.BigEndian.Uint32(b[4:]),
		Timestamp: int64(binary.BigEndian.Uint64(b[8:])),
	}
	p := b[16:]
	switch in.Type {
	case InputKey:
		in.Code = binary.BigEndian.Uint16(p)
		in.Mods = binary.BigEndian.Uint16(p[2:])
	case InputMouseRel, InputMouseAbs, InputScroll:
		in.X = int32(binary.BigEndian.Uint32(p))
		in.Y = int32(binary.BigEndian.Uint32(p[4:]))
	case InputMouseButton:
		in.Button = b[2]
	case InputGamepad:
		in.Pad = b[2]
		in.Buttons = binary.BigEndian.Uint32(p)
		in.LT, in.RT = p[4], p[5]
		in.LX = int16(binary.BigEndian.Uint16(p[6:]))
		in.LY = int16(binary.BigEndian.Uint16(p[8:]))
		in.RX = int16(binary.BigEndian.Uint16(p[10:]))
		in.RY = int16(binary.BigEndian.Uint16(p[12:]))
	default:
		return ErrBadDatagram
	}
	return nil
}

// RumbleLen is the fixed size of a rumble datagram.
const RumbleLen = 12

// Rumble drives a client gamepad's motors; strengths are 0..65535.
type Rumble struct {
	Pad        uint8
	Seq        uint32
	Low, High  uint16
	DurationMs uint16
}

func (r *Rumble) Put(b []byte) []byte {
	b = b[:RumbleLen]
	b[0], b[1] = KindRumble, r.Pad
	binary.BigEndian.PutUint16(b[2:], r.Low)
	binary.BigEndian.PutUint16(b[4:], r.High)
	binary.BigEndian.PutUint16(b[6:], r.DurationMs)
	binary.BigEndian.PutUint32(b[8:], r.Seq)
	return b
}

func (r *Rumble) Parse(b []byte) error {
	if len(b) != RumbleLen || b[0] != KindRumble {
		return ErrBadDatagram
	}
	r.Pad = b[1]
	r.Low = binary.BigEndian.Uint16(b[2:])
	r.High = binary.BigEndian.Uint16(b[4:])
	r.DurationMs = binary.BigEndian.Uint16(b[6:])
	r.Seq = binary.BigEndian.Uint32(b[8:])
	return nil
}

const (
	PingLen = 16
	PongLen = 32
)

// Ping carries the sender's clock; Pong echoes it with the responder's
// receive time (T1) and send time (T2), NTP style.
type Ping struct {
	Seq uint32
	T0  int64
}

type Pong struct {
	Seq        uint32
	T0, T1, T2 int64
}

func (p *Ping) Put(b []byte) []byte {
	b = b[:PingLen]
	b[0], b[1], b[2], b[3] = KindPing, 0, 0, 0
	binary.BigEndian.PutUint32(b[4:], p.Seq)
	binary.BigEndian.PutUint64(b[8:], uint64(p.T0))
	return b
}

func (p *Ping) Parse(b []byte) error {
	if len(b) != PingLen || b[0] != KindPing {
		return ErrBadDatagram
	}
	p.Seq = binary.BigEndian.Uint32(b[4:])
	p.T0 = int64(binary.BigEndian.Uint64(b[8:]))
	return nil
}

func (p *Pong) Put(b []byte) []byte {
	b = b[:PongLen]
	b[0], b[1], b[2], b[3] = KindPong, 0, 0, 0
	binary.BigEndian.PutUint32(b[4:], p.Seq)
	binary.BigEndian.PutUint64(b[8:], uint64(p.T0))
	binary.BigEndian.PutUint64(b[16:], uint64(p.T1))
	binary.BigEndian.PutUint64(b[24:], uint64(p.T2))
	return b
}

func (p *Pong) Parse(b []byte) error {
	if len(b) != PongLen || b[0] != KindPong {
		return ErrBadDatagram
	}
	p.Seq = binary.BigEndian.Uint32(b[4:])
	p.T0 = int64(binary.BigEndian.Uint64(b[8:]))
	p.T1 = int64(binary.BigEndian.Uint64(b[16:]))
	p.T2 = int64(binary.BigEndian.Uint64(b[24:]))
	return nil
}

// FeedbackLen is the fixed size of a feedback datagram.
const FeedbackLen = 28

// Feedback is the client's report for one fully received video frame. It
// also carries the final shard counts of the most recent frame that left the
// client's reassembly window, which the host uses as its loss signal.
type Feedback struct {
	FrameID uint32
	// ArrivalTS is the client's monotonic clock when the frame completed.
	ArrivalTS int64
	Bytes     uint32

	HasFinal     bool
	FinalOK      bool
	FinalFrameID uint32
	FinalRecv    uint16
	FinalSent    uint16
}

func (f *Feedback) Put(b []byte) []byte {
	b = b[:FeedbackLen]
	b[0], b[1], b[2], b[3] = KindFeedback, 0, 0, 0
	if f.HasFinal {
		b[1] |= 1
	}
	if f.FinalOK {
		b[1] |= 2
	}
	binary.BigEndian.PutUint32(b[4:], f.FrameID)
	binary.BigEndian.PutUint64(b[8:], uint64(f.ArrivalTS))
	binary.BigEndian.PutUint32(b[16:], f.Bytes)
	binary.BigEndian.PutUint32(b[20:], f.FinalFrameID)
	binary.BigEndian.PutUint16(b[24:], f.FinalRecv)
	binary.BigEndian.PutUint16(b[26:], f.FinalSent)
	return b
}

func (f *Feedback) Parse(b []byte) error {
	if len(b) != FeedbackLen || b[0] != KindFeedback {
		return ErrBadDatagram
	}
	f.HasFinal = b[1]&1 != 0
	f.FinalOK = b[1]&2 != 0
	f.FrameID = binary.BigEndian.Uint32(b[4:])
	f.ArrivalTS = int64(binary.BigEndian.Uint64(b[8:]))
	f.Bytes = binary.BigEndian.Uint32(b[16:])
	f.FinalFrameID = binary.BigEndian.Uint32(b[20:])
	f.FinalRecv = binary.BigEndian.Uint16(b[24:])
	f.FinalSent = binary.BigEndian.Uint16(b[26:])
	return nil
}
