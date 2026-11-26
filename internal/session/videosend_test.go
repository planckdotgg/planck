package session

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// slowOut takes delay to send each frame or slice, as pacing a large
// frame does, and records what went out.
type slowOut struct {
	delay  time.Duration
	mu     sync.Mutex
	frames []uint32
	slices []uint32
}

func (o *slowOut) wait() { transport.SleepUntilMono(transport.Mono() + int64(o.delay)) }

func (o *slowOut) SendVideoFrame(info wire.VideoInfo, _ []byte) error {
	o.wait()
	o.mu.Lock()
	o.frames = append(o.frames, info.FrameID)
	o.mu.Unlock()
	return nil
}

func (o *slowOut) SendVideoSlice(info wire.VideoInfo, slice int, _ []byte) error {
	o.wait()
	o.mu.Lock()
	o.slices = append(o.slices, info.FrameID<<8|uint32(slice))
	o.mu.Unlock()
	return nil
}

func (o *slowOut) SendChunkedFrame(info wire.VideoInfo, frame []byte, _ []int, _ int) error {
	return o.SendVideoFrame(info, frame)
}

// TestVideoSenderNeverBlocksTheLoop: with each frame taking 20 ms to send,
// handing over 30 frames 1 ms apart never waits, the frames that go out
// do so in order with the newest last, and the ones that fell two behind
// are dropped and reported.
func TestVideoSenderNeverBlocksTheLoop(t *testing.T) {
	out := &slowOut{delay: 20 * time.Millisecond}
	var mu sync.Mutex
	var dropped, sent []uint32
	s := newVideoSender(out, 2, func(id uint32) {
		mu.Lock()
		dropped = append(dropped, id)
		mu.Unlock()
	}, func(j *sendJob, err error) {
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		sent = append(sent, j.info.FrameID)
		mu.Unlock()
	})
	var worst time.Duration
	for id := uint32(1); id <= 30; id++ {
		buf := append(s.buffer(), make([]byte, 1000)...)
		t0 := time.Now()
		s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: id}, data: buf, buf: buf, sent: true})
		worst = max(worst, time.Since(t0))
		time.Sleep(time.Millisecond)
	}
	// Let the queue drain: the last frame must go out, not be dropped.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := len(sent) > 0 && sent[len(sent)-1] == 30
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.close()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("hand-over worst %v; sent %v; dropped %v", worst, sent, dropped)
	if worst > 2*time.Millisecond {
		t.Fatalf("a hand-over took %v: the loop waited on the sender", worst)
	}
	if !slices.IsSorted(sent) || sent[len(sent)-1] != 30 {
		t.Fatalf("frames sent out of order or the newest missing: %v", sent)
	}
	if len(dropped) == 0 || len(sent)+len(dropped) != 30 {
		t.Fatalf("sent %d and dropped %d of 30", len(sent), len(dropped))
	}
	for _, id := range dropped {
		if slices.Contains(sent, id) {
			t.Fatalf("frame %d both sent and dropped", id)
		}
	}
}

// TestVideoSenderKeepsTheFrameInFlight: a frame whose slices are going
// out is never cut short by drops; a whole queued frame behind it is.
func TestVideoSenderKeepsTheFrameInFlight(t *testing.T) {
	out := &slowOut{delay: 5 * time.Millisecond}
	var dropped []uint32
	s := newVideoSender(out, 1, func(id uint32) { dropped = append(dropped, id) }, func(*sendJob, error) {})
	slice := func(id uint32, i int) {
		s.push(&sendJob{kind: sendSlice, info: wire.VideoInfo{FrameID: id, SliceCount: 4}, slice: i, data: []byte{1}})
	}
	slice(1, 0)
	time.Sleep(time.Millisecond)
	slice(1, 1)
	s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: 2}, data: []byte{1}})
	slice(1, 2)
	slice(1, 3)
	s.push(&sendJob{kind: sendEnd, info: wire.VideoInfo{FrameID: 1}})
	s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: 3}, data: []byte{1}})
	time.Sleep(60 * time.Millisecond)
	s.close()
	out.mu.Lock()
	defer out.mu.Unlock()
	if want := []uint32{1<<8 | 0, 1<<8 | 1, 1<<8 | 2, 1<<8 | 3}; !slices.Equal(out.slices, want) {
		t.Fatalf("slices of the frame in flight: %x, want %x", out.slices, want)
	}
	if !slices.Equal(dropped, []uint32{2}) || !slices.Equal(out.frames, []uint32{3}) {
		t.Fatalf("dropped %v, sent whole %v: want 2 dropped and 3 sent", dropped, out.frames)
	}
}

// TestVideoSenderCloseDropsTheQueue: close returns after the frame going
// out, without sending what is queued behind it, and push after close is
// a no-op.
func TestVideoSenderCloseDropsTheQueue(t *testing.T) {
	out := &slowOut{delay: 30 * time.Millisecond}
	s := newVideoSender(out, 4, func(uint32) {}, func(*sendJob, error) {})
	for id := uint32(1); id <= 4; id++ {
		s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: id}, data: []byte{1}})
	}
	time.Sleep(5 * time.Millisecond)
	t0 := time.Now()
	s.close()
	if d := time.Since(t0); d > 100*time.Millisecond {
		t.Fatalf("close took %v", d)
	}
	s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: 5}, data: []byte{1}})
	out.mu.Lock()
	defer out.mu.Unlock()
	if !slices.Equal(out.frames, []uint32{1}) {
		t.Fatalf("sent %v after close, want only the frame in flight", out.frames)
	}
}

func TestVideoSenderHandOverDoesNotAllocate(t *testing.T) {
	s := newVideoSender(&slowOut{}, 2, func(uint32) {}, func(*sendJob, error) {})
	defer s.close()
	id := uint32(0)
	buf := s.buffer()
	job := &sendJob{kind: sendFrame}
	push := func() {
		id++
		job.info.FrameID, job.data, job.buf = id, buf, buf
		s.push(job)
		buf = s.buffer()
	}
	for range 100 {
		push()
		time.Sleep(50 * time.Microsecond)
	}
	if a := testing.AllocsPerRun(1000, push); a > 0.05 {
		t.Fatalf("hand-over allocates %.2f per frame", a)
	}
}

// TestSentRateCountsOnlyFramesSent: frames the sender drops under
// backpressure never count toward the host's sent FPS and bitrate.
func TestSentRateCountsOnlyFramesSent(t *testing.T) {
	st := &stream{ctx: context.Background()}
	out := &slowOut{delay: 20 * time.Millisecond}
	var dropped atomic.Int32
	s := newVideoSender(out, 1, func(uint32) { dropped.Add(1) }, st.frameSent)
	const size = 1000
	for id := uint32(1); id <= 10; id++ {
		buf := append(s.buffer(), make([]byte, size)...)
		s.push(&sendJob{kind: sendFrame, info: wire.VideoInfo{FrameID: id}, data: buf, buf: buf, bytes: size, sent: true})
	}
	waitFor(t, "the last frame sent", func() bool {
		out.mu.Lock()
		defer out.mu.Unlock()
		return len(out.frames) > 0 && out.frames[len(out.frames)-1] == 10
	})
	s.close()
	out.mu.Lock()
	sent := len(out.frames)
	out.mu.Unlock()
	if dropped.Load() == 0 {
		t.Fatal("no frame was dropped; the test needs backpressure")
	}
	st.sent.mu.Lock()
	counted, bytes := len(st.sent.stamps), 0
	for _, b := range st.sent.bytes {
		bytes += b
	}
	st.sent.mu.Unlock()
	if counted != sent || bytes != sent*size || st.frames.Load() != uint64(sent) {
		t.Fatalf("counted %d frames, %d bytes, frames %d; sent %d frames of %d bytes (%d dropped)",
			counted, bytes, st.frames.Load(), sent, size, dropped.Load())
	}
}
