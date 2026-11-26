package session

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// refPipe is an encoder that predicts each P frame from the newest frame
// not invalidated, and writes that reference into the frame, so a sink can
// tell a frame decoded on top of one it never had.
type refPipe struct {
	fps     int
	next    int64
	n       uint32
	mu      sync.Mutex
	invalid map[uint32]bool
	ref     uint32
	keys    *atomic.Int64
	invs    *atomic.Int64
}

const noRef = ^uint32(0)

func (p *refPipe) Next(_ time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	if p.next == 0 {
		p.next = transport.Mono()
	}
	transport.SleepUntilMono(p.next)
	p.next += int64(time.Second) / int64(p.fps)
	p.mu.Lock()
	ref := noRef
	if forceKey {
		p.keys.Add(1)
	} else {
		ref = p.ref
	}
	p.ref = p.n
	p.mu.Unlock()
	dst = binary.BigEndian.AppendUint32(dst[:0], p.n)
	dst = binary.BigEndian.AppendUint32(dst, ref)
	dst = append(dst, make([]byte, 3000)...)
	pkt := encode.Packet{Data: dst, Keyframe: forceKey, PTS: int64(p.n)}
	p.n++
	return pkt, transport.Mono(), nil
}

// InvalidateRefs makes the next frame predict from the newest frame
// older than pts that was not invalidated.
func (p *refPipe) InvalidateRefs(pts int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invs.Add(1)
	p.invalid[uint32(pts)] = true
	for p.invalid[p.ref] && p.ref > 0 {
		p.ref--
	}
	return nil
}

func (p *refPipe) SetBitrate(int) error { return nil }
func (p *refPipe) Info() VideoInfo {
	return VideoInfo{Width: 1920, Height: 1080, FPS: p.fps, Source: "fake"}
}
func (p *refPipe) Close() error { return nil }

// TestRefInvalidationRecoversWithoutKeyframe: the client's decoder stalls
// for 60 ms, so its queue overflows and frames are lost. The client asks
// the host to invalidate them, skips what predicts from them, and decodes
// again from the host's Recovery frame: no keyframe after the first, and
// no frame decoded on top of one the decoder never had.
func TestRefInvalidationRecoversWithoutKeyframe(t *testing.T) {
	var keys, invs atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 120, BitrateKbps: 40_000, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &refPipe{fps: p.FPS, invalid: map[uint32]bool{}, keys: &keys, invs: &invs}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)

	r := newClient(t, t.TempDir(), h.PIN())
	var mu sync.Mutex
	decoded := map[uint32]bool{}
	var bad, units, afterStall int
	var keysAtStall int64
	stalled := false
	r.sink.OnUnit = func(au []byte, _ int64) {
		n, ref := binary.BigEndian.Uint32(au), binary.BigEndian.Uint32(au[4:])
		mu.Lock()
		if ref != noRef && !decoded[ref] {
			bad++
			t.Errorf("frame %d decoded on top of frame %d, never decoded", n, ref)
		}
		decoded[n] = true
		units++
		if stalled {
			afterStall++
		}
		stall := units == 60
		mu.Unlock()
		if stall {
			keysAtStall = keys.Load()
			time.Sleep(60 * time.Millisecond)
			mu.Lock()
			stalled = true
			mu.Unlock()
		}
	}
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "decoding to resume after the stall", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return afterStall >= 120
	})
	cs := r.client.Stats()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("%d frames decoded, %d dropped by the client, %d reference invalidations, %d keyframes (%d before the stall)", units, cs.FramesDropped, invs.Load(), keys.Load(), keysAtStall)
	if cs.FramesDropped == 0 {
		t.Fatal("the stall dropped no frames: nothing was recovered")
	}
	if invs.Load() == 0 {
		t.Fatal("the host invalidated no reference")
	}
	// A keyframe before the stall is the connect's: frame 0 can land
	// before the client's route is live, and the client asks again.
	if k := keys.Load(); k != keysAtStall {
		t.Fatalf("%d keyframes after the stall, want none", k-keysAtStall)
	}
	if bad != 0 {
		t.Fatalf("%d frames decoded without their reference", bad)
	}
}

type invalidPipe struct {
	refPipe
	unsupported bool
	got         []int64
}

func (p *invalidPipe) InvalidateRefs(pts int64) error {
	if p.unsupported {
		return encode.ErrUnsupported
	}
	p.got = append(p.got, pts)
	return nil
}

// TestApplyInvalidations: a lost frame invalidates it and every frame
// encoded since; one too far back, or an encoder that cannot invalidate,
// takes a keyframe instead.
func TestApplyInvalidations(t *testing.T) {
	st := &stream{invalid: make(chan uint32, 16)}
	for id := uint32(0); id < 20; id++ {
		st.pts[id%ptsRingSize].id, st.pts[id%ptsRingSize].pts = id, 1000+int64(id)
	}
	pipe := &invalidPipe{}
	st.invalidate(17)
	if key, rec := st.applyInvalidations(pipe, 20); key || !rec || len(pipe.got) != 3 || pipe.got[0] != 1017 || pipe.got[2] != 1019 {
		t.Fatalf("frame 17 before 20: key %v recovery %v, invalidated %v", key, rec, pipe.got)
	}
	st.invalidate(10)
	if key, rec := st.applyInvalidations(pipe, 20); !key || rec {
		t.Fatalf("frame 10 before 20: key %v recovery %v, want a keyframe", key, rec)
	}
	st.invalidate(21)
	if key, rec := st.applyInvalidations(pipe, 20); key || rec {
		t.Fatalf("a frame not encoded yet: key %v recovery %v", key, rec)
	}
	pipe.unsupported = true
	st.invalidate(19)
	if key, rec := st.applyInvalidations(pipe, 20); !key || rec {
		t.Fatalf("unsupported: key %v recovery %v, want a keyframe", key, rec)
	}
	for range 20 {
		st.invalidate(19)
	}
	if !st.keyNeeded.Load() {
		t.Fatal("invalidations past the queue asked for no keyframe")
	}
}

// TestLateKeyframeKeepsWaitingForKeyframe: a keyframe older than the last
// frame admitted is rejected and leaves the client waiting for a keyframe,
// so the P frames after it stay dropped.
func TestLateKeyframeKeepsWaitingForKeyframe(t *testing.T) {
	var keys, invs atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 120, BitrateKbps: 40_000, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &refPipe{fps: p.FPS, invalid: map[uint32]bool{}, keys: &keys, invs: &invs}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	r.client.mu.Lock()
	live := r.client.conn.live.Load()
	r.client.mu.Unlock()
	if !live.sess.RefInvalidation() {
		t.Fatal("reference invalidation was not negotiated")
	}

	cc := &conn{}
	cc.live.Store(live)
	cc.stream.Store(&streamState{})
	cc.ref.admit(wire.VideoInfo{FrameID: 100, Keyframe: true}, 0)
	cc.ref.admit(wire.VideoInfo{FrameID: 101}, 0)
	cc.needKey.Store(true)

	cc.onFrame(wire.VideoInfo{FrameID: 99, Keyframe: true}, nil)
	if !cc.needKey.Load() {
		t.Fatal("a rejected late keyframe cleared the wait for a keyframe")
	}
	if cc.dropped.Load() != 1 || len(cc.frames) != 0 {
		t.Fatalf("late keyframe: dropped %d, queued %d; want it dropped", cc.dropped.Load(), len(cc.frames))
	}
}
