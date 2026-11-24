package pyrowave

import (
	"encoding/binary"
	"errors"
)

// Record sizes in 32-bit words.
const (
	HeaderWords = 2
	// MaxRecordWords is what the 12-bit payload_words field can say.
	MaxRecordWords = 1<<12 - 1
	paddingMarker  = 0xFFFFFFFF
)

// Sequence header values.
const (
	ChromaRes420 = 0
	ChromaRes444 = 1

	PrimariesSRGB   = 0
	PrimariesBT2020 = 1
	TransferSRGB    = 0
	TransferPQ      = 1
	MatrixBT709     = 0
	MatrixBT2020    = 1
	RangeFull       = 0
	RangeLimited    = 1
	SitingCenter    = 0
	SitingLeft      = 1
)

// BlockHeader is the 8-byte header of a 32x32 block record.
type BlockHeader struct {
	Ballot       uint16
	PayloadWords int
	Sequence     uint8
	QuantCode    uint8
	BlockIndex   int
}

func (h BlockHeader) Put(b []byte) {
	binary.LittleEndian.PutUint32(b, uint32(h.Ballot)|uint32(h.PayloadWords)<<16|uint32(h.Sequence&7)<<28)
	binary.LittleEndian.PutUint32(b[4:], uint32(h.QuantCode)|uint32(h.BlockIndex)<<8)
}

func ParseBlockHeader(b []byte) BlockHeader {
	w0 := binary.LittleEndian.Uint32(b)
	w1 := binary.LittleEndian.Uint32(b[4:])
	return BlockHeader{
		Ballot:       uint16(w0),
		PayloadWords: int(w0 >> 16 & 0xfff),
		Sequence:     uint8(w0 >> 28 & 7),
		QuantCode:    uint8(w1),
		BlockIndex:   int(w1 >> 8),
	}
}

// SequenceHeader is the start-of-frame record (extended = 1, code = 0).
type SequenceHeader struct {
	Width, Height int
	Sequence      uint8
	TotalBlocks   int
	Chroma        uint8
	Primaries     uint8
	Transfer      uint8
	Matrix        uint8
	Range         uint8
	Siting        uint8
}

func (s SequenceHeader) Put(b []byte) {
	binary.LittleEndian.PutUint32(b, uint32(s.Width-1)&0x3fff|(uint32(s.Height-1)&0x3fff)<<14|uint32(s.Sequence&7)<<28|1<<31)
	binary.LittleEndian.PutUint32(b[4:], uint32(s.TotalBlocks)&0xffffff|
		uint32(s.Chroma&1)<<26|uint32(s.Primaries&1)<<27|uint32(s.Transfer&1)<<28|
		uint32(s.Matrix&1)<<29|uint32(s.Range&1)<<30|uint32(s.Siting&1)<<31)
}

func ParseSequenceHeader(b []byte) (SequenceHeader, bool) {
	w0 := binary.LittleEndian.Uint32(b)
	w1 := binary.LittleEndian.Uint32(b[4:])
	if w0>>31 == 0 || w1>>24&3 != 0 {
		return SequenceHeader{}, false
	}
	return SequenceHeader{
		Width:       int(w0&0x3fff) + 1,
		Height:      int(w0>>14&0x3fff) + 1,
		Sequence:    uint8(w0 >> 28 & 7),
		TotalBlocks: int(w1 & 0xffffff),
		Chroma:      uint8(w1 >> 26 & 1),
		Primaries:   uint8(w1 >> 27 & 1),
		Transfer:    uint8(w1 >> 28 & 1),
		Matrix:      uint8(w1 >> 29 & 1),
		Range:       uint8(w1 >> 30 & 1),
		Siting:      uint8(w1 >> 31 & 1),
	}, true
}

// RecordKind says what a record in a word stream is.
type RecordKind uint8

const (
	RecordEnd RecordKind = iota // a zero word: the rest of the chunk is empty
	RecordBlock
	RecordSequence
	RecordPadding
)

var (
	ErrTruncated = errors.New("pyrowave: record runs past its chunk")
	ErrBadRecord = errors.New("pyrowave: malformed record")
)

// NextRecord reads the record at the start of b (word aligned, little
// endian) and returns its kind and length in bytes.
func NextRecord(b []byte) (RecordKind, int, error) {
	if len(b) < 4 {
		return RecordEnd, 0, nil
	}
	w0 := binary.LittleEndian.Uint32(b)
	switch {
	case w0 == 0:
		return RecordEnd, 0, nil
	case w0 == paddingMarker:
		if len(b) < 8 {
			return 0, 0, ErrTruncated
		}
		n := 8 + 4*int(binary.LittleEndian.Uint32(b[4:]))
		if n > len(b) || n < 8 {
			return 0, 0, ErrTruncated
		}
		return RecordPadding, n, nil
	case w0>>31 != 0:
		if len(b) < 8 {
			return 0, 0, ErrTruncated
		}
		if binary.LittleEndian.Uint32(b[4:])>>24&3 != 0 {
			return 0, 0, ErrBadRecord
		}
		return RecordSequence, 8, nil
	}
	words := int(w0 >> 16 & 0xfff)
	if words < HeaderWords || uint16(w0) == 0 {
		return 0, 0, ErrBadRecord
	}
	if 4*words > len(b) {
		return 0, 0, ErrTruncated
	}
	return RecordBlock, 4 * words, nil
}

// Block payload helpers. A block record is the header, then N 16-bit code
// words, N QScale bytes, the magnitude planes and the sign bits, where N is
// the number of 8x8 blocks set in the ballot.

// SubblockPlanes is the number of magnitude planes of subblock s of an 8x8
// block with code word cw and QScale byte qs.
func SubblockPlanes(cw uint16, qs uint8, s int) int {
	return int(cw>>(2*s)&3) + int(qs&0xf)
}

// Unswizzle8x8 maps a coefficient's index inside an 8x8 block (subblock*8
// + pixel) to its position.
func Unswizzle8x8(i int) (x, y int) {
	y = i&1 | (i>>3&3)<<1
	x = i>>1&3 | (i>>5&1)<<2
	return
}
