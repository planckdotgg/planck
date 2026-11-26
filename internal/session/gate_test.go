package session

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/encode"
)

// waitingSource blocks in Next, as a display with nothing new does, until
// the test releases it.
type waitingSource struct {
	waiting chan struct{}
	release chan struct{}
}

func (s *waitingSource) Next(time.Duration) (capture.Frame, error) {
	close(s.waiting)
	<-s.release
	return capture.Frame{Width: 64, Height: 64}, nil
}
func (s *waitingSource) Release() error { return nil }
func (s *waitingSource) Close() error   { return nil }

// orderEncoder records its invalidations and encodes in the order they
// came.
type orderEncoder struct {
	mu     sync.Mutex
	events []string
}

func (e *orderEncoder) Caps() encode.Caps { return encode.Caps{} }
func (e *orderEncoder) Encode(_ capture.Frame, key bool, dst []byte) (encode.Packet, error) {
	e.mu.Lock()
	e.events = append(e.events, fmt.Sprintf("encode key=%v", key))
	e.mu.Unlock()
	return encode.Packet{Data: append(dst, 1), Keyframe: key}, nil
}
func (e *orderEncoder) InvalidateRefs(pts int64) error {
	e.mu.Lock()
	e.events = append(e.events, fmt.Sprintf("invalidate %d", pts))
	e.mu.Unlock()
	return nil
}
func (e *orderEncoder) SetBitrate(int) error { return nil }
func (e *orderEncoder) Close() error         { return nil }

// TestInvalidationDuringCaptureWait: a client's report of a lost frame
// that reaches the host while the video loop waits for the display is
// applied to the picture that wait returns, before it is encoded, not to
// the one after. Live at 5120x1440@120 the frame later turned "4 frames
// back" into "5 frames back, past 4" and a keyframe.
func TestInvalidationDuringCaptureWait(t *testing.T) {
	src := &waitingSource{waiting: make(chan struct{}), release: make(chan struct{})}
	enc := &orderEncoder{}
	pipe := &sourcePipe{src: src, enc: enc, w: 64, h: 64}
	st := &stream{h: &Host{logf: t.Logf}, invalid: make(chan invalidGap, 16)}
	for id := uint32(0); id < 10; id++ {
		st.pts[id%ptsRingSize].id, st.pts[id%ptsRingSize].pts = id, 100+int64(id)
	}
	const next = 10
	var recovered bool
	pipe.gateEncode(func() bool {
		key, rec := st.takeInvalidations(pipe, next)
		recovered = rec
		return key
	})
	go func() {
		<-src.waiting
		st.invalidate(7)
		close(src.release)
	}()
	pkt, _, err := pipe.Next(time.Second, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"invalidate 107", "invalidate 108", "invalidate 109", "encode key=false"}
	if !slices.Equal(enc.events, want) || !recovered || pkt.Keyframe {
		t.Fatalf("events %q, recovery %v, keyframe %v; want %q, a recovery frame", enc.events, recovered, pkt.Keyframe, want)
	}
}

// TestGateForcesKeyframe: a gate that says only a keyframe recovers makes
// the captured picture one.
func TestGateForcesKeyframe(t *testing.T) {
	src := &waitingSource{waiting: make(chan struct{}), release: make(chan struct{})}
	close(src.release)
	enc := &orderEncoder{}
	pipe := &sourcePipe{src: src, enc: enc, w: 64, h: 64}
	pipe.gateEncode(func() bool { return true })
	if pkt, _, err := pipe.Next(time.Second, false, nil); err != nil || !pkt.Keyframe {
		t.Fatalf("keyframe %v, err %v; events %q", pkt.Keyframe, err, enc.events)
	}
}
