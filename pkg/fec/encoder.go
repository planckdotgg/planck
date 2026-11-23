package fec

import (
	"errors"
	"fmt"

	"github.com/klauspost/reedsolomon"
	"github.com/watzon/planck/pkg/wire"
)

// MaxDataPerBlock caps data shards per Reed-Solomon block, leaving room in the
// 255-shard code for parity up to 100% of data.
const MaxDataPerBlock = 128

var ErrTooLarge = errors.New("fec: slice too large")

type rsCache map[int]reedsolomon.Encoder

func (c rsCache) get(data, parity int) (reedsolomon.Encoder, error) {
	key := data<<8 | parity
	if enc, ok := c[key]; ok {
		return enc, nil
	}
	enc, err := reedsolomon.New(data, parity)
	if err != nil {
		return nil, err
	}
	c[key] = enc
	return enc, nil
}

// Encoder turns video slices into shard datagrams. Each datagram buffer has
// headroom bytes before the plaintext (video header and shard) and tailroom
// bytes of spare capacity after it, so the caller can seal it in place.
// Buffers are reused, so after warm-up the encoder itself does not allocate.
// Not safe for concurrent use.
type Encoder struct {
	shardSize int
	headroom  int
	tailroom  int
	rs        rsCache
	bufs      [][]byte
	shards    [][]byte
}

func NewEncoder(shardSize, headroom, tailroom int) *Encoder {
	return &Encoder{
		shardSize: shardSize,
		headroom:  headroom,
		tailroom:  tailroom,
		rs:        rsCache{},
		bufs:      make([][]byte, wire.MaxShards),
		shards:    make([][]byte, wire.MaxShards),
	}
}

func (e *Encoder) ShardSize() int { return e.shardSize }

// EncodeFrame splits frame evenly into info.SliceCount slices and encodes
// each. It returns the number of datagrams emitted.
func (e *Encoder) EncodeFrame(info wire.VideoInfo, frame []byte, policy ParityPolicy, emit func(pkt []byte) error) (int, error) {
	slices := int(info.SliceCount)
	if slices < 1 || slices > wire.MaxSlices || len(frame) < slices {
		return 0, fmt.Errorf("fec: cannot split %d bytes into %d slices", len(frame), slices)
	}
	sent := 0
	for s := 0; s < slices; s++ {
		lo, hi := len(frame)*s/slices, len(frame)*(s+1)/slices
		n, err := e.EncodeSlice(info, s, frame[lo:hi], policy, emit)
		sent += n
		if err != nil {
			return sent, err
		}
	}
	return sent, nil
}

// EncodeSlice emits every datagram of one slice: per block, data shards then
// parity shards. emit receives the full buffer, headroom included; it is
// reused after emit returns. Slices of one frame may be encoded as the codec
// produces them.
func (e *Encoder) EncodeSlice(info wire.VideoInfo, slice int, data []byte, policy ParityPolicy, emit func(pkt []byte) error) (int, error) {
	if info.SliceCount == 0 || int(info.SliceCount) > wire.MaxSlices || slice < 0 || slice >= int(info.SliceCount) {
		return 0, fmt.Errorf("fec: slice %d of %d out of range", slice, info.SliceCount)
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("fec: empty slice")
	}
	ss := e.shardSize
	dataTotal := (len(data) + ss - 1) / ss
	blocks := (dataTotal + MaxDataPerBlock - 1) / MaxDataPerBlock
	if blocks > wire.MaxBlocks {
		return 0, fmt.Errorf("%w: %d bytes needs %d blocks, max %d", ErrTooLarge, len(data), blocks, wire.MaxBlocks)
	}
	h := wire.VideoHeader{
		VideoInfo:  info,
		SliceIndex: uint8(slice),
		BlockCount: uint8(blocks),
		ShardLen:   uint16(ss),
	}
	sent := 0
	off := 0
	for b := 0; b < blocks; b++ {
		d := dataTotal / blocks
		if b < dataTotal%blocks {
			d++
		}
		blockLen := min(d*ss, len(data)-off)
		parity := min(policy.Parity(d), wire.MaxShards-d)
		total := d + parity
		hdrEnd := e.headroom + wire.VideoHeaderLen
		for i := 0; i < total; i++ {
			if e.bufs[i] == nil {
				e.bufs[i] = make([]byte, hdrEnd+ss+e.tailroom)
			}
			e.shards[i] = e.bufs[i][hdrEnd : hdrEnd+ss]
		}
		block := data[off : off+blockLen]
		for i := 0; i < d; i++ {
			n := copy(e.shards[i], block[min(i*ss, len(block)):])
			clear(e.shards[i][n:])
		}
		if parity > 0 {
			enc, err := e.rs.get(d, parity)
			if err != nil {
				return sent, err
			}
			if err := enc.Encode(e.shards[:total]); err != nil {
				return sent, err
			}
		}
		h.BlockIndex = uint8(b)
		h.DataShards = uint8(d)
		h.ParityShards = uint8(parity)
		h.BlockLen = uint32(blockLen)
		for i := 0; i < total; i++ {
			h.ShardIndex = uint8(i)
			buf := e.bufs[i][:hdrEnd+ss]
			h.Put(buf[e.headroom:])
			if err := emit(buf); err != nil {
				return sent, err
			}
			sent++
		}
		off += blockLen
	}
	return sent, nil
}
