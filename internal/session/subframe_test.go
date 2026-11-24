package session

import (
	"context"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/transport"
)

// slicingPipe is a fakePipe whose encoder streams each frame as four
// uneven slices, sleeping between them as a sub-frame encoder finishes them.
type slicingPipe struct {
	fakePipe
	streamed int
}

func (p *slicingPipe) NextSlices(timeout time.Duration, forceKey bool, dst []byte, emit sliceEmit) (encode.Packet, int64, bool, error) {
	pkt, captured, err := p.Next(timeout, forceKey, dst)
	if err != nil {
		return pkt, captured, false, err
	}
	n := len(pkt.Data)
	ends := []int{n / 10, n / 2, n * 3 / 4, n}
	start := 0
	for i, end := range ends {
		if err := emit(captured, i, pkt.Data[start:end], pkt.Keyframe); err != nil {
			return pkt, captured, true, err
		}
		start = end
		time.Sleep(100 * time.Microsecond)
	}
	p.streamed++
	return pkt, captured, true, nil
}

func TestSubFrameSlicesArriveWhole(t *testing.T) {
	var pipe *slicingPipe
	hs := &harness{}
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 120, BitrateKbps: 40_000, Slices: 4, SubFrame: true, NoTailscale: true, NoAudio: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			if !p.SubFrame || p.Slices != 4 {
				t.Errorf("video params %+v: want 4 slices, sub-frame", p)
			}
			pipe = &slicingPipe{fakePipe: fakePipe{fps: p.FPS, forced: &hs.forced, kbps: &hs.kbps}}
			return pipe, nil
		},
		Logf: t.Logf,
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
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 120 })
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d frames rebuilt from streamed slices were corrupt", n)
	}
	cs := r.client.Stats()
	if cs.FramesLost != 0 {
		t.Fatalf("%d frames lost on loopback", cs.FramesLost)
	}
	r.client.Disconnect()
	h.Stop()
	if pipe.streamed < 120 {
		t.Fatalf("the host streamed %d frames slice by slice", pipe.streamed)
	}
}

// TestGlassLatency feeds a presenting sink's reports through the client's
// observer and checks the glass and capture-to-glass windows.
func TestGlassLatency(t *testing.T) {
	cc := &conn{glassW: newWindow(8), e2eGlassW: newWindow(8), presentW: newWindow(8)}
	now := transport.Mono()
	cc.captured[7%captureRing] = capturedAt{frame: 7, at: now - int64(20*time.Millisecond), row: -1}
	o := viewObserver{cc}
	o.Presented(player.Timing{PTS: 7, Present: time.Millisecond}, nil)
	o.Glass(7, 9*time.Millisecond, 2*time.Millisecond)
	o.Glass(8, 5*time.Millisecond, 0)
	if g := cc.glassW.summary(); g.Samples != 2 {
		t.Fatalf("glass samples %d, want 2", g.Samples)
	}
	e := cc.e2eGlassW.summary()
	if e.Samples != 1 || e.P50 < 17.9 || e.P50 > 19 {
		t.Fatalf("capture to glass %+v, want one sample of about 18 ms", e)
	}
	if p := cc.presentW.summary(); p.Samples != 1 || p.P50 != 1 {
		t.Fatalf("present %+v", p)
	}
}
