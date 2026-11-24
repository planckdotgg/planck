package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/transport"
)

// statsEvery is how often the stream's telemetry is sampled for the HUD.
const statsEvery = 250 * time.Millisecond

// backend is the client UI's ui.Backend: discovery, pairing and the
// stream through session.Client, and hosting this machine through
// session.Host.
type backend struct {
	dir    string
	name   string
	finder *finder
	ts     session.PathInfoer
	// openSink makes the video sink of each stream: the window's GPU
	// surface, or a decoder alone in tests.
	openSink session.SinkOpener
	// hostConfig is the configuration the Hosting toggle starts.
	hostConfig func(s ui.Settings) session.HostConfig
	encoders   []ui.EncoderInfo
	logf       func(string, ...any)
	// overrides are the command line's, over the stored ones.
	overrides defaults.UserOverrides

	mu       sync.Mutex
	onChange func()
	settings ui.Settings
	client   *session.Client
	target   session.Target
	pinned   map[string]config.PinnedHost
	tel      ui.Telemetry
	pair     ui.PairState
	pairTo   session.Target
	host     *session.Host
	hosting  ui.HostingState
}

type backendConfig struct {
	dir, name  string
	finder     *finder
	tailscale  session.PathInfoer
	openSink   session.SinkOpener
	hostConfig func(ui.Settings) session.HostConfig
	encoders   []ui.EncoderInfo
	logf       func(string, ...any)
	overrides  defaults.UserOverrides
}

func newBackend(c backendConfig) (*backend, error) {
	dir := c.dir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	stored, err := config.LoadClientConfig(dir)
	if err != nil {
		return nil, err
	}
	b := &backend{dir: dir, name: c.name, finder: c.finder, ts: c.tailscale, openSink: c.openSink, hostConfig: c.hostConfig,
		encoders: c.encoders, logf: c.logf, overrides: c.overrides, settings: settingsFrom(stored), pinned: stored.PinnedHosts}
	b.finder.subscribe(b.changed)
	return b, nil
}

// settingsFrom reads the stored preferences; the UI's other settings
// start at their defaults each run.
func settingsFrom(c config.ClientConfig) ui.Settings {
	s := ui.Settings{TargetFPS: c.TargetFPS, MaxBitrateMbps: c.MaxBitrateKbps / 1000, Slices: 1, AudioEnabled: c.AudioEnabled, PreferLAN: true}
	s.Codec, _ = session.ParseCodec(c.PreferredCodec)
	s.Chroma, _ = session.ParseChroma(c.PreferredChroma)
	return s
}

func (b *backend) Subscribe(fn func()) {
	b.mu.Lock()
	b.onChange = fn
	b.mu.Unlock()
}

func (b *backend) changed() {
	b.mu.Lock()
	fn := b.onChange
	b.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Hosts lists the hosts found and the paired hosts not found, which show
// offline.
func (b *backend) Hosts() []ui.HostInfo {
	b.mu.Lock()
	pinned := b.pinned
	b.mu.Unlock()
	var out []ui.HostInfo
	seen := map[string]bool{}
	for _, v := range b.finder.list() {
		_, paired := pinned[v.Beacon.HostID]
		h := ui.HostInfo{
			ID: v.Beacon.HostID, Name: v.Beacon.Name, OS: v.Beacon.OS, PathKind: v.Best.Kind,
			RTTMs: float64(v.Best.RTT) / 1e6, Online: v.Online, Paired: paired, TailscaleSameUser: v.SameUser,
			Address: netip.AddrPortFrom(v.Best.Addr.Addr(), v.Beacon.ControlPort).String(),
		}
		if len(v.Beacon.GPUs) > 0 {
			h.GPU = v.Beacon.GPUs[0]
		}
		seen[h.ID] = true
		out = append(out, h)
	}
	for key, p := range pinned {
		if seen[key] {
			continue
		}
		name := p.Name
		if name == "" {
			name = strings.TrimPrefix(key, "addr:")
		}
		out = append(out, ui.HostInfo{ID: key, Name: name, Paired: true, Address: p.LastAddr})
	}
	return out
}

func (b *backend) RefreshHosts() { b.finder.Refresh() }

// resolveTarget resolves what Connect was given: a host found by discovery, a
// paired host by its last address, or an address typed in.
func (b *backend) resolveTarget(ctx context.Context, id string) (session.Target, string) {
	if v, ok := b.finder.lookup(id); ok {
		return session.Target{Candidates: v.Candidates, HostID: v.Beacon.HostID, Fingerprint: v.Beacon.CertFingerprint}, v.Beacon.Name
	}
	b.mu.Lock()
	p, ok := b.pinned[id]
	b.mu.Unlock()
	if ok && p.LastAddr != "" {
		t := session.Target{Addr: p.LastAddr, Fingerprint: p.CertFingerprint}
		if !strings.HasPrefix(id, "addr:") {
			t.HostID = id
		}
		return t, p.Name
	}
	return identify(ctx, id)
}

func (b *backend) Connect(ctx context.Context, id string) error {
	t, name := b.resolveTarget(ctx, id)
	err := b.dial(ctx, t, "")
	if errors.Is(err, transport.ErrAuthDenied) {
		b.mu.Lock()
		b.pair = ui.PairState{Status: ui.PairPending, HostName: name, Fingerprint: t.Fingerprint, TailscaleSameUser: b.sameUser(t)}
		b.pairTo = t
		b.mu.Unlock()
		b.changed()
		return ui.ErrPairingRequired
	}
	return err
}

func (b *backend) sameUser(t session.Target) bool {
	for _, c := range t.Candidates {
		if b.finder.sameUser(c.Addr.Addr()) {
			return true
		}
	}
	return false
}

// dial streams from t, replacing any stream, and follows the session
// until it ends.
func (b *backend) dial(ctx context.Context, t session.Target, pin string) error {
	b.mu.Lock()
	s := b.settings
	old := b.client
	b.client = nil
	b.mu.Unlock()
	if old != nil {
		old.Disconnect()
	}
	ov := b.overrides
	if stored, err := config.LoadClientConfig(b.dir); err == nil {
		ov = defaults.Merge(stored.OverridesFor(t.HostID), b.overrides)
	}
	c, err := session.NewClient(session.ClientConfig{
		ConfigDir: b.dir, Name: b.name, PIN: pin, NoAudio: !s.AudioEnabled, Quality: quality(s), Overrides: ov,
		OpenSink: b.openSink, Tailscale: b.ts, Logf: b.logf,
	})
	if err != nil {
		return err
	}
	err = c.Connect(ctx, t)
	// The session pins a host as soon as it accepts this device, before
	// the stream starts, which may still fail.
	b.reloadPins()
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.client, b.target = c, t
	b.tel = telemetry(c.Stats())
	b.mu.Unlock()
	b.changed()
	go b.follow(c)
	return nil
}

func (b *backend) reloadPins() {
	stored, err := config.LoadClientConfig(b.dir)
	if err != nil {
		b.logf("planck: %v", err)
		return
	}
	b.mu.Lock()
	b.pinned = stored.PinnedHosts
	b.mu.Unlock()
}

// newPin reports whether after holds a pin that before did not, or a
// pin with another fingerprint.
func newPin(before, after map[string]config.PinnedHost) bool {
	for key, p := range after {
		if q, ok := before[key]; !ok || !strings.EqualFold(q.CertFingerprint, p.CertFingerprint) {
			return true
		}
	}
	return false
}

func quality(s ui.Settings) session.Quality {
	return session.Quality{Codec: s.Codec, Chroma: s.Chroma, FPS: s.TargetFPS, BitrateKbps: s.MaxBitrateMbps * 1000, Slices: s.Slices}
}

// follow samples c's stats for the HUD until its session ends or another
// stream replaces it.
func (b *backend) follow(c *session.Client) {
	t := time.NewTicker(statsEvery)
	defer t.Stop()
	done := c.Done()
	for running := done != nil; running; {
		select {
		case <-done:
			if err := c.Err(); err != nil && !errors.Is(err, transport.ErrClosed) {
				b.logf("planck: stream ended: %v", err)
			}
			running = false
		case <-t.C:
			b.mu.Lock()
			if b.client != c {
				b.mu.Unlock()
				return
			}
			b.tel = telemetry(c.Stats())
			b.mu.Unlock()
			b.changed()
		}
	}
	b.mu.Lock()
	if b.client == c {
		b.client, b.tel = nil, ui.Telemetry{}
	}
	b.mu.Unlock()
	b.changed()
}

func telemetry(s session.ClientStats) ui.Telemetry {
	t := ui.Telemetry{
		Connected: s.Connected, HostName: s.HostName, FPS: s.FPS, Width: s.Width, Height: s.Height,
		EncodeMs: s.Encode.P50, NetMs: s.Network.P50, DecodeMs: s.Decode.P50, PresentMs: s.Present.P50, TotalMs: s.E2E.P50,
		BitrateMbps: s.Kbps / 1000, FECPct: s.FECParityPct, LossPct: s.LossPct, AudioBufMs: s.AudioBufferMs,
	}
	if s.E2EGlass.Samples > 0 {
		t.PresentMs, t.TotalMs = s.Glass.P50, s.E2EGlass.P50
	}
	for _, k := range []discovery.PathKind{discovery.PathLANDirect, discovery.PathTailscaleDirect, discovery.PathTailscaleDERP} {
		if k.String() == s.Path {
			t.PathKind = k
		}
	}
	if c, err := session.ParseCodec(s.Codec); err == nil {
		t.Codec = c
	}
	t.Chroma = encode.NV12
	if s.Chroma == "4:4:4" {
		t.Chroma = encode.YUV444
	}
	return t
}

// Disconnect returns at once: the session's sink may need the main
// thread, which calls this, to close.
func (b *backend) Disconnect() {
	b.mu.Lock()
	c := b.client
	b.client, b.tel = nil, ui.Telemetry{}
	b.mu.Unlock()
	if c != nil {
		go c.Disconnect()
	}
	b.changed()
}

func (b *backend) Telemetry() ui.Telemetry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tel
}

func (b *backend) RequestKeyframe() {
	if c := b.current(); c != nil {
		_ = c.RequestKeyframe()
	}
}

func (b *backend) current() *session.Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.client
}

// SendInput forwards one input event to the stream, if there is one.
func (b *backend) SendInput(ev input.Event) {
	if c := b.current(); c != nil {
		_ = c.SendInput(ev)
	}
}

func (b *backend) Hosting() ui.HostingState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hosting
}

func (b *backend) SetHosting(ctx context.Context, on bool) error {
	b.mu.Lock()
	h, s := b.host, b.settings
	b.host = nil
	b.mu.Unlock()
	if h != nil {
		h.Stop()
	}
	state := ui.HostingState{Encoder: encoderLabel(s.Encoder, b.encoders)}
	var err error
	if on {
		h, err = session.NewHost(b.hostConfig(s))
		if err == nil {
			err = h.Start(context.Background())
		}
		if err != nil {
			state.Err = err.Error()
			h = nil
		} else {
			state.On = true
			b.logf("planck: hosting as %q on %v; pairing PIN %s, fingerprint %s", h.Name(), h.Addr(), h.PIN(), h.Fingerprint())
		}
	}
	b.mu.Lock()
	b.host, b.hosting = h, state
	b.mu.Unlock()
	b.changed()
	return err
}

func encoderLabel(gpu string, all []ui.EncoderInfo) string {
	for _, e := range all {
		if gpu == "" || e.GPU == gpu {
			return e.GPU + " " + e.API
		}
	}
	return gpu
}

func (b *backend) Tailnet() ui.TailnetStatus {
	st := b.finder.tailnetStatus()
	if st == nil || st.BackendState != "Running" {
		return ui.TailnetStatus{}
	}
	return ui.TailnetStatus{Running: true, User: st.Self.UserLogin}
}

func (b *backend) Encoders() []ui.EncoderInfo { return slices.Clone(b.encoders) }

func (b *backend) Settings() ui.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// SaveSettings stores s; a live stream reconnects with a new quality in
// the background, as protocol v1 fixes it at the handshake.
func (b *backend) SaveSettings(s ui.Settings) error {
	stored, err := config.LoadClientConfig(b.dir)
	if err != nil {
		return err
	}
	if s.Codec != 0 {
		stored.PreferredCodec = configName(s.Codec)
	}
	if s.Chroma != 0 {
		stored.PreferredChroma = configName(s.Chroma)
	}
	stored.TargetFPS, stored.MaxBitrateKbps, stored.AudioEnabled = s.TargetFPS, s.MaxBitrateMbps*1000, s.AudioEnabled
	if err := config.SaveClientConfig(b.dir, stored); err != nil {
		return err
	}
	b.mu.Lock()
	before := b.settings
	b.settings = s
	live, t := b.client != nil, b.target
	b.mu.Unlock()
	if live && (quality(before) != quality(s) || before.AudioEnabled != s.AudioEnabled) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := b.dial(ctx, t, ""); err != nil {
				b.logf("planck: reconnect with the new settings: %v", err)
			}
		}()
	}
	return nil
}

// configName is the lower-case name pkg/config stores a codec or chroma
// under.
func configName(v fmt.Stringer) string {
	switch v {
	case encode.H264:
		return "h264"
	case encode.HEVC:
		return "hevc"
	case encode.AV1:
		return "av1"
	case encode.NV12:
		return "nv12"
	case encode.P010:
		return "p010"
	case encode.YUV444:
		return "yuv444"
	case encode.YUV444P10:
		return "yuv444p10"
	}
	return strings.ToLower(v.String())
}

func (b *backend) Pairing() ui.PairState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pair
}

func (b *backend) SubmitPIN(ctx context.Context, pin string) error { return b.pairWith(ctx, pin) }

// TrustTailscalePeer connects again with no PIN: the host trusts a device
// of its own tailnet user once its Tailscale answers who this one is.
func (b *backend) TrustTailscalePeer(ctx context.Context) error { return b.pairWith(ctx, "") }

func (b *backend) pairWith(ctx context.Context, pin string) error {
	b.mu.Lock()
	t := b.pairTo
	before := b.pinned
	b.pair.Status, b.pair.Error = ui.PairVerifying, ""
	b.mu.Unlock()
	b.changed()
	err := b.dial(ctx, t, pin)
	b.mu.Lock()
	switch {
	case err == nil:
		b.pair.Status = ui.PairPaired
	case newPin(before, b.pinned):
		// The host took this device, and has rotated its PIN: the stream
		// failed after pairing, and reconnects need no PIN.
		b.pair.Status, b.pair.Error = ui.PairPaired, "Paired, but the stream did not start: "+err.Error()
	case errors.Is(err, transport.ErrAuthDenied) && pin != "":
		b.pair.Status, b.pair.Error = ui.PairFailed, "The host did not accept that PIN. Check the PIN it shows now and try again."
	case errors.Is(err, transport.ErrAuthDenied):
		b.pair.Status, b.pair.Error = ui.PairFailed, "The host does not trust this device yet. Enter its PIN instead."
	default:
		b.pair.Status, b.pair.Error = ui.PairFailed, err.Error()
	}
	b.mu.Unlock()
	b.changed()
	return err
}

func (b *backend) CancelPairing() {
	b.mu.Lock()
	b.pair, b.pairTo = ui.PairState{}, session.Target{}
	b.mu.Unlock()
	b.changed()
}

// close stops the stream and hosting, for quitting.
func (b *backend) close() {
	b.mu.Lock()
	c, h := b.client, b.host
	b.client, b.host = nil, nil
	b.mu.Unlock()
	if c != nil {
		c.Disconnect()
	}
	if h != nil {
		h.Stop()
	}
}
