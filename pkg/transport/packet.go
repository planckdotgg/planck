package transport

import (
	"encoding/binary"
	"errors"
)

// Packet kinds carried in the first plaintext byte.
const (
	KindMedia byte = 1
	KindAck   byte = 2
	KindPing  byte = 3
	KindPong  byte = 4
)

// MediaHeaderLen is the size of the plaintext header that precedes each shard.
const MediaHeaderLen = 20

// MediaHeader describes one FEC shard of one slice of one frame.
type MediaHeader struct {
	SliceIndex   uint8
	SliceCount   uint8
	ShardIndex   uint8
	DataShards   uint8
	ParityShards uint8
	ShardLen     uint16
	FrameID      uint32
	SliceLen     uint32
	FrameShards  uint16
}

var ErrBadHeader = errors.New("transport: bad media header")

func (h *MediaHeader) Put(b []byte) {
	_ = b[MediaHeaderLen-1]
	b[0] = KindMedia
	b[1] = h.SliceIndex
	b[2] = h.SliceCount
	b[3] = h.ShardIndex
	b[4] = h.DataShards
	b[5] = h.ParityShards
	binary.BigEndian.PutUint16(b[6:], h.ShardLen)
	binary.BigEndian.PutUint32(b[8:], h.FrameID)
	binary.BigEndian.PutUint32(b[12:], h.SliceLen)
	binary.BigEndian.PutUint16(b[16:], h.FrameShards)
	b[18], b[19] = 0, 0
}

// Parse decodes a media header and validates it against the payload length.
func (h *MediaHeader) Parse(b []byte) error {
	if len(b) < MediaHeaderLen || b[0] != KindMedia {
		return ErrBadHeader
	}
	h.SliceIndex = b[1]
	h.SliceCount = b[2]
	h.ShardIndex = b[3]
	h.DataShards = b[4]
	h.ParityShards = b[5]
	h.ShardLen = binary.BigEndian.Uint16(b[6:])
	h.FrameID = binary.BigEndian.Uint32(b[8:])
	h.SliceLen = binary.BigEndian.Uint32(b[12:])
	h.FrameShards = binary.BigEndian.Uint16(b[16:])
	total := int(h.DataShards) + int(h.ParityShards)
	switch {
	case h.SliceCount == 0 || h.SliceIndex >= h.SliceCount || int(h.SliceCount) > MaxSlices,
		h.DataShards == 0 || int(h.ShardIndex) >= total || total > MaxShardsPerSlice,
		int(h.ShardLen) != len(b)-MediaHeaderLen,
		int(h.SliceLen) > int(h.DataShards)*int(h.ShardLen),
		int(h.SliceLen) <= (int(h.DataShards)-1)*int(h.ShardLen):
		return ErrBadHeader
	}
	return nil
}

// Ack is the receiver's per-frame report. It also carries the final shard
// counts of an older frame so the sender can estimate loss for adaptive FEC.
type Ack struct {
	FrameID          uint32
	FirstSliceLeadUs uint32
	FinalFrameID     uint32
	FinalRecvShards  uint16
	FinalSentShards  uint16
	FinalFrameOK     bool
	HasFinal         bool
}

const AckLen = 20

func (a *Ack) Put(b []byte) []byte {
	b = b[:AckLen]
	b[0] = KindAck
	b[1] = 0
	if a.HasFinal {
		b[1] |= 1
	}
	if a.FinalFrameOK {
		b[1] |= 2
	}
	b[2], b[3] = 0, 0
	binary.BigEndian.PutUint32(b[4:], a.FrameID)
	binary.BigEndian.PutUint32(b[8:], a.FirstSliceLeadUs)
	binary.BigEndian.PutUint32(b[12:], a.FinalFrameID)
	binary.BigEndian.PutUint16(b[16:], a.FinalRecvShards)
	binary.BigEndian.PutUint16(b[18:], a.FinalSentShards)
	return b
}

func (a *Ack) Parse(b []byte) error {
	if len(b) != AckLen || b[0] != KindAck {
		return ErrBadHeader
	}
	a.HasFinal = b[1]&1 != 0
	a.FinalFrameOK = b[1]&2 != 0
	a.FrameID = binary.BigEndian.Uint32(b[4:])
	a.FirstSliceLeadUs = binary.BigEndian.Uint32(b[8:])
	a.FinalFrameID = binary.BigEndian.Uint32(b[12:])
	a.FinalRecvShards = binary.BigEndian.Uint16(b[16:])
	a.FinalSentShards = binary.BigEndian.Uint16(b[18:])
	return nil
}

// PingLen is the size of ping and pong packets: kind, pad, sequence, and the
// sender's timestamp echoed back unchanged.
const PingLen = 16

func PutPing(b []byte, kind byte, seq uint32, ts int64) []byte {
	b = b[:PingLen]
	b[0], b[1], b[2], b[3] = kind, 0, 0, 0
	binary.BigEndian.PutUint32(b[4:], seq)
	binary.BigEndian.PutUint64(b[8:], uint64(ts))
	return b
}

func ParsePing(b []byte) (seq uint32, ts int64, ok bool) {
	if len(b) != PingLen || (b[0] != KindPing && b[0] != KindPong) {
		return 0, 0, false
	}
	return binary.BigEndian.Uint32(b[4:]), int64(binary.BigEndian.Uint64(b[8:])), true
}
