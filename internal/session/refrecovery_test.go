package session

import (
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

func TestRefRecoverySkipsToTheRecoveryFrame(t *testing.T) {
	var r refRecovery
	ms := int64(time.Millisecond)
	step := func(id uint32, recovery bool, now int64) admission {
		return r.admit(wire.VideoInfo{FrameID: id, Recovery: recovery}, now)
	}
	r.admit(wire.VideoInfo{FrameID: 10, Keyframe: true}, 0)
	if a := step(11, false, ms); !a.decode || a.invalidate {
		t.Fatalf("next frame: %+v", a)
	}
	// 12 is lost: 13 shows it, and predicts from it.
	if a := step(13, false, 2*ms); a.decode || !a.invalidate || a.invalidFrom != 12 {
		t.Fatalf("frame after a gap: %+v, want skipped and 12 invalidated", a)
	}
	if a := step(14, false, 3*ms); a.decode || a.invalidate {
		t.Fatalf("frame while waiting: %+v", a)
	}
	// A late 12 decodes nothing: 13 and 14 are gone.
	if a := step(12, false, 3*ms); a.decode {
		t.Fatalf("late frame decoded: %+v", a)
	}
	if a := step(15, true, 4*ms); !a.decode {
		t.Fatalf("Recovery frame skipped: %+v", a)
	}
	if a := step(16, false, 5*ms); !a.decode {
		t.Fatalf("frame after recovery: %+v", a)
	}
}

func TestRefRecoveryAfterAGapAndAQueueDrop(t *testing.T) {
	var r refRecovery
	r.admit(wire.VideoInfo{FrameID: 1, Keyframe: true}, 0)
	r.admit(wire.VideoInfo{FrameID: 2}, 0)
	// 3 is lost and 4 is a Recovery frame: the host invalidated every
	// frame up to the one it encoded next, 3 among them.
	if a := r.admit(wire.VideoInfo{FrameID: 4, Recovery: true}, 0); !a.decode || !a.invalidate {
		t.Fatalf("Recovery frame right after a gap: %+v", a)
	}
	r.lose(5, 0)
	if a := r.admit(wire.VideoInfo{FrameID: 5, Recovery: true}, 0); a.decode {
		t.Fatalf("frame the queue dropped admitted again: %+v", a)
	}
	if a := r.admit(wire.VideoInfo{FrameID: 6}, 0); a.decode {
		t.Fatalf("frame predicting from a dropped one decoded: %+v", a)
	}
	if a := r.admit(wire.VideoInfo{FrameID: 7, Recovery: true}, 0); !a.decode {
		t.Fatalf("Recovery frame after the dropped one skipped: %+v", a)
	}
}

func TestRefRecoveryGivesUpForAKeyframe(t *testing.T) {
	var r refRecovery
	r.admit(wire.VideoInfo{FrameID: 1, Keyframe: true}, 0)
	r.admit(wire.VideoInfo{FrameID: 3}, 0)
	late := int64(refRecoveryTimeout) + 1
	if a := r.admit(wire.VideoInfo{FrameID: 4}, late); a.decode || !a.giveUp {
		t.Fatalf("no Recovery frame in %v: %+v, want giving up", refRecoveryTimeout, a)
	}
	if a := r.admit(wire.VideoInfo{FrameID: 9, Keyframe: true}, late); !a.decode {
		t.Fatalf("keyframe: %+v", a)
	}
}

func TestRefRecoveryFollowsIDWrap(t *testing.T) {
	var r refRecovery
	r.admit(wire.VideoInfo{FrameID: ^uint32(0), Keyframe: true}, 0)
	if a := r.admit(wire.VideoInfo{FrameID: 0}, 0); !a.decode || a.invalidate {
		t.Fatalf("frame after the wrap: %+v", a)
	}
}
