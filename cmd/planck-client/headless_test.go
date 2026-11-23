package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
)

// fakePipe stands in for capture and encode with paced, opaque frames.
type fakePipe struct {
	fps  int
	next int64
	n    int64
}

func (p *fakePipe) Next(_ time.Duration, forceKey bool, dst []byte) (encode.Packet, int64, error) {
	if p.next == 0 {
		p.next = transport.Mono()
	}
	transport.SleepUntilMono(p.next)
	p.next += int64(time.Second) / int64(p.fps)
	captured := transport.Mono()
	size := 8000
	if forceKey {
		size = 30_000
	}
	dst = dst[:0]
	for i := range size {
		dst = append(dst, byte(i))
	}
	p.n++
	return encode.Packet{Data: dst, Keyframe: forceKey, PTS: p.n}, captured, nil
}

func (p *fakePipe) SetBitrate(int) error       { return nil }
func (p *fakePipe) InvalidateRefs(int64) error { return encode.ErrUnsupported }
func (p *fakePipe) Info() session.VideoInfo {
	return session.VideoInfo{Width: 1920, Height: 1080, FPS: p.fps, Source: "fake"}
}
func (p *fakePipe) Close() error { return nil }

// toneCapturer delivers a 440 Hz tone in 20 ms chunks.
type toneCapturer struct{ wg sync.WaitGroup }

func (c *toneCapturer) Start(ctx context.Context, onPCM func([]int16, int64)) error {
	c.wg.Go(func() {
		pcm := make([]int16, 960*2)
		var phase float64
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for i := range 960 {
				v := int16(8000 * math.Sin(phase))
				pcm[2*i], pcm[2*i+1] = v, v
				phase += 2 * math.Pi * 440 / audio.SampleRate
			}
			onPCM(pcm, 0)
		}
	})
	return nil
}

func (c *toneCapturer) Close() error { c.wg.Wait(); return nil }

type noTailnet struct{}

func (noTailnet) PathInfo(context.Context, netip.Addr) (tailscale.PathInfo, error) {
	return tailscale.PathInfo{}, tailscale.ErrNotRunning
}
func (noTailnet) Status(context.Context) (*tailscale.Status, error) {
	return nil, tailscale.ErrNotRunning
}

// countSink accepts every access unit without decoding: the fake frames
// are not a bitstream.
type countSink struct{ units atomic.Int64 }

func (s *countSink) open() session.SinkOpener {
	return session.NullSinkOpener(&session.NullSink{OnUnit: func([]byte, int64) { s.units.Add(1) }})
}

type fakeHost struct {
	*session.Host
	inputs atomic.Int64
}

func startFakeHost(t *testing.T, discovery string) *fakeHost {
	t.Helper()
	fh := &fakeHost{}
	h, err := session.NewHost(session.HostConfig{
		ConfigDir: t.TempDir(), Name: "fake-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: discovery,
		FPS: 120, BitrateKbps: 40_000, Slices: 2, NoTailscale: true, PIN: "246810",
		OpenVideo: func(p session.VideoParams) (session.VideoPipe, error) { return &fakePipe{fps: p.FPS}, nil },
		OpenAudio: func() (audio.Capturer, error) { return &toneCapturer{}, nil },
		OnInput:   func(input.Event) { fh.inputs.Add(1) },
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	fh.Host = h
	return fh
}

func headlessOpts(t *testing.T, args ...string) options {
	t.Helper()
	o, err := parseOptions(append([]string{"-headless", "-config", t.TempDir()}, args...), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// TestHeadlessLoopback runs the headless benchmark against an in-process
// host over real QUIC and AEAD UDP: PIN pairing, video, silent audio and
// the input probe, then the summary.
func TestHeadlessLoopback(t *testing.T) {
	h := startFakeHost(t, "off")
	sink := &countSink{}
	deps := headlessDeps{openSink: sink.open(), tailscale: noTailnet{}, logf: t.Logf}

	o := headlessOpts(t, "-connect", h.Addr().String(), "-duration", "2500ms", "-silent-audio", "-input-probe", "-fps", "120")
	if _, err := headless(context.Background(), o, deps); !errors.Is(err, transport.ErrAuthDenied) {
		t.Fatalf("unpaired, no PIN: %v, want ErrAuthDenied", err)
	}

	o = headlessOpts(t, "-connect", h.Addr().String(), "-pin", "246810", "-duration", "2500ms", "-silent-audio", "-input-probe",
		"-fps", "120", "-codec", "hevc", "-path-mode", "lan")
	s, err := headless(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(s)
	t.Logf("summary: %s", j)
	switch {
	case s.PathKind != "lan-direct" || s.Codec != "HEVC" || s.Chroma != "4:2:0":
		t.Errorf("identity: %+v", s)
	case s.FramesDecoded < 200 || s.FPS < 90 || s.FPS > 130 || s.BitrateMbps <= 0:
		t.Errorf("throughput: %d frames %.1f fps %.1f Mbps", s.FramesDecoded, s.FPS, s.BitrateMbps)
	case s.AudioPackets == 0 || s.InputSent < 2 || h.inputs.Load() < 2:
		t.Errorf("audio %d packets, input %d sent %d received", s.AudioPackets, s.InputSent, h.inputs.Load())
	case s.E2EMs.P50 <= 0 || s.E2EMs.P99 < s.E2EMs.P50 || s.EncodeMs.P50 <= 0 || s.NetRTTMs.P50 <= 0 || s.NetOneWayMs.P99 <= 0:
		t.Errorf("latency: %+v", s)
	}
	if got := sink.units.Load(); uint64(got) != s.FramesDecoded {
		t.Errorf("sink saw %d units, summary %d", got, s.FramesDecoded)
	}
}

// TestHeadlessDiscover finds the host by name through a discovery probe
// and races its path, then checks -path-mode filters paths.
func TestHeadlessDiscover(t *testing.T) {
	h := startFakeHost(t, "127.0.0.1:0")
	disc := h.DiscoveryAddr().String()
	sink := &countSink{}
	deps := headlessDeps{openSink: sink.open(), tailscale: noTailnet{}, logf: t.Logf}

	o := headlessOpts(t, "-discover", "fake-host", "-discovery-addrs", disc, "-pin", "246810", "-duration", "1s", "-no-audio")
	s, err := headless(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if s.HostID != h.HostID() || s.PathKind != "lan-direct" || s.FramesDecoded == 0 || s.AudioPackets != 0 {
		t.Fatalf("summary: %+v", s)
	}

	o = headlessOpts(t, "-discover", "fake-host", "-discovery-addrs", disc, "-path-mode", "tailscale", "-timeout", "1s", "-duration", "1s")
	if _, err := headless(context.Background(), o, deps); !errors.Is(err, errHostNotFound) {
		t.Fatalf("tailscale only, host on the LAN: %v, want errHostNotFound", err)
	}
}

func TestParseOptions(t *testing.T) {
	o, err := parseOptions([]string{"-headless", "-connect", "10.0.0.2", "-codec", "av1", "-chroma", "yuv444p10", "-fps", "240",
		"-bitrate", "80000", "-pin", "012345", "-json", "-duration", "5s", "-path-mode", "tailscale"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !o.headless || o.codec != encode.AV1 || o.chroma != encode.YUV444P10 || o.fps != 240 || o.bitrateKbps != 80_000 ||
		o.pin != "012345" || !o.json || o.duration != 5*time.Second || o.pathMode != pathTailscale {
		t.Errorf("got %+v", o)
	}
	if got := withDefaultPort(o.connect); got != "10.0.0.2:47701" {
		t.Errorf("default port: %q", got)
	}
	if got := withDefaultPort("[fd7a::1]:5000"); got != "[fd7a::1]:5000" {
		t.Errorf("explicit port: %q", got)
	}
	for _, args := range [][]string{
		{"-headless"},
		{"-headless", "-connect", "a", "-discover", "b"},
		{"-connect", "a"},
		{"-headless", "-connect", "a", "-pin", "12345"},
		{"-headless", "-connect", "a", "-no-audio", "-silent-audio"},
		{"-headless", "-connect", "a", "-path-mode", "derp"},
		{"-headless", "-connect", "a", "-codec", "vp9"},
		{"-headless", "-connect", "a", "-discovery-addrs", "nope"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

// TestHeadlessE2EBinaries builds planck-host and planck-client and streams
// the host's synthetic pattern through this machine's hardware encoder
// and decoder over loopback: no display capture, no input injection, no
// audio device, no window.
func TestHeadlessE2EBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("builds both commands and uses the GPU")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("needs a hardware encoder and decoder")
	}
	bin := t.TempDir()
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return filepath.Join(bin, name)
	}
	build := exec.Command("go", "build", "-o", bin, "../planck-host", ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	discPort := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := exec.CommandContext(ctx, exe("planck-host"), "-synth", "-fps", "120", "-codec", "hevc", "-no-audio", "-no-input",
		"-listen", "127.0.0.1:0", "-media-port", "0", "-discovery-port", fmt.Sprint(discPort), "-pin", "135790",
		"-name", "e2e-host", "-config", t.TempDir(), "-stats", "0")
	stdout, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var hostLog bytes.Buffer
	host.Stderr = &hostLog
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		host.Wait()
		t.Logf("planck-host log:\n%s", hostLog.String())
	}()
	addr := ""
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		t.Logf("host: %s", sc.Text())
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "listening" {
			addr = f[1]
			break
		}
	}
	if addr == "" {
		t.Fatal("planck-host printed no listening address")
	}
	go io.Copy(io.Discard, stdout)

	run := func(args ...string) Summary {
		t.Helper()
		cmd := exec.Command(exe("planck-client"), append([]string{"-headless", "-json", "-no-audio", "-config", t.TempDir(), "-pin", "135790"}, args...)...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("planck-client %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, out.String(), errOut.String())
		}
		var s Summary
		if err := json.Unmarshal(out.Bytes(), &s); err != nil {
			t.Fatalf("summary: %v\n%s", err, out.String())
		}
		t.Logf("planck-client %v:\n%s", args, out.String())
		return s
	}
	byAddr := run("-connect", addr, "-duration", "5s")
	found := run("-discover", "e2e-host", "-discovery-addrs", fmt.Sprintf("127.0.0.1:%d", discPort), "-duration", "5s", "-path-mode", "lan")
	if found.Host != "e2e-host" || found.HostID == "" {
		t.Errorf("discovered host: %q %q", found.Host, found.HostID)
	}
	for _, s := range []Summary{byAddr, found} {
		switch {
		case s.PathKind != "lan-direct" || s.Codec != "HEVC" || s.Width != 1920 || s.Height != 1080:
			t.Errorf("stream: %+v", s)
		// The client's decoder starts cold while frames already arrive: a
		// few drop, and the pictures referencing them fail until the
		// keyframe they ask for. Steady state is what this checks.
		case s.FramesDecoded < 300 || s.FPS < 90 || s.SinkErrors*20 > s.FramesDecoded:
			t.Errorf("decode: %d frames %.1f fps %d sink errors", s.FramesDecoded, s.FPS, s.SinkErrors)
		case s.EncodeMs.P50 <= 0 || s.DecodeMs.P50 <= 0 || s.E2EMs.P50 <= 0 || s.E2EMs.P50 > 50:
			t.Errorf("latency: encode %+v decode %+v e2e %+v", s.EncodeMs, s.DecodeMs, s.E2EMs)
		}
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}
