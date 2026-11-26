package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/planckdotgg/planck/internal/session"
	usage "github.com/planckdotgg/planck/internal/telemetry"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gamepad"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// statsEvery is how often the stream's telemetry is sampled for the HUD.
const statsEvery = 250 * time.Millisecond

// backend is the client UI's ui.Backend: discovery, pairing and the
// stream through session.Client, and hosting this machine through
// session.Host.
type backend struct {
	dir    string
	finder *finder
	ts     session.PathInfoer
	// openSink makes the video sink of each stream: the window's GPU
	// surface, or a decoder alone in tests.
	openSink session.SinkOpener
	// hostConfig is the configuration the Hosting toggle starts.
	hostConfig func(s ui.Settings) session.HostConfig
	encoders   []ui.EncoderInfo
	logf       func(string, ...any)
	// routeConfig tunes the route monitor; the route override sets its
	// PreferLAN.
	routeConfig session.RouteConfig
	// overrides are the command line's, over the stored ones.
	overrides defaults.UserOverrides
	viewSize  func() (w, h int)
	display   func() defaults.ClientDisplay
	onCursor  func(wire.CursorState)
	// onCursorShape hears the host's cursor image; nil does not ask for it.
	onCursorShape func(wire.CursorShape)
	// onSystemKeys hears immersive mode turned on or off.
	onSystemKeys func(on bool)
	// onHostDisplays is backendConfig's.
	onHostDisplays func(host string, ds []defaults.HostDisplay, streamed string)
	// measure times an encoder, and displays lists this machine's.
	measure  func(ctx context.Context, gpu string) (session.Latency, error)
	displays func() []defaults.HostDisplay
	// audioOutputs, audioInputs and runningApps list this machine's sound
	// devices and processes.
	audioOutputs, audioInputs func() ([]audio.Device, error)
	runningApps               func() ([]audio.App, error)
	caches                    probeCaches
	// pads reads this device's controllers for Settings and the stream.
	pads *session.GamepadPump
	// onKeys hears the hotkeys and the never-send list change; heldMods is
	// what modifiers the host holds for this device, as HID bits. The GUI
	// sets both before the first stream.
	onKeys   func(hotkeys []ui.Hotkey, never []string)
	heldMods func() uint8
	// globals takes the host's hotkeys while hosting; nil registers none.
	globals    globalShortcuts
	globalMu   sync.Mutex
	registered []string
	// swap is Settings.SwapCmdCtrl, for the input path.
	swap atomic.Bool

	mu       sync.Mutex
	onChange func()
	// name is the name hosts pair this device under.
	name     string
	settings ui.Settings
	// hostStored is host.json as this backend last read or wrote it.
	hostStored config.HostConfig
	// stats is the stream's last sample, from started; last is the final
	// sample of the stream before.
	stats   ui.SessionStats
	started time.Time
	last    *ui.SessionStats
	client  *session.Client
	target  session.Target
	pinned  map[string]config.PinnedHost
	tel     ui.Telemetry
	pair    ui.PairState
	pairTo  session.Target
	host    *session.Host
	hosting ui.HostingState
	aliases map[string]string
	// connected is the pin key of the host streaming now.
	connected string
	// stored are client.json's overrides: for every host, and by host ID.
	storedAll   defaults.UserOverrides
	storedHosts map[string]defaults.UserOverrides
	layout      ui.OverlayLayout
	// self holds this machine's own host ID and certificate fingerprint,
	// which discovery finds while it hosts and the list leaves out.
	self map[string]bool
	// keys are client.json's hotkeys and never-send list.
	keys config.ClientConfig
	// gamepadCfg is gamepad.json as this backend last read or wrote it.
	gamepadCfg config.GamepadConfig
	// hostOS is the OS of the host hostOSOf streams from.
	hostOSOf *session.Client
	hostOS   string
	// hostDisplays are each host's displays as it advertised them while
	// streaming this run, by pin key.
	hostDisplays map[string][]ui.DisplayInfo
	// usageTel is the app's usage telemetry, nil in tests; usageStream is
	// what it counted of the stream now, nil without one.
	usageTel    *usage.Client
	usageStream *usage.Session
}

type backendConfig struct {
	dir, name  string
	finder     *finder
	tailscale  session.PathInfoer
	openSink   session.SinkOpener
	hostConfig func(ui.Settings) session.HostConfig
	encoders   []ui.EncoderInfo
	logf       func(string, ...any)
	route      session.RouteConfig
	overrides  defaults.UserOverrides
	// viewSize is the stream window's size in pixels.
	viewSize func() (w, h int)
	// display is the display the stream window is on; nil, or a zero
	// width, is the main display.
	display func() defaults.ClientDisplay
	// onCursor hears the host's cursor state, and onCursorShape its
	// image.
	onCursor      func(wire.CursorState)
	onCursorShape func(wire.CursorShape)
	// onSystemKeys hears immersive mode turned on or off, at once.
	onSystemKeys func(on bool)
	// onHostDisplays hears, once a stream, the host's displays and the
	// one streaming ("" on a virtual display); nil hears nothing.
	onHostDisplays func(host string, ds []defaults.HostDisplay, streamed string)
	// measure replaces session.MeasureEncoder, and displays
	// session.LocalDisplays.
	measure  func(ctx context.Context, gpu string) (session.Latency, error)
	displays func() []defaults.HostDisplay
	// audioOutputs, audioInputs and runningApps replace audio's.
	audioOutputs, audioInputs func() ([]audio.Device, error)
	runningApps               func() ([]audio.App, error)
	globals                   globalShortcuts
	// openGamepads opens this device's controllers, session.OpenGamepads
	// in the app; nil has none. virtualPads replaces
	// gamepad.VirtualPadStatus.
	openGamepads func() (session.GamepadSource, error)
	virtualPads  func() ui.VirtualPadStatus
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
	b := &backend{dir: dir, name: cmp.Or(c.name, stored.DeviceName, config.Hostname()), finder: c.finder, ts: c.tailscale, openSink: c.openSink, hostConfig: c.hostConfig,
		encoders: c.encoders, logf: c.logf, routeConfig: c.route, overrides: c.overrides, viewSize: c.viewSize, display: c.display, onCursor: c.onCursor, onCursorShape: c.onCursorShape, onSystemKeys: c.onSystemKeys, onHostDisplays: c.onHostDisplays, settings: settingsFrom(stored), pinned: stored.PinnedHosts, aliases: stored.HostAliases,
		storedAll: stored.Overrides, storedHosts: stored.HostOverrides, layout: layoutFrom(stored.Overlay), self: map[string]bool{},
		measure: c.measure, displays: c.displays, audioOutputs: c.audioOutputs, audioInputs: c.audioInputs, runningApps: c.runningApps,
		globals: c.globals, keys: config.ClientConfig{Hotkeys: stored.Hotkeys, NeverSend: stored.NeverSend}}
	b.swap.Store(stored.SwapCmdCtrl)
	if b.audioOutputs == nil {
		b.audioOutputs = audio.OutputDevices
	}
	if b.audioInputs == nil {
		b.audioInputs = audio.InputDevices
	}
	if b.runningApps == nil {
		b.runningApps = audio.RunningApps
	}
	if b.measure == nil {
		b.measure = func(ctx context.Context, gpu string) (session.Latency, error) {
			return session.MeasureEncoder(ctx, nil, gpu, measureFrames)
		}
	}
	if b.displays == nil {
		b.displays = session.LocalDisplays
	}
	gp, err := config.LoadGamepadConfig(dir)
	if err != nil {
		return nil, err
	}
	b.gamepadCfg = gp
	if c.openGamepads == nil {
		c.openGamepads = func() (session.GamepadSource, error) { return noGamepads{}, nil }
	}
	b.pads = session.NewGamepadPump(c.openGamepads, b.changed, b.logf)
	b.pads.SetProfile(gamepadProfile(gp, b.logf))
	if c.virtualPads == nil {
		c.virtualPads = gamepad.VirtualPadStatus
	}
	b.initCaches(c.virtualPads)
	host, err := config.LoadHostConfig(dir)
	if err != nil {
		return nil, err
	}
	b.hostStored = host
	b.settings.HostName, b.settings.Encoder = host.Name, host.Adapter
	b.self[host.HostID] = true
	if cert, err := tls.LoadX509KeyPair(host.CertPath, host.KeyPath); err == nil && len(cert.Certificate) > 0 {
		b.self[config.CertFingerprint(cert.Certificate[0])] = true
	}
	b.finder.subscribe(func() {
		b.syncPins()
		b.changed()
	})
	return b, nil
}

// settingsFrom reads the stored preferences; the UI's other settings
// start at their defaults each run.
func settingsFrom(c config.ClientConfig) ui.Settings {
	s := ui.Settings{AudioEnabled: c.AudioEnabled, ClipboardSend: c.ClipboardSend, ClipboardReceive: c.ClipboardReceive,
		SystemKeysCapture: c.SystemKeysCapture, AudioVolume: 1, AudioMuted: c.AudioMuted, OpenFullscreen: c.StreamsOpenFullscreen(),
		SwapCmdCtrl: c.SwapCmdCtrl, OutputDevice: c.AudioOutputDevice, MicDevice: c.MicDevice,
		OpenEveryHostDisplay: c.OpenEveryHostDisplay, MaxStreamWindows: c.StreamWindows(), UsageTelemetry: c.TelemetryOn()}
	if c.AudioVolume != nil {
		s.AudioVolume = min(max(*c.AudioVolume, 0), 1)
	}
	return s
}

var docks = map[ui.DockEdge]string{ui.DockLeft: "left", ui.DockRight: "right", ui.DockTop: "top", ui.DockBottom: "bottom"}

var anchors = map[ui.Anchor]string{ui.AnchorStart: "start", ui.AnchorCenter: "center", ui.AnchorEnd: "end"}

func anchorFrom(name string) ui.Anchor {
	for a, n := range anchors {
		if n == name {
			return a
		}
	}
	return ui.AnchorNone
}

func placementFrom(p *config.Placement) ui.Placement {
	if p == nil {
		return ui.Placement{}
	}
	return ui.Placement{AnchorX: anchorFrom(p.AnchorX), AnchorY: anchorFrom(p.AnchorY),
		OffsetX: float32(p.OffsetX), OffsetY: float32(p.OffsetY)}
}

func placementTo(p ui.Placement) *config.Placement {
	if p.AnchorX == ui.AnchorNone || p.AnchorY == ui.AnchorNone {
		return nil
	}
	return &config.Placement{AnchorX: anchors[p.AnchorX], AnchorY: anchors[p.AnchorY],
		OffsetX: float64(p.OffsetX), OffsetY: float64(p.OffsetY)}
}

func layoutFrom(l *config.OverlayLayout) ui.OverlayLayout {
	if l == nil {
		return ui.OverlayLayout{}
	}
	out := ui.OverlayLayout{Placed: true, DockFrac: float32(l.DockFrac), ButtonPlace: placementFrom(l.Button),
		Metrics: l.Metrics, MetricsPlace: placementFrom(l.MetricsPlace), MetricsCompact: l.MetricsCompact,
		ButtonX: float32(l.ButtonX), ButtonY: float32(l.ButtonY), MetricsX: float32(l.MetricsX), MetricsY: float32(l.MetricsY)}
	for e, name := range docks {
		if name == l.Dock {
			out.Dock = e
		}
	}
	return out
}

func (b *backend) OverlayLayout() ui.OverlayLayout {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.layout
}

// SaveOverlayLayout keeps where the overlay was left for the next stream.
func (b *backend) SaveOverlayLayout(l ui.OverlayLayout) error {
	err := config.UpdateClientConfig(b.dir, func(stored *config.ClientConfig) error {
		stored.Overlay = &config.OverlayLayout{Dock: docks[l.Dock], DockFrac: float64(l.DockFrac),
			Button: placementTo(l.ButtonPlace), Metrics: l.Metrics, MetricsPlace: placementTo(l.MetricsPlace),
			MetricsCompact: l.MetricsCompact, ButtonX: float64(l.ButtonX), ButtonY: float64(l.ButtonY),
			MetricsX: float64(l.MetricsX), MetricsY: float64(l.MetricsY)}
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.layout = l
	b.mu.Unlock()
	return nil
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

// Hosts lists every host once: the hosts found, with their paired
// record's routes, and the paired hosts not found, offline. A pin made
// by address joins the host found with its certificate. This machine's
// own host is left out, and so is a host not paired once it is gone.
func (b *backend) Hosts() []ui.HostInfo {
	b.mu.Lock()
	pinned, aliases, connected := b.pinned, b.aliases, b.connected
	self := maps.Clone(b.self)
	b.mu.Unlock()
	isSelf := func(id, fp string) bool { return self[id] || fp != "" && self[strings.ToLower(fp)] }
	cardOf := map[string]string{}
	var out []ui.HostInfo
	for _, v := range b.finder.list() {
		id := v.Beacon.HostID
		if isSelf(id, v.Beacon.CertFingerprint) {
			continue
		}
		p, paired := pinned[id]
		var addrPins []string
		for key, q := range pinned {
			if strings.HasPrefix(key, "addr:") && strings.EqualFold(q.CertFingerprint, v.Beacon.CertFingerprint) {
				addrPins = append(addrPins, key)
			}
		}
		paired = paired || len(addrPins) > 0
		if !paired && !v.Online {
			continue
		}
		cardOf[id] = id
		for _, key := range addrPins {
			cardOf[key] = id
		}
		h := ui.HostInfo{
			ID: id, AdvertisedName: v.Beacon.Name, OS: v.Beacon.OS, PathKind: v.Best.Kind,
			RTTMs: float64(v.Best.RTT) / 1e6, Online: v.Online, Paired: paired, TailscaleSameUser: v.SameUser,
			Address:    v.controlAddr(v.Best),
			LANAddress: v.controlAddr(v.LAN), TailscaleAddress: v.controlAddr(v.Tailscale),
			LANReachable: v.LANOnline, TailscaleReachable: v.TailscaleOnline, Fingerprint: strings.ToLower(v.Beacon.CertFingerprint),
		}
		if h.LANAddress == "" {
			h.LANAddress = p.LastLANAddr
		}
		if h.TailscaleAddress == "" {
			h.TailscaleAddress = p.LastTailscaleAddr
		}
		if len(v.Beacon.GPUs) > 0 {
			h.GPU = v.Beacon.GPUs[0]
		}
		out = append(out, h)
	}
	byFingerprint := map[string]string{}
	for key, p := range pinned {
		fp := strings.ToLower(p.CertFingerprint)
		if old, ok := byFingerprint[fp]; !strings.HasPrefix(key, "addr:") && (!ok || key < old) {
			byFingerprint[fp] = key
		}
	}
	for key, p := range pinned {
		if _, ok := cardOf[key]; ok || isSelf(key, p.CertFingerprint) {
			continue
		}
		if id, ok := byFingerprint[strings.ToLower(p.CertFingerprint)]; ok && strings.HasPrefix(key, "addr:") {
			cardOf[key] = id
			continue
		}
		cardOf[key] = key
		name := p.Name
		if name == "" {
			name = strings.TrimPrefix(key, "addr:")
		}
		out = append(out, ui.HostInfo{ID: key, AdvertisedName: name, Paired: true, Address: p.LastAddr,
			LANAddress: p.LastLANAddr, TailscaleAddress: p.LastTailscaleAddr, Fingerprint: strings.ToLower(p.CertFingerprint)})
	}
	for i := range out {
		h := &out[i]
		h.Alias = aliases[h.ID]
		h.Name = cmp.Or(h.Alias, h.AdvertisedName)
		h.Connected = connected != "" && cardOf[connected] == h.ID
	}
	slices.SortFunc(out, func(a, b ui.HostInfo) int {
		switch {
		case a.Connected != b.Connected:
			return boolFirst(a.Connected)
		case a.Online != b.Online:
			return boolFirst(a.Online)
		}
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// boolFirst orders a true before a false, for a sort on a field that
// differs.
func boolFirst(a bool) int {
	if a {
		return -1
	}
	return 1
}

func (b *backend) RefreshHosts() { b.finder.Refresh() }

// RenameHost keeps alias for the host in client.json; empty forgets it.
func (b *backend) RenameHost(id, alias string) error {
	alias = strings.TrimSpace(alias)
	if len(alias) > session.MaxHostNameLen {
		return fmt.Errorf("a host name is at most %d bytes", session.MaxHostNameLen)
	}
	var aliases map[string]string
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		if alias == "" {
			delete(c.HostAliases, id)
		} else {
			if c.HostAliases == nil {
				c.HostAliases = map[string]string{}
			}
			c.HostAliases[id] = alias
		}
		aliases = c.HostAliases
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.aliases = aliases
	b.mu.Unlock()
	b.changed()
	return nil
}

// RemoveHost forgets the host whose card is id: every pin of its
// certificate, by ID and by address, with their aliases and overrides,
// and what discovery found of it.
func (b *backend) RemoveHost(id string) error {
	b.mu.Lock()
	pinned, connected := b.pinned, b.connected
	b.mu.Unlock()
	fp := pinned[id].CertFingerprint
	if v, ok := b.finder.lookup(id); ok && fp == "" {
		fp = v.Beacon.CertFingerprint
	}
	keys := []string{id}
	for key, p := range pinned {
		if key != id && fp != "" && strings.EqualFold(p.CertFingerprint, fp) {
			keys = append(keys, key)
		}
	}
	if connected != "" && slices.Contains(keys, connected) {
		return errors.New("disconnect from the host before removing it")
	}
	var hostOverrides map[string]defaults.UserOverrides
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		for _, key := range keys {
			delete(c.PinnedHosts, key)
			delete(c.HostAliases, key)
			delete(c.HostOverrides, key)
		}
		hostOverrides = maps.Clone(c.HostOverrides)
		return nil
	})
	if err != nil {
		return err
	}
	b.finder.forget(id)
	b.mu.Lock()
	b.storedHosts = hostOverrides
	b.mu.Unlock()
	b.reloadPins()
	b.changed()
	return nil
}

// syncPins keeps each paired host's advertised name and routes up to
// date in client.json as discovery finds them.
func (b *backend) syncPins() {
	b.mu.Lock()
	pinned := b.pinned
	b.mu.Unlock()
	type update struct {
		name, lan, tailnet string
	}
	updates := map[string]update{}
	for _, v := range b.finder.list() {
		p, ok := pinned[v.Beacon.HostID]
		if !ok {
			continue
		}
		u := update{name: v.Beacon.Name, lan: v.controlAddr(v.LAN), tailnet: v.controlAddr(v.Tailscale)}
		if u.name != p.Name || u.lan != "" && u.lan != p.LastLANAddr || u.tailnet != "" && u.tailnet != p.LastTailscaleAddr {
			updates[v.Beacon.HostID] = u
		}
	}
	if len(updates) == 0 {
		return
	}
	err := config.UpdateClientConfig(b.dir, func(c *config.ClientConfig) error {
		for id, u := range updates {
			p, ok := c.PinnedHosts[id]
			if !ok {
				continue
			}
			p.Name = u.name
			p.LastLANAddr = cmp.Or(u.lan, p.LastLANAddr)
			p.LastTailscaleAddr = cmp.Or(u.tailnet, p.LastTailscaleAddr)
			c.PinnedHosts[id] = p
		}
		return nil
	})
	if err != nil {
		b.logf("planck: remember host routes: %v", err)
		return
	}
	b.reloadPins()
}

// resolveTarget resolves what Connect was given: a host found by discovery,
// a paired host, or an address typed in. A known host gets every route it
// was found or reached on, and its last address as the fallback.
func (b *backend) resolveTarget(ctx context.Context, id string) (session.Target, string) {
	b.mu.Lock()
	p, paired := b.pinned[id]
	alias := b.aliases[id]
	b.mu.Unlock()
	v, found := b.finder.lookup(id)
	if !found && !paired {
		t, name := identify(ctx, id, b.finder.port)
		return t, cmp.Or(alias, name)
	}
	t := session.Target{Addr: p.LastAddr, Fingerprint: p.CertFingerprint}
	if !strings.HasPrefix(id, "addr:") {
		t.HostID = id
	}
	name := p.Name
	if found {
		t.Candidates, t.HostID, t.Fingerprint, name = v.Candidates, v.Beacon.HostID, v.Beacon.CertFingerprint, v.Beacon.Name
	}
	for _, last := range []string{p.LastLANAddr, p.LastTailscaleAddr} {
		ap, err := netip.ParseAddrPort(last)
		if err != nil {
			continue
		}
		c := discovery.Candidate{HostID: t.HostID, Kind: kindOf(ap.Addr()), Addr: netip.AddrPortFrom(ap.Addr(), b.finder.port)}
		if !slices.ContainsFunc(t.Candidates, func(o discovery.Candidate) bool { return o.Addr == c.Addr }) {
			t.Candidates = append(t.Candidates, c)
		}
	}
	return t, cmp.Or(alias, name)
}

func (b *backend) Connect(ctx context.Context, target string) error {
	id, err := b.quickConnect(target)
	if err != nil {
		return err
	}
	t, name := b.resolveTarget(ctx, id)
	err = b.dial(ctx, t, "")
	if errors.Is(err, transport.ErrAuthDenied) {
		var denied *session.HostDeniedError
		if errors.As(err, &denied) && t.Fingerprint == "" {
			// The PIN then pairs only with the certificate the user checked.
			t.Fingerprint = denied.Fingerprint
		}
		b.mu.Lock()
		b.pair = ui.PairState{Status: ui.PairPending, HostName: name, Fingerprint: t.Fingerprint, TailscaleSameUser: b.sameUser(t), Address: t.Addr}
		if v, ok := b.finder.lookup(t.HostID); ok {
			b.pair.Address, b.pair.PathKind, b.pair.RTTMs = v.controlAddr(v.Best), v.Best.Kind, float64(v.Best.RTT)/1e6
		}
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
	s, name := b.settings, b.name
	old := b.client
	b.client = nil
	if old != nil {
		b.endSessionLocked()
	}
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
	err := b.dialNew(ctx, t, pin, s, name)
	if err != nil && old != nil {
		// The stream this one replaced is gone; its telemetry must go too.
		b.mu.Lock()
		if b.client == nil {
			b.tel, b.connected = ui.Telemetry{}, ""
		}
		b.mu.Unlock()
		b.changed()
	}
	return err
}

func (b *backend) dialNew(ctx context.Context, t session.Target, pin string, s ui.Settings, name string) error {
	ov := b.overridesFor(targetKey(t))
	routes, err := routeAllowed(t, ov.RoutePolicy)
	if err != nil {
		return err
	}
	cfg := session.ClientConfig{
		ConfigDir: b.dir, Name: name, PIN: pin, NoAudio: !s.AudioEnabled, Quality: legacyQuality(ov), Overrides: ov,
		OpenSink: b.openSink, Tailscale: b.ts, Logf: b.logf, Route: b.route(ov), ViewSize: b.viewSize, Clipboard: clipboardOf(s),
		OnCursor: b.onCursor, OnCursorShape: b.onCursorShape, Capabilities: b.capabilities, Gamepads: b.pads,
	}
	if id := s.OutputDevice; id != "" {
		cfg.OpenAudioPlayer = func() (audio.Player, error) { return audio.NewOutputPlayer(id) }
	}
	if id := s.MicDevice; id != "" {
		cfg.OpenMic = func() (audio.MicCapturer, error) { return audio.NewMicCapturerOn(id) }
	}
	c, err := session.NewClient(cfg)
	if err != nil {
		return err
	}
	c.SetVolume(playbackGain(s))
	err = c.Connect(ctx, routes)
	// The session pins a host as soon as it accepts this device, before
	// the stream starts, which may still fail.
	b.reloadPins()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		// Another connect replaced this one as it finished.
		c.Close()
		return err
	}
	st := c.Stats()
	b.mu.Lock()
	b.client, b.target = c, t
	b.connected = hostKey(st)
	b.tel = telemetry(st)
	b.started = time.Now()
	b.stats = sessionStats(st, b.started)
	b.usageStream = &usage.Session{Mode: usage.ModeClient, Codec: st.Codec, Transport: st.Path}
	b.usageTel.SessionStarted(*b.usageStream)
	b.mu.Unlock()
	b.changed()
	if b.viewSize != nil {
		b.resizeStream(b.viewSize())
	}
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
	b.pinned, b.aliases = stored.PinnedHosts, stored.HostAliases
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

// hostKey is the key a stream's host is pinned, listed and given its own
// overrides under: its host ID, or its address when it sent none.
func hostKey(s session.ClientStats) string { return cmp.Or(s.HostID, "addr:"+s.Addr) }

// targetKey is hostKey before the stream starts.
func targetKey(t session.Target) string { return cmp.Or(t.HostID, "addr:"+t.Addr) }

// legacyQuality is what o asks of a host that does not resolve streams
// from both ends' capabilities.
func legacyQuality(o defaults.UserOverrides) session.Quality {
	var q session.Quality
	if o.Codec != "" && o.Codec != defaults.Auto && o.Codec != "pyrowave" {
		q.Codec, _ = session.ParseCodec(string(o.Codec))
	}
	switch {
	case o.Chroma == "444" && o.BitDepth == "10":
		q.Chroma = encode.YUV444P10
	case o.Chroma == "444":
		q.Chroma = encode.YUV444
	case o.BitDepth == "10":
		q.Chroma = encode.P010
	case o.Chroma == "420" || o.BitDepth == "8":
		q.Chroma = encode.NV12
	}
	q.FPS, _ = strconv.Atoi(string(o.FPS))
	q.Slices, _ = strconv.Atoi(string(o.SubFrameSlices))
	q.BitrateKbps = o.BitrateMbps * 1000
	return q
}

// overridesFor is what a stream from hostID asks for: the stored
// overrides, the host's over every host's, with the command line's on top.
func (b *backend) overridesFor(hostID string) defaults.UserOverrides {
	b.mu.Lock()
	defer b.mu.Unlock()
	return defaults.Merge(defaults.Merge(b.storedAll, b.storedHosts[hostID]), b.overrides)
}

// routeAllowed keeps t's routes the route override allows.
func routeAllowed(t session.Target, policy defaults.RoutePref) (session.Target, error) {
	if policy != "lan" && policy != "tailscale" {
		return t, nil
	}
	allowed := func(ip netip.Addr) bool { return (kindOf(ip) == discovery.PathLANDirect) == (policy == "lan") }
	t.Candidates = slices.DeleteFunc(slices.Clone(t.Candidates), func(c discovery.Candidate) bool { return !allowed(c.Addr.Addr()) })
	if ap, err := netip.ParseAddrPort(t.Addr); err == nil && !allowed(ap.Addr()) {
		t.Addr = ""
	}
	if len(t.Candidates) == 0 && t.Addr == "" {
		return t, fmt.Errorf("the route setting is %s, and the host has no %s route", policy, routeName(policy))
	}
	return t, nil
}

func routeName(p defaults.RoutePref) string {
	if p == "lan" {
		return "LAN"
	}
	return "Tailscale"
}

// follow samples c's stats for the HUD until its session ends or another
// stream replaces it.
func (b *backend) follow(c *session.Client) {
	t := time.NewTicker(statsEvery)
	defer t.Stop()
	done := c.Done()
	announced := false
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
			st := c.Stats()
			b.tel = telemetry(st)
			if st.Connected {
				b.stats = sessionStats(st, b.started)
				b.noteHostDisplaysLocked(st)
			}
			b.mu.Unlock()
			if st.Connected && !announced && st.HostDisplays != nil && b.onHostDisplays != nil {
				announced = true
				b.onHostDisplays(st.HostName, st.HostDisplays, streamedDisplay(st.Profile))
			}
			b.changed()
		}
	}
	b.mu.Lock()
	if b.client == c {
		b.client, b.tel, b.connected = nil, ui.Telemetry{}, ""
		b.endSessionLocked()
	}
	b.mu.Unlock()
	b.changed()
}

// streamedDisplay is the host display p captures, "" on a virtual
// display or without a profile.
func streamedDisplay(p *defaults.ResolvedProfile) string {
	if p == nil || p.VirtualDisplay {
		return ""
	}
	return p.CaptureDisplay
}

func telemetry(s session.ClientStats) ui.Telemetry {
	t := ui.Telemetry{
		Connected: s.Connected, HostName: s.HostName, HostID: hostKey(s), Profile: s.Profile, FPS: s.FPS, Width: s.Width, Height: s.Height,
		EncodeMs: s.Encode.P50, NetMs: s.Network.P50, DecodeMs: s.Decode.P50, PresentMs: s.Present.P50, TotalMs: s.E2E.P50,
		BitrateMbps: s.Kbps / 1000, FECPct: s.FECParityPct, LossPct: s.LossPct, AudioBufMs: s.AudioBufferMs,
		DatagramBytes: s.DatagramBytes, MicMuted: s.Mic.Muted, MicDevice: s.Mic.Device, MicHint: s.Mic.Hint,
		CaptureMs: s.CaptureMs, RTTMs: s.RTTMs, CeilingMbps: float64(s.MaxBitrateKbps) / 1000,
		FramesDropped: s.FramesDropped,
	}
	if s.HostEncodeMs > 0 {
		t.EncodeMs = s.HostEncodeMs
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
	b.client, b.tel, b.connected = nil, ui.Telemetry{}, ""
	b.endSessionLocked()
	b.mu.Unlock()
	if c != nil {
		go c.Close()
	}
	b.changed()
}

// SetMicMuted mutes or unmutes this client's microphone on the host.
func (b *backend) SetMicMuted(muted bool) error {
	c := b.current()
	if c == nil {
		return session.ErrNotConnected
	}
	err := c.SetMicMuted(muted)
	b.mu.Lock()
	if b.client == c {
		b.tel = telemetry(c.Stats())
	}
	b.mu.Unlock()
	b.changed()
	return err
}

func clipboardOf(s ui.Settings) session.ClipboardConfig {
	return session.ClipboardConfig{Send: s.ClipboardSend, Receive: s.ClipboardReceive}
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

// capabilities are this device's, with the display the stream window is
// on in place of the main one.
func (b *backend) capabilities() defaults.ClientCapabilities {
	c := session.ProbeClientCapabilities()
	if b.display != nil {
		if d := b.display(); d.WidthPx > 0 && d.HeightPx > 0 {
			c.Display = d
		}
	}
	return c
}

// resizeStream offers the stream, if there is one, the window's new size
// and the display it is on now. After a connect it offers a size or
// display that changed while the stream was starting.
func (b *backend) resizeStream(w, h int) {
	c := b.current()
	if c == nil {
		return
	}
	var d defaults.ClientDisplay
	if b.display != nil {
		d = b.display()
	}
	if err := c.ResizeOn(w, h, d); err != nil {
		b.logf("planck: resize the stream: %v", err)
	}
}

// SendInput forwards one input event to the stream, if there is one.
func (b *backend) SendInput(ev input.Event) {
	if c := b.current(); c != nil {
		_ = c.SendInput(b.swapInput(c, ev))
	}
}

// Hosting reads the PIN, which rotates, and the client live.
func (b *backend) Hosting() ui.HostingState {
	b.mu.Lock()
	st, h := b.hosting, b.host
	b.mu.Unlock()
	if h != nil {
		st.PIN = h.PIN()
		if fp := h.ConnectedClient(); fp != "" {
			st.Client = h.PairedClients()[fp].Name
		}
	}
	return st
}

func (b *backend) SetHosting(ctx context.Context, on bool) error {
	b.mu.Lock()
	h, s := b.host, b.settings
	state := ui.HostingState{Encoder: encoderLabel(s.Encoder, b.encoders)}
	b.host = nil
	b.mu.Unlock()
	if h != nil {
		h.Stop()
	}
	b.caches.paired.invalidate()
	var err error
	if on {
		hc := b.hostConfig(s)
		hc.OnSession = b.hostSessionEvent
		h, err = session.NewHost(hc)
		if err == nil {
			err = h.Start(context.Background())
		}
		if err != nil {
			state.Err = hostingError(err)
			h = nil
		} else {
			state.On, state.Fingerprint, state.Using = true, h.Fingerprint(), hostPorts(h)
			b.logf("planck: hosting as %q on %v; pairing PIN %s, fingerprint %s", h.Name(), h.Addr(), h.PIN(), h.Fingerprint())
		}
	}
	b.mu.Lock()
	b.host, b.hosting = h, state
	if h != nil {
		b.self[h.HostID()], b.self[h.Fingerprint()] = true, true
	}
	b.mu.Unlock()
	b.syncGlobals()
	b.changed()
	return err
}

// hostPorts are the ports h holds.
func hostPorts(h *session.Host) ui.HostPorts {
	p := ui.HostPorts{Media: h.MediaPort()}
	if a, ok := h.Addr().(*net.UDPAddr); ok {
		p.Control = a.Port
	}
	if a, ok := h.DiscoveryAddr().(*net.UDPAddr); ok {
		p.Discovery = a.Port
	}
	return p
}

// hostingError is what the hosting card says of a failure to host.
func hostingError(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Addr != nil {
		if _, port, e := net.SplitHostPort(op.Addr.String()); e == nil {
			proto := strings.ToUpper(strings.TrimRight(op.Net, "46"))
			switch {
			case errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, wsaeAddrInUse):
				return fmt.Sprintf("Another app is using %s port %s.", proto, port)
			case errors.Is(err, wsaeAccess):
				// Windows refuses a shared bind on a port another socket
				// holds alone, and a bind in a range it reserved.
				return fmt.Sprintf("Windows will not let Planck use %s port %s: another app holds it, or Windows reserved it.", proto, port)
			}
		}
	}
	return err.Error()
}

// wsaeAddrInUse and wsaeAccess are Windows' WSAEADDRINUSE and WSAEACCES,
// which syscall.EADDRINUSE and syscall.EACCES are not.
const (
	wsaeAddrInUse = syscall.Errno(10048)
	wsaeAccess    = syscall.Errno(10013)
)

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

func (b *backend) Encoders() []ui.EncoderInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.encoders)
}

func (b *backend) Settings() ui.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// SaveSettings stores s; a live stream reconnects with a new quality in
// the background, as protocol v1 fixes it at the handshake. A new host
// name goes to host.json, and to the host at once while hosting.
func (b *backend) SaveSettings(s ui.Settings) error {
	s.HostName = strings.TrimSpace(s.HostName)
	b.mu.Lock()
	before, h := b.settings, b.host
	b.mu.Unlock()
	if s.HostName != before.HostName {
		if err := b.renameLocalHost(h, s.HostName); err != nil {
			return err
		}
	}
	if s.Encoder != before.Encoder {
		if err := config.UpdateHostConfig(b.dir, func(c *config.HostConfig) error {
			c.Adapter = s.Encoder
			return nil
		}); err != nil {
			return err
		}
	}
	if s.HostName != before.HostName || s.Encoder != before.Encoder {
		if stored, err := config.LoadHostConfig(b.dir); err == nil {
			b.mu.Lock()
			b.hostStored = stored
			b.mu.Unlock()
		}
	}
	err := config.UpdateClientConfig(b.dir, func(stored *config.ClientConfig) error {
		stored.AudioEnabled = s.AudioEnabled
		stored.ClipboardSend, stored.ClipboardReceive = s.ClipboardSend, s.ClipboardReceive
		stored.SystemKeysCapture = s.SystemKeysCapture
		stored.AudioMuted = s.AudioMuted
		stored.AudioVolume = nil
		if s.AudioVolume != 1 {
			v := s.AudioVolume
			stored.AudioVolume = &v
		}
		stored.OpenFullscreen = nil
		if !s.OpenFullscreen {
			off := false
			stored.OpenFullscreen = &off
		}
		stored.SwapCmdCtrl, stored.AudioOutputDevice, stored.MicDevice = s.SwapCmdCtrl, s.OutputDevice, s.MicDevice
		stored.OpenEveryHostDisplay = s.OpenEveryHostDisplay
		stored.MaxStreamWindows = 0
		if s.MaxStreamWindows != config.DefaultStreamWindows {
			stored.MaxStreamWindows = s.MaxStreamWindows
		}
		stored.TelemetryEnabled = nil
		if !s.UsageTelemetry {
			off := false
			stored.TelemetryEnabled = &off
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	before = b.settings
	b.settings = s
	c := b.client
	b.mu.Unlock()
	b.swap.Store(s.SwapCmdCtrl)
	b.usageTel.SetEnabled(s.UsageTelemetry)
	if clipboardOf(before) != clipboardOf(s) && c != nil {
		c.SetClipboard(clipboardOf(s))
	}
	if before.SystemKeysCapture != s.SystemKeysCapture && b.onSystemKeys != nil {
		b.onSystemKeys(s.SystemKeysCapture)
	}
	if c != nil && playbackGain(before) != playbackGain(s) {
		c.SetVolume(playbackGain(s))
	}
	if before.AudioEnabled != s.AudioEnabled {
		b.redial()
	}
	return nil
}

// playbackGain is the level the stream's audio plays at: muting keeps the
// audio flowing, silent.
func playbackGain(s ui.Settings) float64 {
	if s.AudioMuted {
		return 0
	}
	return s.AudioVolume
}

// redial restarts a live stream in the background, as protocol v1 fixes
// its format at the handshake.
func (b *backend) redial() {
	b.mu.Lock()
	live, t := b.client != nil, b.target
	b.mu.Unlock()
	if !live {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := b.dial(ctx, t, ""); err != nil {
			b.logf("planck: reconnect with the new settings: %v", err)
		}
	}()
}

func (b *backend) Overrides(hostID string) defaults.UserOverrides {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hostID == "" {
		return b.storedAll
	}
	return b.storedHosts[hostID]
}

// SaveOverrides stores o, and restarts a live stream whose overrides it
// changes. A host's all-auto overrides are dropped from client.json.
func (b *backend) SaveOverrides(hostID string, o defaults.UserOverrides) error {
	o = o.Normalize()
	if err := o.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	streaming := targetKey(b.target)
	b.mu.Unlock()
	before := b.overridesFor(streaming)
	auto := o == defaults.UserOverrides{}.Normalize()
	err := config.UpdateClientConfig(b.dir, func(stored *config.ClientConfig) error {
		switch {
		case hostID == "":
			stored.Overrides = o
		case auto:
			delete(stored.HostOverrides, hostID)
		default:
			if stored.HostOverrides == nil {
				stored.HostOverrides = map[string]defaults.UserOverrides{}
			}
			stored.HostOverrides[hostID] = o
		}
		b.mu.Lock()
		b.storedAll, b.storedHosts = stored.Overrides, maps.Clone(stored.HostOverrides)
		b.mu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	b.changed()
	if after := b.overridesFor(streaming); after.Normalize() != before.Normalize() {
		b.redial()
	}
	return nil
}

// renameLocalHost names this machine as a host: through h while
// hosting, which also tells clients at once, else in host.json.
func (b *backend) renameLocalHost(h *session.Host, name string) error {
	if h != nil {
		return h.SetName(name)
	}
	if len(name) > session.MaxHostNameLen {
		return fmt.Errorf("a host name is at most %d bytes", session.MaxHostNameLen)
	}
	return config.UpdateHostConfig(b.dir, func(c *config.HostConfig) error {
		c.Name = name
		return nil
	})
}

// route is how the stream follows the host between routes: to the LAN
// unless the route override keeps it on Tailscale.
func (b *backend) route(o defaults.UserOverrides) session.RouteConfig {
	r := b.routeConfig
	r.PreferLAN = o.RoutePolicy != "tailscale"
	return r
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
	b.endUsageLocked()
	b.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if h != nil {
		h.Stop()
	}
	b.pads.Close()
}
