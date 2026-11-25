package ui

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
)

// fakeBackend records calls and serves fixed state. Connect blocks until
// the test sends its result on connectResult or the context ends.
type fakeBackend struct {
	mu            sync.Mutex
	onChange      func()
	hosts         []HostInfo
	tel           Telemetry
	settings      Settings
	saveErr       error
	saved         []Settings
	hosting       HostingState
	hostingCalls  []bool
	tailnet       TailnetStatus
	encoders      []EncoderInfo
	pair          PairState
	pins          []string
	pinErr        error
	trusts        int
	cancels       int
	connects      []string
	connectResult chan error
	connectCtxErr error
	disconnects   int
	keyframes     int
	refreshes     int
	renames       [][2]string
	renameErr     error
	removes       []string
	overrides     map[string]defaults.UserOverrides
	ovSaves       int
	ovErr         error
	layout        OverlayLayout
	layoutSaves   int
	stats         SessionStats
	last          *SessionStats
	hostSettings  HostSettings
	hostSaves     int
	displays      []DisplayInfo
	ports         PortStatus
	paired        []PairedClient
	revokes       []string
	measures      []string
	clientName    string
	// keys holds the hotkeys and the never-send list, with config's rules.
	keys           config.ClientConfig
	ctrlAltDels    int
	switches       int
	switchErr      error
	hostDisplays   map[string][]DisplayInfo
	selected       []string
	selectErr      error
	kicks          int
	audioDevices   AudioDevices
	apps           []string
	privacyProblem string
}

func newFake() *fakeBackend {
	return &fakeBackend{
		keys:          config.DefaultClientConfig(),
		connectResult: make(chan error, 1),
		settings:      Settings{AudioEnabled: true, AudioVolume: 0.8},
		overrides:     map[string]defaults.UserOverrides{},
	}
}

func (f *fakeBackend) lock() func() { f.mu.Lock(); return f.mu.Unlock }

func (f *fakeBackend) Subscribe(fn func()) { defer f.lock()(); f.onChange = fn }
func (f *fakeBackend) Hosts() []HostInfo {
	defer f.lock()()
	return append([]HostInfo(nil), f.hosts...)
}
func (f *fakeBackend) RefreshHosts()        { defer f.lock()(); f.refreshes++ }
func (f *fakeBackend) Disconnect()          { defer f.lock()(); f.disconnects++; f.tel = Telemetry{} }
func (f *fakeBackend) Telemetry() Telemetry { defer f.lock()(); return f.tel }
func (f *fakeBackend) RequestKeyframe()     { defer f.lock()(); f.keyframes++ }
func (f *fakeBackend) SetMicMuted(muted bool) error {
	defer f.lock()()
	if !muted && f.tel.MicDevice == "" {
		return errors.New(f.tel.MicHint)
	}
	f.tel.MicMuted = muted
	return nil
}
func (f *fakeBackend) Hosting() HostingState { defer f.lock()(); return f.hosting }
func (f *fakeBackend) Tailnet() TailnetStatus {
	defer f.lock()()
	return f.tailnet
}
func (f *fakeBackend) Encoders() []EncoderInfo { defer f.lock()(); return f.encoders }
func (f *fakeBackend) Settings() Settings      { defer f.lock()(); return f.settings }
func (f *fakeBackend) Pairing() PairState      { defer f.lock()(); return f.pair }
func (f *fakeBackend) CancelPairing()          { defer f.lock()(); f.cancels++; f.pair = PairState{} }

func (f *fakeBackend) Connect(ctx context.Context, target string) error {
	f.mu.Lock()
	f.connects = append(f.connects, target)
	f.mu.Unlock()
	select {
	case err := <-f.connectResult:
		return err
	case <-ctx.Done():
		f.mu.Lock()
		f.connectCtxErr = ctx.Err()
		f.mu.Unlock()
		return ctx.Err()
	}
}

func (f *fakeBackend) RenameHost(id, alias string) error {
	defer f.lock()()
	f.renames = append(f.renames, [2]string{id, alias})
	return f.renameErr
}

func (f *fakeBackend) RemoveHost(id string) error {
	defer f.lock()()
	f.removes = append(f.removes, id)
	f.hosts = slices.DeleteFunc(f.hosts, func(h HostInfo) bool { return h.ID == id })
	return nil
}

func (f *fakeBackend) SetHosting(ctx context.Context, on bool) error {
	defer f.lock()()
	f.hostingCalls = append(f.hostingCalls, on)
	f.hosting.On = on
	return nil
}

func (f *fakeBackend) SaveSettings(s Settings) error {
	defer f.lock()()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, s)
	f.settings = s
	return nil
}

func (f *fakeBackend) Overrides(hostID string) defaults.UserOverrides {
	defer f.lock()()
	return f.overrides[hostID]
}

func (f *fakeBackend) SaveOverrides(hostID string, o defaults.UserOverrides) error {
	defer f.lock()()
	if f.ovErr != nil {
		return f.ovErr
	}
	if err := o.Validate(); err != nil {
		return err
	}
	f.ovSaves++
	f.overrides[hostID] = o
	return nil
}

func (f *fakeBackend) OverlayLayout() OverlayLayout { defer f.lock()(); return f.layout }

func (f *fakeBackend) SaveOverlayLayout(l OverlayLayout) error {
	defer f.lock()()
	f.layoutSaves++
	f.layout = l
	return nil
}

// override is the stored overrides of hostID, all auto when none.
func (f *fakeBackend) override(hostID string) defaults.UserOverrides {
	return f.Overrides(hostID).Normalize()
}

func (f *fakeBackend) SubmitPIN(ctx context.Context, pin string) error {
	defer f.lock()()
	f.pins = append(f.pins, pin)
	f.pair.Status = PairPaired
	return f.pinErr
}

func (f *fakeBackend) TrustTailscalePeer(ctx context.Context) error {
	defer f.lock()()
	f.trusts++
	f.pair.Status = PairPaired
	return nil
}

func (f *fakeBackend) SessionStats() SessionStats { defer f.lock()(); return f.stats }

func (f *fakeBackend) LastSession() (SessionStats, bool) {
	defer f.lock()()
	if f.last == nil {
		return SessionStats{}, false
	}
	return *f.last, true
}

func (f *fakeBackend) HostSettings() HostSettings { defer f.lock()(); return f.hostSettings }

func (f *fakeBackend) SetHostSettings(s HostSettings) error {
	defer f.lock()()
	f.hostSaves++
	f.hostSettings = s
	return nil
}

func (f *fakeBackend) Displays() []DisplayInfo { defer f.lock()(); return f.displays }

func (f *fakeBackend) MeasureEncoder(ctx context.Context, gpu string) (float64, error) {
	defer f.lock()()
	f.measures = append(f.measures, gpu)
	for _, e := range f.encoders {
		if e.GPU == gpu {
			return e.EncodeMs, nil
		}
	}
	return 0, errors.New("no such encoder")
}

func (f *fakeBackend) TestPorts(HostPorts) PortStatus { defer f.lock()(); return f.ports }

func (f *fakeBackend) PairedClients() []PairedClient {
	defer f.lock()()
	return append([]PairedClient(nil), f.paired...)
}

func (f *fakeBackend) RevokeClient(fp string) error {
	defer f.lock()()
	f.revokes = append(f.revokes, fp)
	f.paired = slices.DeleteFunc(f.paired, func(c PairedClient) bool { return c.Fingerprint == fp })
	return nil
}

func (f *fakeBackend) RevokeAllClients() error {
	defer f.lock()()
	f.revokes = append(f.revokes, "*")
	f.paired = nil
	return nil
}

func (f *fakeBackend) ClientName() string { defer f.lock()(); return f.clientName }

func (f *fakeBackend) SetClientName(name string) error {
	defer f.lock()()
	f.clientName = name
	return nil
}

func (f *fakeBackend) Hotkeys() []Hotkey {
	defer f.lock()()
	out := make([]Hotkey, 0, len(config.HotkeyDefs))
	for _, d := range config.HotkeyDefs {
		out = append(out, Hotkey{Action: d.Action, Chord: f.keys.Hotkey(d.Action).String(), Default: d.Default.String(), Global: d.Global})
	}
	return out
}

func (f *fakeBackend) SetHotkey(a HotkeyAction, chord string) error {
	defer f.lock()()
	return f.keys.SetHotkey(a, chord, "darwin")
}

func (f *fakeBackend) ResetHotkey(a HotkeyAction) error {
	defer f.lock()()
	return f.keys.ResetHotkey(a, "darwin")
}

func (f *fakeBackend) ResetAllHotkeys() error { defer f.lock()(); f.keys.ResetAllHotkeys(); return nil }

func (f *fakeBackend) NeverSend() []string { defer f.lock()(); return slices.Clone(f.keys.NeverSend) }

func (f *fakeBackend) AddNeverSend(chord string) error {
	defer f.lock()()
	return f.keys.AddNeverSend(chord)
}

func (f *fakeBackend) RemoveNeverSend(chord string) error {
	defer f.lock()()
	return f.keys.RemoveNeverSend(chord)
}

func (f *fakeBackend) SendCtrlAltDel() { defer f.lock()(); f.ctrlAltDels++ }

func (f *fakeBackend) SwitchDisplay() error {
	defer f.lock()()
	f.switches++
	return f.switchErr
}

func (f *fakeBackend) HostDisplays(hostID string) []DisplayInfo {
	defer f.lock()()
	return slices.Clone(f.hostDisplays[hostID])
}

// SelectHostDisplay moves the fake stream to id, as a host's new Stream
// would.
func (f *fakeBackend) SelectHostDisplay(id string) error {
	defer f.lock()()
	f.selected = append(f.selected, id)
	if f.selectErr != nil {
		return f.selectErr
	}
	if f.tel.Profile != nil {
		p := *f.tel.Profile
		p.VirtualDisplay, p.CaptureDisplay = false, id
		f.tel.Profile = &p
	}
	return nil
}

func (f *fakeBackend) KickClient() error { defer f.lock()(); f.kicks++; return nil }

func (f *fakeBackend) AudioDevices() AudioDevices { defer f.lock()(); return f.audioDevices }

func (f *fakeBackend) RunningApps() []string { defer f.lock()(); return slices.Clone(f.apps) }

func (f *fakeBackend) PrivacyModeProblem() string { defer f.lock()(); return f.privacyProblem }

func (f *fakeBackend) set(fn func(f *fakeBackend)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func sampleHosts() []HostInfo {
	return []HostInfo{
		{ID: "h-bigboi", Name: "bigboi", OS: "Windows 11", GPU: "RTX 4080 NVENC", PathKind: discovery.PathLANDirect,
			RTTMs: 0.18, Online: true, Paired: true, Address: "192.168.188.20", Width: 1920, Height: 1080, RefreshHz: 60},
		{ID: "h-studio", Name: "studio-mac", OS: "macOS 27", GPU: "M5 Pro VideoToolbox",
			PathKind: discovery.PathTailscaleDirect, RTTMs: 0.4, Online: true, Paired: true, TailscaleSameUser: true},
		{ID: "h-cabin", Name: "cabin-pc", OS: "Windows 11", GPU: "RX 6700 XT AMF",
			PathKind: discovery.PathTailscaleDERP, Online: true},
	}
}

func sampleTelemetry() Telemetry {
	return Telemetry{Connected: true, HostName: "bigboi", HostID: "h-bigboi", FPS: 240, TargetFPS: 240,
		EncodeMs: 1.5, NetMs: 0.7, DecodeMs: 1.0, PresentMs: 2.5, TotalMs: 8.4,
		BitrateMbps: 80, FECPct: 5, PathKind: discovery.PathLANDirect,
		Codec: encode.HEVC, Chroma: encode.P010, Width: 1920, Height: 1080}
}

func newTester(t *testing.T, f *fakeBackend) (*App, *mui.Tester) {
	t.Helper()
	if err := theme.RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	a := NewApp(f)
	return a, mui.NewTester(a.View, testWidth, testHeight)
}

const testWidth, testHeight = 1440, 900

// eventually runs frames until cond holds, for work the app does off the
// main thread.
func eventually(t *testing.T, tt *mui.Tester, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 3s")
		}
		time.Sleep(5 * time.Millisecond)
		tt.Frame()
	}
}

func click(t *testing.T, tt *mui.Tester, name string) {
	t.Helper()
	if err := tt.Click(name); err != nil {
		t.Fatalf("click %q: %v (texts: %q)", name, err, tt.Texts())
	}
	tt.Frame()
}

// clickScrolled scrolls the page until name is in the window, then
// clicks it.
func clickScrolled(t *testing.T, tt *mui.Tester, name string) {
	t.Helper()
	scrollTo(t, tt, name)
	click(t, tt, name)
}

// scrollTo scrolls the page under the pointer until the element named
// name is wholly in the window.
func scrollTo(t *testing.T, tt *mui.Tester, name string) {
	t.Helper()
	for range 20 {
		info, ok := tt.Access(name)
		if !ok {
			t.Fatalf("no element named %q: %q", name, tt.Texts())
		}
		r := info.Bounds
		if !info.Offscreen && r.Y >= 0 && r.Y+r.H <= testHeight {
			return
		}
		tt.Scroll(testWidth/2, testHeight/2, 0, r.Y-testHeight/2)
		tt.Frame()
	}
	t.Fatalf("%q did not scroll into the window", name)
}

// liveSample is a live stream's statistics, as the design's live page.
func liveSample() SessionStats {
	ft := make([]float64, 60)
	for i := range ft {
		ft[i] = 8.33 + float64(i%5-2)*0.2
	}
	ft[40] = 11.1
	return SessionStats{Live: true, HostName: "bigboi", HostID: "h-bigboi", Started: time.Now().Add(-42 * time.Minute),
		Capture: StageStat{P50: 1.6}, Encode: StageStat{P50: 1.41}, Network: StageStat{P50: 0.5}, Decode: StageStat{P50: 0.87},
		Present: StageStat{P50: 4.02}, Total: StageStat{P50: 8.4, P99: 11.2},
		FrameTimesMs: ft, FPS: 120, TargetFPS: 120, FramesReceived: 302400, FramesDropped: 0, FramesLost: 1, Keyframes: 14,
		BitrateMbps: 84.2, CeilingMbps: 150, LossPct: 0.08, FECParityPct: 18, FECRepaired: 412,
		AudioBufMs: 21, MicDevice: "TONOR G11", Codec: encode.HEVC, Chroma: encode.YUV444P10, BitDepth: 10,
		Width: 1920, Height: 1080, EncoderGPU: "NVIDIA GeForce RTX 4080", EncoderAPI: "NVENC",
		PathKind: discovery.PathLANDirect, RTTMs: 0.42, DatagramBytes: 1452,
		Profile: &defaults.ResolvedProfile{Slices: 4, Split: true, DirectDisplay: true}}
}
