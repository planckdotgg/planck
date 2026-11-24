package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/transport"
)

// stagedPipe is a fakePipe whose pictures were presented capture before
// it acquires them and take encode to encode, as sourcePipe reports.
type stagedPipe struct {
	fakePipe
	capture, encode time.Duration
	acquired        int64
}

func (p *stagedPipe) Next(timeout time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	pkt, _, err := p.fakePipe.Next(timeout, forceKey, dst)
	p.acquired = transport.Mono()
	transport.SleepUntilMono(p.acquired + int64(p.encode))
	return pkt, p.acquired - int64(p.capture), err
}

func (p *stagedPipe) stages() pipeStages { return pipeStages{Acquired: p.acquired} }

// TestHostStatsSplitEncode: the host tells the client how much of the
// encode stage the video headers show was capture, and the client reports
// the two apart.
func TestHostStatsSplitEncode(t *testing.T) {
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 120, BitrateKbps: 40_000, Slices: 2, NoTailscale: true,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &stagedPipe{fakePipe: fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps},
				capture: 3 * time.Millisecond, encode: 2 * time.Millisecond}, nil
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
	waitFor(t, "host stats", func() bool { return r.client.Stats().HostEncodeMs > 0 })
	cs := r.client.Stats()
	t.Logf("capture %.2f ms, host encode %.2f ms, encode as the headers show it %.2f ms", cs.CaptureMs, cs.HostEncodeMs, cs.Encode.P50)
	if cs.CaptureMs < 2.5 || cs.CaptureMs > 4 {
		t.Errorf("capture %.2f ms, want about 3", cs.CaptureMs)
	}
	if cs.HostEncodeMs < 1.5 || cs.HostEncodeMs > 6 {
		t.Errorf("host encode %.2f ms, want about 2", cs.HostEncodeMs)
	}
	if cs.Encode.P50 < cs.CaptureMs+cs.HostEncodeMs-1 {
		t.Errorf("headers' encode %.2f ms is less than capture plus encode", cs.Encode.P50)
	}
}
