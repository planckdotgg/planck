package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/defaults"
)

// fakeVDD stands in for SudoVDA: it counts the displays alive and records
// every size one took.
type fakeVDD struct {
	mu         sync.Mutex
	live, made int
	sizes      [][2]int
	failResize bool
}

type fakeDisplay struct {
	v      *fakeVDD
	name   string
	closed bool
}

func (v *fakeVDD) open(p VideoParams, _ []byte, _ bool) (VirtualDisplay, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.live++
	v.made++
	v.sizes = append(v.sizes, [2]int{p.Width, p.Height})
	return &fakeDisplay{v: v, name: fmt.Sprintf(`\\.\DISPLAY%d`, 40+v.made)}, nil
}

func (v *fakeVDD) state() (live, made int, last [2]int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.live, v.made, v.sizes[len(v.sizes)-1]
}

func (d *fakeDisplay) Name() string { return d.name }

func (d *fakeDisplay) Resize(w, h int) error {
	d.v.mu.Lock()
	defer d.v.mu.Unlock()
	if d.v.failResize {
		d.closeLocked()
		return errors.New("fake resize failed")
	}
	d.v.sizes = append(d.v.sizes, [2]int{w, h})
	return nil
}

func (d *fakeDisplay) Close() error {
	d.v.mu.Lock()
	defer d.v.mu.Unlock()
	d.closeLocked()
	return nil
}

func (d *fakeDisplay) closeLocked() {
	if !d.closed {
		d.closed = true
		d.v.live--
	}
}

func vddHostCaps() defaults.HostCapabilities {
	c := testHostCaps()
	c.VDD = defaults.VDDCap{Available: true, Protocol: "0.2.1", CanSetRenderAdapter: true}
	return c
}

// pipeLog records the params of every pipeline the host opened.
type pipeLog struct {
	mu    sync.Mutex
	opens []VideoParams
}

func (l *pipeLog) last() (VideoParams, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.opens[len(l.opens)-1], len(l.opens)
}

func startVDDHost(t *testing.T, vdd *fakeVDD, pipes *pipeLog) *Host {
	t.Helper()
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "vdd-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: vddHostCaps, OpenVirtualDisplay: vdd.open, Logf: t.Logf,
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			pipes.mu.Lock()
			pipes.opens = append(pipes.opens, p)
			pipes.mu.Unlock()
			return &fakePipe{fps: p.FPS, forced: &forced, kbps: &kbps}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

// windowClient is capsClient with a stream window of view's size; a nil
// view is a headless client.
func windowClient(t *testing.T, pin string, o defaults.UserOverrides, view *atomic.Uint64) *clientRig {
	t.Helper()
	r := &clientRig{sink: &NullSink{}, player: &fakePlayer{}}
	r.sink.OnUnit = func(au []byte, _ int64) {
		if !checkFrame(au) {
			r.corrupt.Add(1)
		}
	}
	cfg := ClientConfig{
		ConfigDir: t.TempDir(), Name: "window-client", PIN: pin, NoAudio: true, Overrides: o, Capabilities: testClientCaps,
		OpenSink: NullSinkOpener(r.sink), OpenAudioPlayer: func() (audio.Player, error) { return r.player, nil },
		Tailscale: noTailscale{}, Logf: t.Logf,
	}
	if view != nil {
		cfg.ViewSize = func() (int, int) { v := view.Load(); return int(v >> 32), int(uint32(v)) }
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Disconnect)
	r.client = c
	return r
}

func decisionOf(p *defaults.ResolvedProfile, knob string) defaults.Decision {
	for _, d := range p.Decisions {
		if d.Knob == knob {
			return d
		}
	}
	return defaults.Decision{}
}

func profileSize(r *clientRig) (int, int) {
	p := r.client.Stats().Profile
	if p == nil {
		return 0, 0
	}
	return p.Width, p.Height
}

// TestResizeFollowsWindow: a stream on a virtual display starts at the
// window's size and follows its resizes on the one display, the pipeline
// reopening on it; a size already asked for sends nothing; the display
// goes with the session.
func TestResizeFollowsWindow(t *testing.T) {
	vdd, pipes := &fakeVDD{}, &pipeLog{}
	h := startVDDHost(t, vdd, pipes)
	var view atomic.Uint64
	view.Store(packView(1280, 720))
	r := windowClient(t, h.PIN(), defaults.UserOverrides{}, &view)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 10 })
	if w, hh := profileSize(r); w != 1280 || hh != 720 {
		t.Fatalf("initial profile %dx%d, want the 1280x720 window", w, hh)
	}
	if live, _, last := vdd.state(); live != 1 || last != [2]int{1280, 720} {
		t.Fatalf("initial display: %d live, %v", live, last)
	}

	if err := r.client.Resize(1601, 1001); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the 1600x1000 profile", func() bool { w, hh := profileSize(r); return w == 1600 && hh == 1000 })
	if d := decisionOf(r.client.Stats().Profile, "resolution"); d.Value != "1600x1000" || d.Why != "client window" {
		t.Fatalf("resolution decision after the resize: %+v", d)
	}
	waitFor(t, "the pipeline at 1600x1000", func() bool { p, _ := pipes.last(); return p.Width == 1600 && p.Height == 1000 })
	p, opens := pipes.last()
	if live, made, last := vdd.state(); live != 1 || made != 1 || last != [2]int{1600, 1000} || p.Display != `\\.\DISPLAY41` {
		t.Fatalf("after resize: %d live of %d made, last %v, pipeline on %q", live, made, last, p.Display)
	}
	units := r.sink.Units.Load()
	waitFor(t, "video after the resize", func() bool { return r.sink.Units.Load() >= units+10 })

	for i := range 10 {
		if err := r.client.Resize(1700+20*i, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.client.Resize(1920, 1200); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the 1920x1200 profile", func() bool { w, hh := profileSize(r); return w == 1920 && hh == 1200 })
	waitFor(t, "the pipeline at 1920x1200", func() bool { p, _ := pipes.last(); return p.Width == 1920 && p.Height == 1200 })
	if live, made, _ := vdd.state(); live != 1 || made != 1 {
		t.Fatalf("after a burst: %d live of %d made", live, made)
	}

	_, opens = pipes.last()
	_, _, before := vdd.state()
	if err := r.client.Resize(1921, 1201); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, n := pipes.last(); n != opens {
		t.Fatalf("the same aligned size reopened the pipeline: %d opens, %d before", n, opens)
	}
	if _, _, last := vdd.state(); last != before {
		t.Fatalf("the same aligned size resized the display to %v", last)
	}
	if n := r.corrupt.Load(); n != 0 {
		t.Fatalf("%d corrupt units", n)
	}

	r.client.Disconnect()
	waitFor(t, "the display removed", func() bool { live, _, _ := vdd.state(); return live == 0 })
}

// TestResizeKeepsExplicitResolution: an explicit resolution starts at its
// own size and does not follow the window; a headless client streams at
// the display's size.
func TestResizeKeepsExplicitResolution(t *testing.T) {
	vdd, pipes := &fakeVDD{}, &pipeLog{}
	h := startVDDHost(t, vdd, pipes)
	var view atomic.Uint64
	view.Store(packView(1280, 720))
	r := windowClient(t, h.PIN(), defaults.UserOverrides{Resolution: "1440p"}, &view)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 10 })
	if w, hh := profileSize(r); w != 2560 || hh != 1440 {
		t.Fatalf("1440p override: %dx%d", w, hh)
	}
	_, opens := pipes.last()
	if err := r.client.Resize(1600, 1000); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, n := pipes.last(); n != opens {
		t.Fatalf("an explicit resolution followed the window: %d opens, %d before", n, opens)
	}
	if w, hh := profileSize(r); w != 2560 || hh != 1440 {
		t.Fatalf("1440p override after a resize: %dx%d", w, hh)
	}
	r.client.Disconnect()

	headless := windowClient(t, h.PIN(), defaults.UserOverrides{}, nil)
	if err := connect(t, headless, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if w, hh := profileSize(headless); w != 1920 || hh != 1080 {
		t.Fatalf("headless: %dx%d, want the client display's 1920x1080", w, hh)
	}
}

// TestResizeFailureLeavesOneDisplay: a display that fails to resize is
// gone, and the stream goes on on a new one at the new size.
func TestResizeFailureLeavesOneDisplay(t *testing.T) {
	vdd, pipes := &fakeVDD{failResize: true}, &pipeLog{}
	h := startVDDHost(t, vdd, pipes)
	var view atomic.Uint64
	view.Store(packView(1280, 720))
	r := windowClient(t, h.PIN(), defaults.UserOverrides{}, &view)
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "video frames", func() bool { return r.sink.Units.Load() >= 10 })
	if err := r.client.Resize(1600, 1000); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a new display at 1600x1000", func() bool { _, made, last := vdd.state(); return made == 2 && last == [2]int{1600, 1000} })
	waitFor(t, "the pipeline on it", func() bool { p, _ := pipes.last(); return p.Display == `\\.\DISPLAY42` && p.Width == 1600 })
	if live, _, _ := vdd.state(); live != 1 {
		t.Fatalf("%d displays live after a failed resize", live)
	}
	units := r.sink.Units.Load()
	waitFor(t, "video on the new display", func() bool { return r.sink.Units.Load() >= units+10 })
	r.client.Disconnect()
	waitFor(t, "the display removed", func() bool { live, _, _ := vdd.state(); return live == 0 })
}
