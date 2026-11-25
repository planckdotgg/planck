package session

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/encode"
)

const (
	display1 = `\\.\DISPLAY1`
	display2 = `\\.\DISPLAY2`
)

// twoDisplayCaps is testHostCaps with a second, HDR, 1440p display.
func twoDisplayCaps(vdd bool) func() defaults.HostCapabilities {
	return func() defaults.HostCapabilities {
		c := testHostCaps()
		c.Displays[0].Name = "Dell U2723QE"
		c.Displays = append(c.Displays, defaults.HostDisplay{ID: display2, Name: "LG OLED", Width: 2560, Height: 1440, RefreshHz: 120,
			Adapter: testGPU.LUID, HDR: true})
		if vdd {
			c.VDD = defaults.VDDCap{Available: true, Protocol: "0.2.1", CanSetRenderAdapter: true}
		}
		return c
	}
}

type displayHost struct {
	h      *Host
	pipes  *pipeLog
	vdd    *fakeVDD
	forced *atomic.Int64
}

func startDisplayHost(t *testing.T, caps func() defaults.HostCapabilities) *displayHost {
	t.Helper()
	d := &displayHost{pipes: &pipeLog{}, vdd: &fakeVDD{}, forced: new(atomic.Int64)}
	var kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "display-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: caps, OpenVirtualDisplay: d.vdd.open, Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			d.pipes.mu.Lock()
			d.pipes.opens = append(d.pipes.opens, p)
			d.pipes.mu.Unlock()
			return &fakePipe{fps: p.FPS, forced: d.forced, kbps: &kbps}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	d.h = h
	return d
}

func connectDisplayClient(t *testing.T, d *displayHost, caps defaults.ClientCapabilities, sinks *switchSinks) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "display-client", PIN: d.h.PIN(), NoAudio: true,
		Capabilities: func() defaults.ClientCapabilities { return caps },
		OpenSink:     sinks.opener, OpenAudioPlayer: func() (audio.Player, error) { return &fakePlayer{}, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
		ViewSize: func() (int, int) { return 1600, 900 },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: d.h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *Client) sessionID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.session().ID()
}

// TestSwitchDisplayLive: the client hears of every host display, and
// moves the stream between two physical ones and back in one session,
// each move opening the new display's pipeline at its size, with a
// keyframe and a new stream epoch. An ID the host does not have changes
// nothing.
func TestSwitchDisplayLive(t *testing.T) {
	d := startDisplayHost(t, twoDisplayCaps(false))
	sinks := &switchSinks{sink: &NullSink{}}
	c := connectDisplayClient(t, d, testClientCaps(), sinks)
	units := func() uint64 { return sinks.sink.Units.Load() }
	waitFor(t, "frames", func() bool { return units() >= 5 })

	ds := c.HostDisplays()
	if len(ds) != 2 || ds[0].Name != "Dell U2723QE" || ds[1].ID != display2 || !ds[1].HDR || ds[1].Width != 2560 {
		t.Fatalf("host displays %+v", ds)
	}
	if got := c.StreamedDisplay(); got != display1 {
		t.Fatalf("streams %q at the start, want the primary", got)
	}
	sess := c.sessionID()

	move := func(id string, w, h int) {
		t.Helper()
		_, opens := d.pipes.last()
		keys := d.forced.Load()
		if err := c.SwitchDisplay(id); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the stream on "+id, func() bool { return c.StreamedDisplay() == id })
		waitFor(t, "the pipeline of "+id, func() bool { p, n := d.pipes.last(); return n > opens && p.Display == id })
		p, _ := d.pipes.last()
		if p.VirtualDisplay || p.Width != w || p.Height != h {
			t.Errorf("%s pipeline %+v, want physical %dx%d", id, p, w, h)
		}
		n := units()
		waitFor(t, "video of "+id, func() bool { return units() >= n+5 })
		if d.forced.Load() <= keys {
			t.Errorf("%s: no keyframe after the switch", id)
		}
		if prof := c.Stats().Profile; prof == nil || prof.Width != w || prof.Height != h {
			t.Errorf("%s: client profile %+v", id, prof)
		}
	}
	move(display2, 2560, 1440)
	epoch := c.conn.stream.Load().epoch
	move(display1, 1920, 1080)
	if e := c.conn.stream.Load().epoch; e != epoch+1 {
		t.Errorf("epoch %d after the second switch, want %d", e, epoch+1)
	}

	_, opens := d.pipes.last()
	if err := c.SwitchDisplay(`\\.\DISPLAY9`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, n := d.pipes.last(); n != opens || c.StreamedDisplay() != display1 {
		t.Errorf("an unknown display reopened the pipeline (%d opens, was %d) or moved the stream to %q", n, opens, c.StreamedDisplay())
	}
	if c.sessionID() != sess {
		t.Error("a display switch changed sessions")
	}
	select {
	case <-c.Done():
		t.Fatalf("stream ended: %v", c.Err())
	default:
	}
}

// TestSwitchDisplayFromVirtual: a stream on a virtual display that moves
// to a physical display removes the virtual one.
func TestSwitchDisplayFromVirtual(t *testing.T) {
	d := startDisplayHost(t, twoDisplayCaps(true))
	sinks := &switchSinks{sink: &NullSink{}}
	c := connectDisplayClient(t, d, testClientCaps(), sinks)
	live := func() int { d.vdd.mu.Lock(); defer d.vdd.mu.Unlock(); return d.vdd.live }
	waitFor(t, "the virtual display", func() bool { return live() == 1 })
	if c.StreamedDisplay() != "" {
		t.Fatalf("a virtual display stream names display %q", c.StreamedDisplay())
	}
	if err := c.SwitchDisplay(display2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stream on display 2", func() bool { return c.StreamedDisplay() == display2 })
	waitFor(t, "the virtual display removed", func() bool { return live() == 0 })
	if p, _ := d.pipes.last(); p.Display != display2 || p.VirtualDisplay {
		t.Errorf("pipeline %+v, want physical display 2", p)
	}
}

// TestHDR10Negotiated: an HDR client display that presents HDR10 picks
// the host's HDR display and gets a 10-bit HDR10 stream: the pipeline is
// asked for HDR10 and the client's sink is told the pictures are PQ.
// Moving to the SDR display opens an SDR sink.
func TestHDR10Negotiated(t *testing.T) {
	d := startDisplayHost(t, twoDisplayCaps(false))
	sinks := &switchSinks{sink: &NullSink{}}
	caps := testClientCaps()
	caps.Display.HDR, caps.PresentHDR10 = true, true
	c := connectDisplayClient(t, d, caps, sinks)
	waitFor(t, "frames", func() bool { return sinks.sink.Units.Load() >= 3 })
	if p, _ := d.pipes.last(); p.HDR10 {
		t.Fatal("the SDR primary display streams HDR10")
	}
	if err := c.SwitchDisplay(display2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the HDR sink", func() bool { last, _, _ := sinks.state(); return last.HDR10 })
	p, _ := d.pipes.last()
	if !p.HDR10 || !p.Chroma.TenBit() {
		t.Errorf("HDR display pipeline %+v, want 10-bit HDR10", p)
	}
	if prof := c.Stats().Profile; prof == nil || !prof.HDR10 || prof.BitDepth != 10 {
		t.Errorf("client profile %+v", prof)
	}
	if err := c.SwitchDisplay(display1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the SDR sink", func() bool { last, _, _ := sinks.state(); return !last.HDR10 && c.StreamedDisplay() == display1 })
}

// TestHDRSourceNeedsPQCapture: only a 10-bit RGB capture carries the PQ
// an HDR10 stream signals.
func TestHDRSourceNeedsPQCapture(t *testing.T) {
	p := VideoParams{HDR10: true, Chroma: encode.P010}
	if !hdrSource(p, capture.FormatRGB10A2) {
		t.Error("RGB10A2 of an HDR desktop is HDR10")
	}
	for _, f := range []capture.Format{capture.FormatBGRA8, capture.FormatRGBA16F} {
		if hdrSource(p, f) {
			t.Errorf("%v is not PQ", f)
		}
	}
	if hdrSource(VideoParams{HDR10: true, Chroma: encode.NV12}, capture.FormatRGB10A2) {
		t.Error("an 8-bit encode is not HDR10")
	}
}
