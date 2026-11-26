package session

import (
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

// refRecoveryTimeout is how long a client skips frames waiting for the
// host's Recovery frame before it asks for a keyframe instead.
const refRecoveryTimeout = 100 * time.Millisecond

// refRecovery decides, frame by frame, what a client with reference
// invalidation decodes. A gap in the frame IDs of complete frames is a lost
// frame, seen at the next complete one rather than when the lost frame
// leaves the FEC window eight frames later. The frames after it predict
// from it, so they are skipped until a keyframe, or a Recovery frame newer
// than the last one lost: the host encodes that after the RefInvalidate and
// predicts it only from frames before every frame invalidated so far.
type refRecovery struct {
	mu       sync.Mutex
	last     uint32
	haveLast bool
	waiting  bool
	// lost is the newest frame lost while waiting, since when.
	lost  uint32
	since int64
}

// admission is what to do with one complete frame.
type admission struct {
	decode bool
	// invalidate asks the host to stop predicting from invalidFrom on.
	invalidate  bool
	invalidFrom uint32
	// giveUp: no Recovery frame came in time; wait for a keyframe.
	giveUp bool
}

// admit takes the next complete frame, now on transport.Mono.
func (r *refRecovery) admit(info wire.VideoInfo, now int64) admission {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info.Keyframe {
		r.last, r.haveLast, r.waiting = info.FrameID, true, false
		return admission{decode: true}
	}
	if r.haveLast && !newerFrame(info.FrameID, r.last) {
		// Late: the frames after it were decoded or skipped already.
		return admission{}
	}
	var a admission
	if r.haveLast && info.FrameID != r.last+1 {
		a.invalidate, a.invalidFrom = true, r.last+1
		r.loseLocked(info.FrameID-1, now)
	}
	r.last, r.haveLast = info.FrameID, true
	switch {
	case !r.waiting:
		a.decode = true
	case info.Recovery && newerFrame(info.FrameID, r.lost):
		r.waiting = false
		a.decode = true
	case now-r.since > int64(refRecoveryTimeout):
		r.waiting = false
		a.giveUp = true
	}
	return a
}

// lose records that frame id, admitted, will not be decoded (a full
// decode queue dropped it); the caller sends RefInvalidate for it.
func (r *refRecovery) lose(id uint32, now int64) {
	r.mu.Lock()
	r.loseLocked(id, now)
	r.mu.Unlock()
}

func (r *refRecovery) loseLocked(id uint32, now int64) {
	if !r.waiting {
		r.since = now
	}
	if !r.waiting || newerFrame(id, r.lost) {
		r.lost = id
	}
	r.waiting = true
}

func newerFrame(a, b uint32) bool { return int32(a-b) > 0 }
