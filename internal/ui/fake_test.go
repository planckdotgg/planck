package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
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
}

func newFake() *fakeBackend {
	return &fakeBackend{
		connectResult: make(chan error, 1),
		settings: Settings{Codec: encode.HEVC, Chroma: encode.P010, TargetFPS: 120, MaxBitrateMbps: 100,
			Slices: 4, AudioEnabled: true, PreferLAN: true},
	}
}

func (f *fakeBackend) lock() func() { f.mu.Lock(); return f.mu.Unlock }

func (f *fakeBackend) Subscribe(fn func()) { defer f.lock()(); f.onChange = fn }
func (f *fakeBackend) Hosts() []HostInfo {
	defer f.lock()()
	return append([]HostInfo(nil), f.hosts...)
}
func (f *fakeBackend) RefreshHosts()         { defer f.lock()(); f.refreshes++ }
func (f *fakeBackend) Disconnect()           { defer f.lock()(); f.disconnects++; f.tel = Telemetry{} }
func (f *fakeBackend) Telemetry() Telemetry  { defer f.lock()(); return f.tel }
func (f *fakeBackend) RequestKeyframe()      { defer f.lock()(); f.keyframes++ }
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
	return Telemetry{Connected: true, HostName: "bigboi", FPS: 240, TargetFPS: 240,
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
	return a, mui.NewTester(a.View, 1440, 900)
}

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
