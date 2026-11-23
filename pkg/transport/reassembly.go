package transport

// ReassemblyWindow is how many frames stay open for late shards. When a
// frame newer than id+ReassemblyWindow arrives, frame id is finalized.
const ReassemblyWindow = 8

const ringSize = 2 * ReassemblyWindow

// PushResult says what one packet completed.
type PushResult struct {
	FrameID        uint32
	SliceReady     bool
	SliceIndex     int
	SliceRecovered bool
	FrameComplete  bool
}

// FrameStats is reported once per frame when it leaves the window.
type FrameStats struct {
	FrameID         uint32
	Complete        bool
	RecvShards      int
	SentShards      int
	Slices          int
	SlicesRecovered int
	SlicesLost      int
}

type sliceSlot struct {
	data, parity int
	sliceLen     int
	have         int
	present      [4]uint64
	backing      []byte
	shards       [][]byte
	ready        bool
	recovered    bool
	seen         bool
}

type frameSlot struct {
	active      bool
	frameID     uint32
	sliceCount  int
	slicesReady int
	frameShards int
	recv        int
	slices      [MaxSlices]sliceSlot
}

// Reassembler rebuilds slices and frames from shards, using Reed-Solomon
// reconstruction when data shards are missing. Slice bytes are stored
// contiguously, so SliceData needs no copy. Not safe for concurrent use.
type Reassembler struct {
	shardSize int
	dec       rsCache
	ring      [ringSize]frameSlot
	newest    uint32
	started   bool
	onFinal   func(FrameStats)
	Late      uint64
	Invalid   uint64
}

// NewReassembler returns a Reassembler for shards of shardSize bytes.
// onFinal, if set, is called for each frame as it leaves the window.
func NewReassembler(shardSize int, onFinal func(FrameStats)) *Reassembler {
	return &Reassembler{shardSize: shardSize, dec: rsCache{}, onFinal: onFinal}
}

// Push consumes one plaintext media packet (media header and shard).
func (r *Reassembler) Push(plain []byte) (PushResult, error) {
	var h MediaHeader
	if err := h.Parse(plain); err != nil || int(h.ShardLen) != r.shardSize {
		r.Invalid++
		return PushResult{}, ErrBadHeader
	}
	res := PushResult{FrameID: h.FrameID}
	if r.started && h.FrameID+ReassemblyWindow <= r.newest {
		r.Late++
		return res, nil
	}
	if !r.started || h.FrameID > r.newest {
		r.advance(h.FrameID)
	}
	f := &r.ring[h.FrameID%ringSize]
	if !f.active || f.frameID != h.FrameID {
		r.finalize(f)
		r.open(f, &h)
	}
	if int(h.SliceCount) != f.sliceCount {
		r.Invalid++
		return res, ErrBadHeader
	}
	f.recv++
	s := &f.slices[h.SliceIndex]
	if !s.seen {
		r.openSlice(s, &h)
	} else if s.data != int(h.DataShards) || s.parity != int(h.ParityShards) || s.sliceLen != int(h.SliceLen) {
		r.Invalid++
		return res, ErrBadHeader
	}
	idx := int(h.ShardIndex)
	if s.ready || s.present[idx/64]&(1<<(idx%64)) != 0 {
		return res, nil
	}
	s.present[idx/64] |= 1 << (idx % 64)
	s.have++
	copy(s.shards[idx][:r.shardSize], plain[MediaHeaderLen:])
	if s.have < s.data {
		return res, nil
	}
	missingData := false
	for i := 0; i < s.data+s.parity; i++ {
		if s.present[i/64]&(1<<(i%64)) == 0 {
			s.shards[i] = s.shards[i][:0]
			if i < s.data {
				missingData = true
			}
		} else {
			s.shards[i] = s.shards[i][:r.shardSize]
		}
	}
	if missingData {
		enc, err := r.dec.get(s.data, s.parity)
		if err != nil {
			return res, err
		}
		if err := enc.ReconstructData(s.shards[:s.data+s.parity]); err != nil {
			return res, err
		}
		s.recovered = true
	}
	s.ready = true
	f.slicesReady++
	res.SliceReady = true
	res.SliceIndex = int(h.SliceIndex)
	res.SliceRecovered = missingData
	res.FrameComplete = f.slicesReady == f.sliceCount
	return res, nil
}

// SliceData returns the reassembled bytes of a ready slice, or nil.
// The slice is valid until the frame leaves the window.
func (r *Reassembler) SliceData(frameID uint32, slice int) []byte {
	f := &r.ring[frameID%ringSize]
	if !f.active || f.frameID != frameID || slice >= f.sliceCount || !f.slices[slice].ready {
		return nil
	}
	s := &f.slices[slice]
	return s.backing[:s.sliceLen]
}

// Flush finalizes every open frame.
func (r *Reassembler) Flush() {
	for i := range r.ring {
		r.finalize(&r.ring[i])
	}
}

func (r *Reassembler) advance(id uint32) {
	if r.started {
		for i := range r.ring {
			f := &r.ring[i]
			if f.active && f.frameID+ReassemblyWindow <= id {
				r.finalize(f)
			}
		}
	}
	r.newest = id
	r.started = true
}

func (r *Reassembler) open(f *frameSlot, h *MediaHeader) {
	f.active = true
	f.frameID = h.FrameID
	f.sliceCount = int(h.SliceCount)
	f.slicesReady = 0
	f.frameShards = int(h.FrameShards)
	f.recv = 0
	for i := 0; i < f.sliceCount; i++ {
		f.slices[i].seen = false
	}
}

func (r *Reassembler) openSlice(s *sliceSlot, h *MediaHeader) {
	s.seen = true
	s.data = int(h.DataShards)
	s.parity = int(h.ParityShards)
	s.sliceLen = int(h.SliceLen)
	s.have = 0
	s.present = [4]uint64{}
	s.ready = false
	s.recovered = false
	total := s.data + s.parity
	need := total * r.shardSize
	if cap(s.backing) < need {
		s.backing = make([]byte, need)
		s.shards = make([][]byte, MaxShardsPerSlice)
	}
	s.backing = s.backing[:need]
	for i := 0; i < total; i++ {
		s.shards[i] = s.backing[i*r.shardSize : (i+1)*r.shardSize]
	}
}

func (r *Reassembler) finalize(f *frameSlot) {
	if !f.active {
		return
	}
	f.active = false
	if r.onFinal == nil {
		return
	}
	st := FrameStats{
		FrameID:    f.frameID,
		Complete:   f.slicesReady == f.sliceCount,
		RecvShards: f.recv,
		SentShards: f.frameShards,
		Slices:     f.sliceCount,
	}
	for i := 0; i < f.sliceCount; i++ {
		s := &f.slices[i]
		switch {
		case s.ready && s.recovered:
			st.SlicesRecovered++
		case !s.ready:
			st.SlicesLost++
		}
	}
	r.onFinal(st)
}
