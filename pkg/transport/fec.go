package transport

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/klauspost/reedsolomon"
)

const (
	MaxSlices         = 16
	MaxShardsPerSlice = 255
)

// PacketBufLen returns the buffer size for one packet of shardSize bytes,
// with headroom for the AEAD header in front and the tag behind.
func PacketBufLen(shardSize int) int {
	return AEADHeaderLen + MediaHeaderLen + shardSize + AEADTagLen
}

// ParityPolicy chooses the parity shard count for a slice of data shards.
type ParityPolicy interface {
	Parity(data int) int
}

// FixedParity is a parity percentage, rounded up so any non-zero percentage
// yields at least one parity shard.
type FixedParity int

func (f FixedParity) Parity(data int) int {
	if f <= 0 {
		return 0
	}
	return (data*int(f) + 99) / 100
}

// AdaptiveParity sizes parity from the observed loss rate: the smallest k such
// that, with independent loss at that rate, a slice of data+k shards loses
// more than k with probability below Target. Safe for concurrent SetLoss.
type AdaptiveParity struct {
	Target  float64
	MinLoss float64
	loss    atomic.Uint64
}

func (a *AdaptiveParity) SetLoss(p float64) { a.loss.Store(math.Float64bits(p)) }
func (a *AdaptiveParity) Loss() float64     { return math.Float64frombits(a.loss.Load()) }

func (a *AdaptiveParity) Parity(data int) int {
	p := max(a.Loss(), a.MinLoss)
	if p <= 0 {
		return 0
	}
	for k := 0; data+k < MaxShardsPerSlice; k++ {
		if binomialTailAbove(data+k, k, p) < a.Target {
			return k
		}
	}
	return MaxShardsPerSlice - data
}

// binomialTailAbove returns P(X > k) for X ~ Binomial(n, p).
func binomialTailAbove(n, k int, p float64) float64 {
	pmf := math.Pow(1-p, float64(n))
	cdf := pmf
	for i := 1; i <= k; i++ {
		pmf *= float64(n-i+1) / float64(i) * p / (1 - p)
		cdf += pmf
	}
	return max(0, 1-cdf)
}

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

// Packetizer splits frames into slices and each slice into fixed-size data
// shards plus Reed-Solomon parity shards. Buffers are reused between calls,
// so after warm-up it does not allocate. Not safe for concurrent use.
type Packetizer struct {
	shardSize int
	enc       rsCache
	bufs      [][]byte
	shards    [][]byte
}

func NewPacketizer(shardSize int) *Packetizer {
	p := &Packetizer{shardSize: shardSize, enc: rsCache{}}
	p.bufs = make([][]byte, MaxShardsPerSlice)
	p.shards = make([][]byte, MaxShardsPerSlice)
	return p
}

// Layout reports the per-slice shard counts for a frame without sending it.
func (p *Packetizer) Layout(frameLen, slices int, policy ParityPolicy) (data, parity, total int, err error) {
	if slices < 1 || slices > MaxSlices || frameLen < slices {
		return 0, 0, 0, fmt.Errorf("transport: cannot split %d bytes into %d slices", frameLen, slices)
	}
	for s := 0; s < slices; s++ {
		lo, hi := sliceBounds(frameLen, slices, s)
		d := (hi - lo + p.shardSize - 1) / p.shardSize
		par := policy.Parity(d)
		if d+par > MaxShardsPerSlice {
			return 0, 0, 0, fmt.Errorf("transport: slice needs %d shards, max %d", d+par, MaxShardsPerSlice)
		}
		data += d
		parity += par
	}
	return data, parity, data + parity, nil
}

func sliceBounds(frameLen, slices, s int) (int, int) {
	return frameLen * s / slices, frameLen * (s + 1) / slices
}

// Packetize emits every packet of frame, slice by slice, data shards before
// parity. emit receives a buffer of PacketBufLen(shardSize) bytes whose
// plaintext (media header and shard) starts at AEADHeaderLen; the buffer is
// reused after emit returns.
func (p *Packetizer) Packetize(frameID uint32, frame []byte, slices int, policy ParityPolicy, emit func(pkt []byte) error) error {
	_, _, frameShards, err := p.Layout(len(frame), slices, policy)
	if err != nil {
		return err
	}
	ss := p.shardSize
	for s := 0; s < slices; s++ {
		lo, hi := sliceBounds(len(frame), slices, s)
		data := (hi - lo + ss - 1) / ss
		parity := policy.Parity(data)
		total := data + parity
		for i := 0; i < total; i++ {
			if p.bufs[i] == nil {
				p.bufs[i] = make([]byte, PacketBufLen(ss))
			}
			p.shards[i] = p.bufs[i][AEADHeaderLen+MediaHeaderLen : AEADHeaderLen+MediaHeaderLen+ss]
		}
		for i := 0; i < data; i++ {
			n := copy(p.shards[i], frame[lo+i*ss:hi])
			clear(p.shards[i][n:])
		}
		if parity > 0 {
			enc, err := p.enc.get(data, parity)
			if err != nil {
				return err
			}
			if err := enc.Encode(p.shards[:total]); err != nil {
				return err
			}
		}
		h := MediaHeader{
			SliceIndex:   uint8(s),
			SliceCount:   uint8(slices),
			DataShards:   uint8(data),
			ParityShards: uint8(parity),
			ShardLen:     uint16(ss),
			FrameID:      frameID,
			SliceLen:     uint32(hi - lo),
			FrameShards:  uint16(frameShards),
		}
		for i := 0; i < total; i++ {
			h.ShardIndex = uint8(i)
			buf := p.bufs[i][:AEADHeaderLen+MediaHeaderLen+ss]
			h.Put(buf[AEADHeaderLen:])
			if err := emit(buf); err != nil {
				return err
			}
		}
	}
	return nil
}
