//go:build windows

package session

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/internal/win"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/vdd"
)

// sizeSink decodes on the GPU and keeps the size of the last picture.
type sizeSink struct {
	*decodeSink
	last atomic.Uint64
}

func (s *sizeSink) Submit(au []byte, pts int64) (player.Timing, error) {
	f, err := s.dec.Decode(au, pts)
	if err != nil {
		return player.Timing{PTS: pts}, err
	}
	s.last.Store(packView(f.Width, f.Height))
	s.dec.Release(f)
	return player.Timing{PTS: f.PTS, Decode: f.Latency}, nil
}

var procEnumDisplaySettingsW = syscall.NewLazyDLL("user32.dll").NewProc("EnumDisplaySettingsW")

// displayOrigin is the desktop position of the display named gdi.
func displayOrigin(gdi string) (int32, int32) {
	var dm [220]byte
	*(*uint16)(unsafe.Pointer(&dm[68])) = uint16(len(dm)) // dmSize
	name, _ := syscall.UTF16PtrFromString(gdi)
	const enumCurrentSettings = ^uintptr(0)
	procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(name)), enumCurrentSettings, uintptr(unsafe.Pointer(&dm[0])))
	return *(*int32)(unsafe.Pointer(&dm[76])), *(*int32)(unsafe.Pointer(&dm[80]))
}

// TestLiveVirtualDisplayResize streams a real SudoVDA display through the
// GPU's encoder to a client on loopback that decodes on the GPU, resizes
// the client's window, and checks each size reaches the decoder from one
// display, and that no SudoVDA monitor is left once the client leaves. It
// adds a monitor to the desktop: run it with PLANCK_LIVE_VDD_TEST=1
// [PLANCK_LIVE_VDD_GPU=4080] [PLANCK_LIVE_VDD_ADDR=127.0.0.1:41100, media
// on the next port].
func TestLiveVirtualDisplayResize(t *testing.T) {
	if os.Getenv("PLANCK_LIVE_VDD_TEST") != "1" {
		t.Skip("set PLANCK_LIVE_VDD_TEST=1 to add a real SudoVDA display")
	}
	gpuName := os.Getenv("PLANCK_LIVE_VDD_GPU")
	if gpuName == "" {
		gpuName = "4080"
	}
	addr := os.Getenv("PLANCK_LIVE_VDD_ADDR")
	if addr == "" {
		addr = "127.0.0.1:41100"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	media, _ := strconv.Atoi(port)
	before, err := vdd.Monitors()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(HostConfig{ConfigDir: t.TempDir(), Name: "live-vdd-host", ListenAddr: addr, MediaPort: media + 1,
		DiscoveryAddr: "off", Adapter: gpuName, NoAudio: true, NoTailscale: true, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	var view atomic.Uint64
	view.Store(packView(1280, 720))
	sink := &sizeSink{}
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "live-vdd-client", PIN: h.PIN(), NoAudio: true, Tailscale: noTailscale{},
		Overrides: defaults.UserOverrides{VirtualDisplay: "on", Codec: "hevc"},
		ViewSize:  func() (int, int) { v := view.Load(); return int(v >> 32), int(uint32(v)) },
		OpenSink: func(cfg decode.Config) (Sink, error) {
			s, err := OpenDecodeSink(cfg)
			if err != nil {
				return nil, err
			}
			sink.decodeSink = s.(*decodeSink)
			return sink, nil
		},
		OpenAudioPlayer: func() (audio.Player, error) { return nil, audio.ErrClosed },
		Logf:            t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: addr}); err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()

	waitLive := func(what string, limit time.Duration, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(limit); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %s after %v", what, limit)
			}
		}
	}
	for i, s := range [][2]int{{1280, 720}, {1734, 968}, {1600, 900}, {2532, 1418}} {
		start := time.Now()
		if i > 0 {
			if err := c.Resize(s[0], s[1]); err != nil {
				t.Fatal(err)
			}
		}
		waitLive("host video at the new size", 15*time.Second, func() bool {
			hs := h.Stats()
			return hs.VirtualDisplay != "" && hs.Video.Width == s[0] && hs.Video.Height == s[1]
		})
		hs := h.Stats()
		x, y := displayOrigin(hs.VirtualDisplay)
		stop, done := make(chan struct{}), make(chan struct{})
		go func() { defer close(done); win.Animate(x+50, y+50, 96, stop) }()
		waitLive("a decoded picture at the new size", 10*time.Second, func() bool { return sink.last.Load() == packView(s[0], s[1]) })
		close(stop)
		<-done
		ms, err := vdd.Monitors()
		if err != nil {
			t.Fatal(err)
		}
		if len(ms) != len(before)+1 {
			t.Fatalf("%d SudoVDA monitors at %dx%d, %d before the test", len(ms), s[0], s[1], len(before))
		}
		cs := c.Stats()
		if cs.Profile == nil || cs.Profile.Width != s[0] || cs.Profile.Height != s[1] {
			t.Fatalf("client profile %+v at %dx%d", cs.Profile, s[0], s[1])
		}
		t.Logf("%dx%d on %s decoded %v after the request; sink errors %d", s[0], s[1], hs.VirtualDisplay, time.Since(start), cs.SinkErrors)
	}
	c.Disconnect()
	waitLive("removal of the virtual display", 6*time.Second, func() bool {
		ms, err := vdd.Monitors()
		return err == nil && len(ms) == len(before)
	})
	h.Stop()
	if ms, err := vdd.Monitors(); err != nil || len(ms) != len(before) {
		t.Fatalf("%d SudoVDA monitors after the host stopped (%v), %d before", len(ms), err, len(before))
	}
}
