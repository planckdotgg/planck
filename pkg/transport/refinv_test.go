package transport

import (
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestRefInvalidationSendsNoKeyframeRequest: a frame lost on the wire
// makes the client session send a PLI and the host's rate controller ask
// for a keyframe, unless both ends offered wire.FeatureRefInvalidate:
// then the application recovers with RefInvalidate and neither happens.
func TestRefInvalidationSendsNoKeyframeRequest(t *testing.T) {
	for _, refInv := range []bool{false, true} {
		p := newPair(t, pairOpts{
			server: func(c *ServerConfig) { c.RefInvalidation = refInv },
			client: func(c *ClientConfig) {
				c.RefInvalidation = refInv
				c.Handlers.VideoFrame = func(wire.VideoInfo, []byte) {}
			},
		})
		if p.host.RefInvalidation() != refInv || p.client.RefInvalidation() != refInv {
			t.Fatalf("offered %v: host %v, client %v", refInv, p.host.RefInvalidation(), p.client.RefInvalidation())
		}
		const lost = 3
		send := p.host.emitVideo
		p.host.emitVideo = func(pkt []byte) error {
			var h wire.VideoHeader
			if h.Parse(pkt[AEADHeaderLen:]) == nil && h.FrameID == lost && h.ShardIndex%2 == 0 {
				return nil
			}
			return send(pkt)
		}
		frame := make([]byte, 20_000)
		for id := uint32(1); id <= 30; id++ {
			if err := p.host.SendVideoFrame(wire.VideoInfo{FrameID: id, SliceCount: 1}, frame); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Millisecond)
		}
		deadline := time.Now().Add(2 * time.Second)
		for p.client.Stats().FramesLost == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		cs, key := p.client.Stats(), p.host.KeyframeRequested()
		t.Logf("reference invalidation %v: frames lost %d, PLIs sent %d, host keyframe requested %v", refInv, cs.FramesLost, cs.PLIsSent, key)
		if cs.FramesLost == 0 {
			t.Fatal("frame 3 was not lost")
		}
		if refInv && (cs.PLIsSent != 0 || key) {
			t.Fatalf("with reference invalidation: %d PLIs, keyframe requested %v", cs.PLIsSent, key)
		}
		if !refInv && (cs.PLIsSent == 0 || !key) {
			t.Fatalf("without reference invalidation: %d PLIs, keyframe requested %v; want both", cs.PLIsSent, key)
		}
	}
}
