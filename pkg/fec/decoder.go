package fec

import (
	"errors"

	"github.com/planckdotgg/planck/pkg/wire"
)

// ReassemblyWindow is how many frames stay open for late shards. When a frame
// newer than id+ReassemblyWindow arrives, frame id is finalized.
const ReassemblyWindow = 8

const ringSize = 2 * ReassemblyWindow

var ErrBadShard = errors.New("fec: bad or inconsistent shard")

// PushResult says what one shard completed.
type PushResult struct {
	Info          wire.VideoInfo
	SliceReady    bool
	SliceIndex    int
	Recovered     bool
	FrameComplete bool
	// FrameBytes is the frame's total slice bytes, set when FrameComplete.
	FrameBytes int
}

// FrameStats is reported once per frame when it leaves the window.
// SentShards counts only blocks that delivered at least one shard.
type FrameStats struct {
	FrameID         uint32
	Keyframe        bool
	Complete        bool
	RecvShards      int
	SentShards      int
	Slices          int
	SlicesRecovered int
	SlicesLost      int
}

type blockSlot struct {
	seen      bool
	ready     bool
	recovered bool
	data      int
	parity    int
	blockLen  int
	have      int
	present   [4]uint64
	backing   []byte
	shards    [][]byte
}

type sliceSlot struct {
	seen        bool
	ready       bool
	recovered   bool
	blockCount  int
	blocksReady int
	blocks      []blockSlot
	joined      []byte
	out         []byte
}

type frameSlot struct {
	active      bool
	info        wire.VideoInfo
	slicesReady int
	recv        int
	sent        int
	bytes       int
	slices      [wire.MaxSlices]sliceSlot
}

// Decoder rebuilds slices and frames from shard datagrams, using
// Reed-Solomon reconstruction when data shards are missing. Single-block
// slices are stored contiguously, so Slice needs no copy. Not safe for
// concurrent use.
type Decoder struct {
	shardSize int
	rs        rsCache
	ring      [ringSize]frameSlot
	newest    uint32
	started   bool
	onFinal   func(FrameStats)

	Late    uint64
	Invalid uint64
}

// NewDecoder returns a Decoder for shards of shardSize bytes. onFinal, if
// set, is called for each frame as it leaves the window.
func NewDecoder(shardSize int, onFinal func(FrameStats)) *Decoder {
	return &Decoder{shardSize: shardSize, rs: rsCache{}, onFinal: onFinal}
}

func newer(a, b uint32) bool { return int32(a-b) > 0 }

// Push consumes one plaintext video datagram (header and shard).
func (d *Decoder) Push(plain []byte) (PushResult, error) {
	var h wire.VideoHeader
	if err := h.Parse(plain); err != nil || int(h.ShardLen) != d.shardSize {
		d.Invalid++
		return PushResult{}, ErrBadShard
	}
	res := PushResult{Info: h.VideoInfo}
	if d.started && !newer(h.FrameID+ReassemblyWindow, d.newest) {
		d.Late++
		return res, nil
	}
	if !d.started || newer(h.FrameID, d.newest) {
		d.advance(h.FrameID)
	}
	f := &d.ring[h.FrameID%ringSize]
	if !f.active || f.info.FrameID != h.FrameID {
		d.finalize(f)
		d.open(f, &h)
	}
	if h.SliceCount != f.info.SliceCount {
		d.Invalid++
		return res, ErrBadShard
	}
	res.Info = f.info
	s := &f.slices[h.SliceIndex]
	if !s.seen {
		openSlice(s, int(h.BlockCount))
	} else if s.blockCount != int(h.BlockCount) {
		d.Invalid++
		return res, ErrBadShard
	}
	b := &s.blocks[h.BlockIndex]
	if !b.seen {
		d.openBlock(b, &h)
		f.sent += b.data + b.parity
	} else if b.data != int(h.DataShards) || b.parity != int(h.ParityShards) || b.blockLen != int(h.BlockLen) {
		d.Invalid++
		return res, ErrBadShard
	}
	f.recv++
	idx := int(h.ShardIndex)
	if b.ready || b.present[idx/64]&(1<<(idx%64)) != 0 {
		return res, nil
	}
	b.present[idx/64] |= 1 << (idx % 64)
	b.have++
	copy(b.shards[idx][:d.shardSize], plain[wire.VideoHeaderLen:])
	if b.have < b.data {
		return res, nil
	}
	if err := d.completeBlock(b); err != nil {
		return res, err
	}
	s.blocksReady++
	s.recovered = s.recovered || b.recovered
	if s.blocksReady < s.blockCount {
		return res, nil
	}
	s.ready = true
	s.out = joinBlocks(s)
	f.slicesReady++
	f.bytes += len(s.out)
	res.SliceReady = true
	res.SliceIndex = int(h.SliceIndex)
	res.Recovered = s.recovered
	if f.slicesReady == int(f.info.SliceCount) {
		res.FrameComplete = true
		res.FrameBytes = f.bytes
	}
	return res, nil
}

func (d *Decoder) completeBlock(b *blockSlot) error {
	missingData := false
	for i := 0; i < b.data+b.parity; i++ {
		if b.present[i/64]&(1<<(i%64)) == 0 {
			b.shards[i] = b.shards[i][:0]
			if i < b.data {
				missingData = true
			}
		} else {
			b.shards[i] = b.shards[i][:d.shardSize]
		}
	}
	if missingData {
		enc, err := d.rs.get(b.data, b.parity)
		if err != nil {
			return err
		}
		if err := enc.ReconstructData(b.shards[:b.data+b.parity]); err != nil {
			return err
		}
		b.recovered = true
	}
	b.ready = true
	return nil
}

func joinBlocks(s *sliceSlot) []byte {
	if s.blockCount == 1 {
		b := &s.blocks[0]
		return b.backing[:b.blockLen]
	}
	s.joined = s.joined[:0]
	for i := 0; i < s.blockCount; i++ {
		b := &s.blocks[i]
		s.joined = append(s.joined, b.backing[:b.blockLen]...)
	}
	return s.joined
}

// Slice returns the bytes of a ready slice, or nil. The bytes stay valid
// until the frame leaves the window.
func (d *Decoder) Slice(frameID uint32, slice int) []byte {
	f := &d.ring[frameID%ringSize]
	if !f.active || f.info.FrameID != frameID || slice < 0 || slice >= int(f.info.SliceCount) || !f.slices[slice].ready {
		return nil
	}
	return f.slices[slice].out
}

// AppendFrame appends every slice of a complete frame to dst. It reports
// false, leaving dst unchanged, if the frame is not complete.
func (d *Decoder) AppendFrame(dst []byte, frameID uint32) ([]byte, bool) {
	f := &d.ring[frameID%ringSize]
	if !f.active || f.info.FrameID != frameID || f.slicesReady != int(f.info.SliceCount) {
		return dst, false
	}
	for i := 0; i < int(f.info.SliceCount); i++ {
		dst = append(dst, f.slices[i].out...)
	}
	return dst, true
}

// AppendPartial appends what is known of frame frameID to dst: ready slices
// whole, and in other slices each seen block's data shards, with zeros for
// the shards that neither arrived nor were recovered. Blocks and slices with
// no shard seen are left out, so offsets shift: only a stream of
// self-describing shard-sized chunks (PyroWave) can use it. missing counts
// the zeroed shards. ok is false once the frame has left the window.
func (d *Decoder) AppendPartial(dst []byte, frameID uint32) (au []byte, missing int, ok bool) {
	f := &d.ring[frameID%ringSize]
	if !f.active || f.info.FrameID != frameID {
		return dst, 0, false
	}
	ss := d.shardSize
	for i := 0; i < int(f.info.SliceCount); i++ {
		s := &f.slices[i]
		if !s.seen {
			continue
		}
		if s.ready {
			dst = append(dst, s.out...)
			continue
		}
		for j := range s.blocks {
			b := &s.blocks[j]
			if !b.seen {
				continue
			}
			if b.ready {
				dst = append(dst, b.backing[:b.blockLen]...)
				continue
			}
			for k := 0; k < b.data; k++ {
				lo, hi := k*ss, min((k+1)*ss, b.blockLen)
				n := len(dst)
				dst = append(dst, b.backing[lo:hi]...)
				if b.present[k/64]&(1<<(k%64)) == 0 {
					clear(dst[n:])
					missing++
				}
			}
		}
	}
	return dst, missing, true
}

// Flush finalizes every open frame.
func (d *Decoder) Flush() {
	for i := range d.ring {
		d.finalize(&d.ring[i])
	}
}

func (d *Decoder) advance(id uint32) {
	if d.started {
		for i := range d.ring {
			f := &d.ring[i]
			if f.active && !newer(f.info.FrameID+ReassemblyWindow, id) {
				d.finalize(f)
			}
		}
	}
	d.newest = id
	d.started = true
}

func (d *Decoder) open(f *frameSlot, h *wire.VideoHeader) {
	f.active = true
	f.info = h.VideoInfo
	f.slicesReady = 0
	f.recv = 0
	f.sent = 0
	f.bytes = 0
	for i := 0; i < int(h.SliceCount); i++ {
		f.slices[i].seen = false
		f.slices[i].ready = false
	}
}

func openSlice(s *sliceSlot, blockCount int) {
	s.seen = true
	s.ready = false
	s.recovered = false
	s.blockCount = blockCount
	s.blocksReady = 0
	s.out = nil
	if cap(s.blocks) < blockCount {
		s.blocks = append(s.blocks[:cap(s.blocks)], make([]blockSlot, blockCount-cap(s.blocks))...)
	}
	s.blocks = s.blocks[:blockCount]
	for i := range s.blocks {
		s.blocks[i].seen = false
	}
}

func (d *Decoder) openBlock(b *blockSlot, h *wire.VideoHeader) {
	b.seen = true
	b.ready = false
	b.recovered = false
	b.data = int(h.DataShards)
	b.parity = int(h.ParityShards)
	b.blockLen = int(h.BlockLen)
	b.have = 0
	b.present = [4]uint64{}
	total := b.data + b.parity
	need := total * d.shardSize
	if cap(b.backing) < need {
		b.backing = make([]byte, need)
	}
	if b.shards == nil {
		b.shards = make([][]byte, wire.MaxShards)
	}
	b.backing = b.backing[:need]
	for i := 0; i < total; i++ {
		b.shards[i] = b.backing[i*d.shardSize : (i+1)*d.shardSize]
	}
}

func (d *Decoder) finalize(f *frameSlot) {
	if !f.active {
		return
	}
	f.active = false
	if d.onFinal == nil {
		return
	}
	st := FrameStats{
		FrameID:    f.info.FrameID,
		Keyframe:   f.info.Keyframe,
		Complete:   f.slicesReady == int(f.info.SliceCount),
		RecvShards: f.recv,
		SentShards: f.sent,
		Slices:     int(f.info.SliceCount),
	}
	for i := 0; i < st.Slices; i++ {
		s := &f.slices[i]
		switch {
		case s.ready && s.recovered:
			st.SlicesRecovered++
		case !s.ready:
			st.SlicesLost++
		}
	}
	d.onFinal(st)
}
