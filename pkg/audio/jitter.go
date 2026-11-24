package audio

import (
	"sync"
	"time"
)

type JitterConfig struct {
	FrameDuration time.Duration // zero selects DefaultFrameDuration
	MinDepth      time.Duration // zero selects one frame
	TargetDepth   time.Duration // starting target; zero selects 20ms
	MaxDepth      time.Duration // zero selects 40ms
}

// PopStatus says what a JitterBuffer.Pop produced.
type PopStatus uint8

const (
	// Buffering: the buffer is filling to its target depth; play silence.
	Buffering PopStatus = iota
	// Packet: the next packet in sequence was copied out.
	Packet
	// Missing: the next packet has not arrived; conceal one frame.
	Missing
)

type JitterStats struct {
	Depth       time.Duration // frames queued, counting gaps, times frame duration
	Target      time.Duration
	Received    uint64
	Late        uint64 // arrived after its play-out slot
	Duplicate   uint64
	Lost        uint64 // play-out slots concealed while later packets were queued
	Underruns   uint64 // play-out slots concealed with nothing queued
	Overruns    uint64 // depth passed MaxDepth and was cut back to target
	Dropped     uint64 // packets skipped to cut depth
	Resets      uint64 // sequence jumps, either way, too large to bridge
	Concealment uint64 // Lost + Underruns
}

const (
	jitterSlots = 64
	// The target drops a frame after this many play-out slots with no underrun.
	jitterDecaySlots = 500
	// Once per window, if the queue never fell below target+1 frames, one
	// frame is dropped so latency does not creep up from clock drift or a
	// burst.
	jitterTrimSlots = 100
)

type jitterSlot struct {
	seq  uint64
	full bool
	n    int
	data [MaxPacketSize]byte
}

// JitterBuffer reorders Opus packets by sequence number and hands them out one
// frame per play-out tick. Its target depth grows by a frame on each underrun
// and shrinks by a frame after a quiet stretch, staying within
// [MinDepth, MaxDepth]. Push and Pop may run on different goroutines.
type JitterBuffer struct {
	mu               sync.Mutex
	frame            time.Duration
	minFrames        uint64
	maxFrames        uint64
	target           uint64
	slots            [jitterSlots]jitterSlot
	started, playing bool
	next, highest    uint64 // next is the sequence to play; highest the newest seen
	sinceUnderrun    int
	underrunRun      uint64 // consecutive underruns
	trimCount        int
	trimMin          uint64
	stats            JitterStats
}

func NewJitterBuffer(cfg JitterConfig) *JitterBuffer {
	if cfg.FrameDuration == 0 {
		cfg.FrameDuration = DefaultFrameDuration
	}
	if cfg.MinDepth == 0 {
		cfg.MinDepth = cfg.FrameDuration
	}
	if cfg.TargetDepth == 0 {
		cfg.TargetDepth = 20 * time.Millisecond
	}
	if cfg.MaxDepth == 0 {
		cfg.MaxDepth = 40 * time.Millisecond
	}
	frames := func(d time.Duration) uint64 {
		n := uint64((d + cfg.FrameDuration - 1) / cfg.FrameDuration)
		return max(1, min(n, jitterSlots/2))
	}
	j := &JitterBuffer{frame: cfg.FrameDuration, minFrames: frames(cfg.MinDepth), maxFrames: frames(cfg.MaxDepth)}
	j.maxFrames = max(j.maxFrames, j.minFrames)
	j.target = min(max(frames(cfg.TargetDepth), j.minFrames), j.maxFrames)
	return j
}

// extend widens a 16-bit wire sequence number to the 64-bit sequence closest
// to ref.
func extend(seq uint16, ref uint64) uint64 {
	diff := int16(seq - uint16(ref))
	return uint64(int64(ref) + int64(diff))
}

// Push stores a copy of payload as packet seq.
func (j *JitterBuffer) Push(seq uint16, payload []byte) {
	if len(payload) == 0 || len(payload) > MaxPacketSize {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stats.Received++
	if !j.started {
		// Start well above zero so extend never wraps below it.
		j.started = true
		j.next = 1<<32 + uint64(seq)
		j.highest = j.next
	}
	s := extend(seq, j.highest)
	switch {
	case s+jitterSlots <= j.next || s >= j.next+jitterSlots:
		j.reset(s)
	case s < j.next:
		j.stats.Late++
		return
	}
	slot := &j.slots[s%jitterSlots]
	if slot.full && slot.seq == s {
		j.stats.Duplicate++
		return
	}
	slot.seq, slot.full, slot.n = s, true, copy(slot.data[:], payload)
	if s > j.highest {
		j.highest = s
	}
	if j.depth() > j.maxFrames {
		j.stats.Overruns++
		j.skip(j.depth() - j.target)
	}
}

// Restart drops what is buffered; the next packet starts a new sequence,
// as a new stream from the same host numbers its packets afresh.
func (j *JitterBuffer) Restart() {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := range j.slots {
		j.slots[i].full = false
	}
	j.started, j.playing = false, false
}

func (j *JitterBuffer) reset(s uint64) {
	j.stats.Resets++
	for i := range j.slots {
		j.slots[i].full = false
	}
	j.next, j.highest, j.playing = s, s, false
}

// depth counts the frames from next through highest, holes included.
func (j *JitterBuffer) depth() uint64 {
	if !j.started || j.highest < j.next {
		return 0
	}
	return j.highest - j.next + 1
}

func (j *JitterBuffer) skip(n uint64) {
	for ; n > 0; n-- {
		if slot := &j.slots[j.next%jitterSlots]; slot.full && slot.seq == j.next {
			slot.full = false
			j.stats.Dropped++
		}
		j.next++
	}
}

// Pop is called once per frame duration by the play-out clock. On Packet it
// copies the payload into dst (which should hold MaxPacketSize bytes) and
// returns its length.
func (j *JitterBuffer) Pop(dst []byte) (int, PopStatus) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.playing {
		if j.depth() < j.target {
			return 0, Buffering
		}
		j.playing = true
		j.trimCount, j.trimMin = 0, j.depth()
	}
	j.trim()
	if j.sinceUnderrun++; j.sinceUnderrun >= jitterDecaySlots {
		j.sinceUnderrun = 0
		if j.target > j.minFrames {
			j.target--
		}
	}
	slot := &j.slots[j.next%jitterSlots]
	if slot.full && slot.seq == j.next {
		slot.full = false
		j.next++
		j.underrunRun = 0
		return copy(dst, slot.data[:slot.n]), Packet
	}
	if j.depth() > 0 {
		j.stats.Lost++
		j.next++
		j.underrunRun = 0
		return 0, Missing
	}
	// Nothing queued: the packet is late or the stream stalled. Hold the
	// sequence so a late arrival still plays; the frame of delay this adds is
	// the depth increase an underrun calls for. A stall longer than the
	// maximum depth rebuffers.
	j.stats.Underruns++
	j.sinceUnderrun = 0
	if j.underrunRun == 0 && j.target < j.maxFrames {
		j.target++
	}
	if j.underrunRun++; j.underrunRun > j.maxFrames {
		j.playing = false
		j.underrunRun = 0
	}
	return 0, Missing
}

func (j *JitterBuffer) trim() {
	j.trimMin = min(j.trimMin, j.depth())
	if j.trimCount++; j.trimCount < jitterTrimSlots {
		return
	}
	if j.trimMin > j.target+1 {
		j.skip(1)
	}
	j.trimCount, j.trimMin = 0, j.depth()
}

func (j *JitterBuffer) Stats() JitterStats {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := j.stats
	st.Depth = time.Duration(j.depth()) * j.frame
	st.Target = time.Duration(j.target) * j.frame
	st.Concealment = st.Lost + st.Underruns
	return st
}
