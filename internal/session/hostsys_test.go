package session

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/input"
)

// fakeSystem records the OS actions a host takes; none touches the
// machine.
type fakeSystem struct {
	mu                                     sync.Mutex
	awake, released, mutes, unmutes, locks int
	isolated, restored                     []string
	sas                                    int
}

func (f *fakeSystem) count(n *int) int { f.mu.Lock(); defer f.mu.Unlock(); return *n }

func (f *fakeSystem) hooks() SystemHooks {
	bump := func(n *int) { f.mu.Lock(); *n++; f.mu.Unlock() }
	return SystemHooks{
		KeepAwake: func() (func(), error) {
			bump(&f.awake)
			return func() { bump(&f.released) }, nil
		},
		Lock: func() error { bump(&f.locks); return nil },
		MuteSpeakers: func() (func() error, error) {
			bump(&f.mutes)
			return func() error { bump(&f.unmutes); return nil }, nil
		},
		Isolate: func(name string) (func() error, error) {
			f.mu.Lock()
			f.isolated = append(f.isolated, name)
			f.mu.Unlock()
			return func() error {
				f.mu.Lock()
				f.restored = append(f.restored, name)
				f.mu.Unlock()
				return nil
			}, nil
		},
		AppPIDs: func(names []string) ([]int, error) { return []int{4242}, nil },
		SendSAS: func() error { bump(&f.sas); return nil },
	}
}

func startSysHost(t *testing.T, s SessionSettings, sys *fakeSystem, tweak func(*Host)) *Host {
	t.Helper()
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "test-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		FPS: 60, BitrateKbps: 20_000, NoTailscale: true, Session: s, System: sys.hooks(),
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
			return &fakePipe{fps: p.FPS, forced: new(atomic.Int64), kbps: new(atomic.Int64)}, nil
		},
		OpenAudio: func() (audio.Capturer, error) { return &fakeCapturer{}, nil },
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tweak != nil {
		tweak(h)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

// TestSessionSettingsLifecycle: hosting holds the machine awake, a stream
// mutes the speakers and always puts them back, and only a client that
// left on its own locks the machine.
func TestSessionSettingsLifecycle(t *testing.T) {
	sys := &fakeSystem{}
	h := startSysHost(t, SessionSettings{StayAwake: true, MuteSpeakers: true, LockOnDisconnect: true}, sys, nil)
	if sys.count(&sys.awake) != 1 {
		t.Fatalf("hosting took %d stay-awake holds, want 1", sys.count(&sys.awake))
	}
	addr := h.Addr().String()
	dir := t.TempDir()
	r := newClient(t, dir, h.PIN())
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the speakers to mute", func() bool { return sys.count(&sys.mutes) == 1 })
	r.client.Disconnect()
	waitFor(t, "the machine to lock", func() bool { return sys.count(&sys.locks) == 1 })
	if sys.count(&sys.unmutes) != 1 {
		t.Errorf("unmuted %d times after the client left, want 1", sys.count(&sys.unmutes))
	}

	r = newClient(t, dir, "")
	if err := connect(t, r, addr); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to run the stream", func() bool { return h.Stats().Connected })
	waitFor(t, "the speakers to mute again", func() bool { return sys.count(&sys.mutes) == 2 })
	if err := h.KickClient(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the kicked client's stream to end", func() bool {
		select {
		case <-r.client.Done():
			return true
		default:
			return false
		}
	})
	waitFor(t, "the speakers to come back", func() bool { return sys.count(&sys.unmutes) == 2 })
	if err := h.KickClient(); !errors.Is(err, ErrNoClient) {
		t.Errorf("kick with no client: %v, want ErrNoClient", err)
	}
	h.SetSessionSettings(SessionSettings{MuteSpeakers: true})
	if sys.count(&sys.released) != 1 {
		t.Errorf("turning stay awake off released %d holds, want 1", sys.count(&sys.released))
	}
	h.Stop()
	if sys.count(&sys.locks) != 1 || sys.count(&sys.awake) != 1 || sys.count(&sys.released) != 1 {
		t.Errorf("after the kick and stop: %d locks, %d holds, %d released; want 1 each", sys.count(&sys.locks), sys.count(&sys.awake), sys.count(&sys.released))
	}
}

// TestLiveMuteToggle: muting while a client streams takes effect at once,
// and unmuting puts the speakers back.
func TestLiveMuteToggle(t *testing.T) {
	sys := &fakeSystem{}
	h := startSysHost(t, SessionSettings{}, sys, nil)
	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to run the stream", func() bool { return h.Stats().Connected })
	if sys.count(&sys.mutes) != 0 {
		t.Fatal("muted with MuteSpeakers off")
	}
	h.SetSessionSettings(SessionSettings{MuteSpeakers: true})
	h.SetSessionSettings(SessionSettings{MuteSpeakers: true})
	if sys.count(&sys.mutes) != 1 {
		t.Errorf("%d mutes, want 1", sys.count(&sys.mutes))
	}
	h.SetSessionSettings(SessionSettings{})
	if sys.count(&sys.unmutes) != 1 {
		t.Errorf("%d unmutes, want 1", sys.count(&sys.unmutes))
	}
}

// TestIdleTimeout: on a clock the test moves, a client is idle only past
// the timeout since its last input, and an idle client is disconnected,
// which locks the machine when the settings say to.
func TestIdleTimeout(t *testing.T) {
	var clock atomic.Int64
	sys := &fakeSystem{}
	h := startSysHost(t, SessionSettings{IdleTimeout: time.Minute, LockOnDisconnect: true}, sys, func(h *Host) {
		h.clock, h.sys.idleTick = clock.Load, 5*time.Millisecond
	})
	if h.idleExpired(int64(59 * time.Second)) {
		t.Error("idle after 59 s of a 1 min timeout")
	}
	if !h.idleExpired(int64(61 * time.Second)) {
		t.Error("not idle after 61 s")
	}
	r := newClient(t, t.TempDir(), h.PIN())
	if err := connect(t, r, h.Addr().String()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to run the stream", func() bool { return h.Stats().Connected })
	clock.Store(int64(50 * time.Second))
	if err := r.client.SendInput(input.Event{Type: input.TypeMouseMoveRel, MouseRel: input.MouseMoveRel{DX: 1}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to see the input", func() bool { return h.lastInput.Load() == int64(50*time.Second) })
	clock.Store(int64(100 * time.Second))
	time.Sleep(50 * time.Millisecond)
	if !h.Stats().Connected {
		t.Fatal("disconnected 50 s after the last input")
	}
	clock.Store(int64(111 * time.Second))
	waitFor(t, "the idle client to be disconnected", func() bool {
		select {
		case <-r.client.Done():
			return true
		default:
			return false
		}
	})
	waitFor(t, "the machine to lock", func() bool { return sys.count(&sys.locks) == 1 })
}

type fakeVD struct{ name string }

func (d fakeVD) Name() string          { return d.name }
func (d fakeVD) Resize(int, int) error { return nil }
func (d fakeVD) Close() error          { return nil }

// TestPrivacyMode: with privacy mode on, opening a virtual display leaves
// it the only one on, and closing it turns the others back on.
func TestPrivacyMode(t *testing.T) {
	sys := &fakeSystem{}
	h, err := NewHost(HostConfig{ConfigDir: t.TempDir(), NoTailscale: true, System: sys.hooks(), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	st := &stream{h: h}
	vd := fakeVD{`\\.\DISPLAY9`}
	st.isolate(vd)
	if len(sys.isolated) != 0 {
		t.Fatal("isolated with privacy mode off")
	}
	h.SetSessionSettings(SessionSettings{PrivacyMode: true})
	st.isolate(vd)
	st.isolate(vd)
	st.closeVirtualDisplay(vd)
	st.closeVirtualDisplay(vd)
	if !slices.Equal(sys.isolated, []string{vd.name}) || !slices.Equal(sys.restored, []string{vd.name}) {
		t.Errorf("isolated %v, restored %v; want %s once each", sys.isolated, sys.restored, vd.name)
	}
}

func TestSecureAttentionAndExclusions(t *testing.T) {
	sys := &fakeSystem{}
	h, err := NewHost(HostConfig{ConfigDir: t.TempDir(), NoTailscale: true, System: sys.hooks(), Logf: t.Logf,
		AudioExcludePIDs: []int{7}, AudioExcludeApps: []string{"Discord"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range input.CtrlAltDel(0) {
		h.secureAttention(ev)
	}
	want := 0
	if runtime.GOOS == "windows" {
		want = 1
	}
	if sys.count(&sys.sas) != want {
		t.Errorf("Ctrl+Alt+Del asked for the SAS %d times, want %d", sys.count(&sys.sas), want)
	}
	pids := h.audioExcludePIDs()
	wantPIDs := []int{7}
	if audio.CanExcludeProcesses {
		wantPIDs = []int{7, 4242}
	}
	if !slices.Equal(pids, wantPIDs) {
		t.Errorf("excluded PIDs %v, want %v", pids, wantPIDs)
	}
}

func TestFromStoredSession(t *testing.T) {
	s := config.DefaultHostConfig(t.TempDir())
	s.StayAwake, s.MuteSpeakers, s.IdleTimeoutMinutes = true, true, 15
	s.AudioExcludeApps, s.AudioDevice, s.VirtualMic = config.NewAppList("Discord"), "dev-1", "CABLE"
	c := DefaultHostConfig().FromStored(s)
	if c.Session != (SessionSettings{StayAwake: true, MuteSpeakers: true, IdleTimeout: 15 * time.Minute}) {
		t.Errorf("session %+v", c.Session)
	}
	if !slices.Equal(c.AudioExcludeApps, []string{"Discord"}) || c.AudioDevice != "dev-1" || c.MicDevice != "CABLE" || c.NoMic {
		t.Errorf("audio: apps %v device %q mic %q off %v", c.AudioExcludeApps, c.AudioDevice, c.MicDevice, c.NoMic)
	}
	s.VirtualMic = config.VirtualMicOff
	if c := DefaultHostConfig().FromStored(s); !c.NoMic {
		t.Error("virtual mic off still takes the microphone")
	}
}

// TestPrivacyModeStream: a stream on a virtual display with privacy mode
// on leaves that display the only one on, and turns the others back on as
// it ends. The client learns the host's OS from its capabilities.
func TestPrivacyModeStream(t *testing.T) {
	vdd, sys := &fakeVDD{}, &fakeSystem{}
	var forced, kbps atomic.Int64
	h, err := NewHost(HostConfig{
		ConfigDir: t.TempDir(), Name: "privacy-host", ListenAddr: "127.0.0.1:0", DiscoveryAddr: "off",
		NoTailscale: true, NoAudio: true, Capabilities: vddHostCaps, OpenVirtualDisplay: vdd.open, Logf: t.Logf,
		Session: SessionSettings{PrivacyMode: true}, System: sys.hooks(),
		OpenVideo: func(p VideoParams) (VideoPipe, error) {
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
	caps := ultrawideClientCaps()
	c, err := NewClient(ClientConfig{
		ConfigDir: t.TempDir(), Name: "privacy-client", PIN: h.PIN(), NoAudio: true,
		Capabilities: func() defaults.ClientCapabilities { return caps },
		OpenSink:     NullSinkOpener(&NullSink{}), Tailscale: noTailscale{}, Logf: t.Logf,
		ViewSize: func() (int, int) { return 1600, 900 },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, Target{Addr: h.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	if got := c.HostOS(); got != vddHostCaps().OS {
		t.Errorf("HostOS = %q, want %q", got, vddHostCaps().OS)
	}
	waitFor(t, "privacy mode", func() bool { sys.mu.Lock(); defer sys.mu.Unlock(); return len(sys.isolated) == 1 })
	c.Disconnect()
	waitFor(t, "the displays back", func() bool { sys.mu.Lock(); defer sys.mu.Unlock(); return len(sys.restored) == 1 })
	sys.mu.Lock()
	defer sys.mu.Unlock()
	if sys.isolated[0] != sys.restored[0] || sys.isolated[0] == "" {
		t.Errorf("isolated %v, restored %v", sys.isolated, sys.restored)
	}
	if c.HostOS() != "" {
		t.Error("HostOS after the stream ended")
	}
}
