package session

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/capture/cursor"
	"github.com/planckdotgg/planck/pkg/clipboard"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// WhoIser resolves a tailnet peer; *tailscale.Client is one.
type WhoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (*tailscale.PeerIdentity, error)
}

// HostConfig configures a Host. The zero value of each field is usable;
// DefaultHostConfig has the production ports.
type HostConfig struct {
	// ConfigDir holds host.json, the host identity and the paired clients.
	// Empty uses config.Dir().
	ConfigDir string
	// Name overrides host.json's name for this run; SetName renames for
	// good.
	Name string
	// ListenAddr is the QUIC control address; empty means ":47701".
	ListenAddr string
	// MediaPort is the UDP media port; 0 picks a free one per session.
	MediaPort int
	// DiscoveryAddr answers LAN probes and sends beacons; empty means
	// ":47700", "off" disables discovery.
	DiscoveryAddr string

	Display string
	// Adapter picks the encoding GPU by a substring of its name or its
	// vendor ("NVIDIA", "AMD", "Intel"); empty encodes on the GPU that owns
	// the frames.
	Adapter string
	// Codec and Chroma zero take the client's request, then HEVC 4:2:0.
	Codec       encode.Codec
	Chroma      encode.Chroma
	Width       int
	Height      int
	FPS         int
	BitrateKbps int
	Slices      int
	SubFrame    bool
	// Split is split-frame encode; SplitAuto applies encode.Config's rule
	// on LAN paths and stays off elsewhere, where the extra bits cost more
	// than the time saved.
	Split encode.Split
	// Synth streams a generated pattern at Width x Height and FPS on the
	// Adapter GPU instead of a display, for benchmarks without a monitor.
	Synth bool
	// Policy is the host owner's overrides for clients that send their
	// capabilities; it beats theirs. It is merged over host.json's policy.
	// Codec through Split above apply to older clients only.
	Policy defaults.UserOverrides
	// VDDPrimary makes a session's virtual display the primary one, so
	// games open on it; off, it extends the desktop.
	VDDPrimary bool

	// PIN is a fixed pairing PIN; empty generates one that changes after
	// each pairing.
	PIN     string
	NoAudio bool
	// InjectInput replays client input as real OS events. Off by default:
	// tests must never move the cursor of the machine they run on.
	InjectInput bool
	// NoTailscale turns off trusting devices of the same tailnet user.
	NoTailscale bool

	// OpenVideo replaces DefaultVideoOpener.
	OpenVideo VideoOpener
	// OpenVirtualDisplay replaces SudoVDA: it makes a session's virtual
	// display for p, with a stable ID from seed.
	OpenVirtualDisplay func(p VideoParams, seed []byte, primary bool) (VirtualDisplay, error)
	// Capabilities replaces the probe of this machine's encoders, displays
	// and virtual display driver. Without it, a host with its own
	// OpenVideo does not offer wire.FeatureCapabilities: it cannot know
	// what that pipeline does, so clients get the legacy negotiation.
	Capabilities func() defaults.HostCapabilities
	// OpenAudio replaces the platform loopback capturer.
	OpenAudio func() (audio.Capturer, error)
	// AudioExcludePIDs are processes, such as a voice chat app, whose
	// audio the stream leaves out besides Planck's own (macOS only).
	AudioExcludePIDs []int
	// AudioExcludeApps are apps by name whose processes each session
	// leaves out of the sound, as AudioExcludePIDs (macOS only).
	AudioExcludeApps []string
	// AudioDevice records one output device by audio.Device ID, Planck
	// included (Windows); empty records everything but Planck.
	AudioDevice string
	// Session is what hosting does around a session; SetSessionSettings
	// changes it live. System replaces the OS actions it takes.
	Session SessionSettings
	System  SystemHooks
	// NoMic refuses the client's microphone.
	NoMic bool
	// MicDevice picks the virtual input device the client's microphone
	// plays into by a substring of its name; empty finds a known one.
	MicDevice string
	// FindVirtualMic and OpenMicSink replace audio.FindVirtualMic and
	// audio.OpenMicSink.
	FindVirtualMic func(want string) (audio.VirtualMic, error)
	OpenMicSink    func(audio.VirtualMic) (audio.MicSink, error)
	// Clipboard is what the host shares: Send is host to client, Receive
	// client to host.
	Clipboard ClipboardConfig
	// OpenClipboard replaces clipboard.Open.
	OpenClipboard func() (clipboard.Board, error)
	// Injector replaces the platform injector when InjectInput is set.
	Injector input.Injector
	// OnInput sees every valid input event, injected or not.
	OnInput func(input.Event)
	// OnSession hears each client's stream start and end, from the
	// stream's goroutine.
	OnSession func(SessionEvent)
	// ReadCursor replaces cursor.Read, the host's system cursor that
	// clients reading wire.CursorState hear of.
	ReadCursor func() (cursor.State, error)
	// ReadCursorShape replaces cursor.ReadShape, the image of the cursor
	// clients reading wire.CursorShape see.
	ReadCursorShape func(cursor.State) (cursor.Shape, error)
	// WhoIs replaces the local Tailscale daemon client.
	WhoIs WhoIser
	// FECMinLoss keeps parity for at least this loss rate.
	FECMinLoss float64
	// SimulateLoss drops this fraction of outgoing media datagrams.
	SimulateLoss float64
	Logf         func(format string, args ...any)
}

// DefaultHostConfig is the configuration of cmd/planck-host before flags.
func DefaultHostConfig() HostConfig {
	return HostConfig{
		ListenAddr:    fmt.Sprintf(":%d", config.DefaultControlPort),
		MediaPort:     config.DefaultMediaPort,
		DiscoveryAddr: fmt.Sprintf(":%d", discovery.DefaultPort),
		FPS:           60,
		BitrateKbps:   50_000,
		Width:         1920,
		Height:        1080,
		Slices:        1,
	}
}

// FromStored is c with the hosting settings host.json keeps: the ports
// and discovery, the capture display, the encoding GPU, the virtual
// display's primary flag, a fixed PIN, what clients may do, the sound and
// the virtual mic, and the session settings. NewHost reads the name, the
// policy and the trust settings itself.
func (c HostConfig) FromStored(s config.HostConfig) HostConfig {
	listenHost, _, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		listenHost = ""
	}
	c.ListenAddr = net.JoinHostPort(listenHost, fmt.Sprint(s.ControlPort))
	c.MediaPort = int(s.MediaPort)
	c.DiscoveryAddr = "off"
	if s.DiscoveryPort != 0 {
		c.DiscoveryAddr = fmt.Sprintf(":%d", s.DiscoveryPort)
	}
	c.Display, c.VDDPrimary, c.PIN = s.Display, s.VDDPrimary, s.FixedPIN
	if s.Adapter != "" {
		c.Adapter = s.Adapter
	}
	c.InjectInput = c.InjectInput && !s.NoInput
	c.AudioExcludeApps, c.AudioDevice = s.AudioExcludeApps.Names(), s.AudioDevice
	switch s.VirtualMic {
	case "":
	case config.VirtualMicOff:
		c.NoMic = true
	default:
		c.MicDevice = s.VirtualMic
	}
	c.Session = SessionSettings{StayAwake: s.StayAwake, LockOnDisconnect: s.LockOnDisconnect, MuteSpeakers: s.MuteSpeakers,
		PrivacyMode: s.PrivacyMode, IdleTimeout: time.Duration(s.IdleTimeoutMinutes) * time.Minute}
	c.Clipboard = ClipboardConfig{}
	switch s.Clipboard {
	case config.ClipboardBoth:
		c.Clipboard = ClipboardConfig{Send: true, Receive: true}
	case config.ClipboardHostToClient:
		c.Clipboard.Send = true
	case config.ClipboardClientToHost:
		c.Clipboard.Receive = true
	}
	return c
}

// HostStats is a snapshot of the host and its current client.
type HostStats struct {
	Connected  bool
	Client     string
	ClientAddr string
	Path       string
	Video      VideoInfo
	FPS        float64
	SentKbps   float64
	TargetKbps int
	EncodeMs   Latency
	RTTMs      float64
	// FECLossPct is the smoothed pre-FEC loss the parity is sized for, and
	// FECParityPct the parity it added to the last frame.
	FECLossPct   float64
	FECParityPct float64
	Frames       uint64
	Keyframes    uint64
	Dropped      uint64
	PLIs         uint64
	AudioPackets uint64
	Mic          MicStats
	Clipboard    ClipboardStats
	InputEvents  uint64
	InputErrors  uint64
	// Report is the client's last StatsReport.
	Report wire.StatsReport
	// Profile is what the session resolved to, nil for an older client;
	// VirtualDisplay is the GDI name of its virtual display, if any.
	Profile        *defaults.ResolvedProfile
	VirtualDisplay string
	Sessions       uint64
	LastError      string
}

// Host serves one client at a time; a new client takes over.
type Host struct {
	cfg    HostConfig
	dir    string
	name   atomic.Pointer[string]
	hostID string
	cert   tls.Certificate
	fp     [32]byte
	pairs  *pairings
	whois  WhoIser
	open   VideoOpener
	inject input.Injector
	// held is what inject holds down, let go of as each client leaves.
	held input.Held
	// inputOwner is the session whose input is injected and held, nil
	// until the next session sends or is admitted. retired are sessions
	// taken over or ended whose callbacks may still be running; their
	// input is dropped.
	inputOwner *transport.Session
	retired    map[*transport.Session]struct{}
	// injMu orders every use of inject against Stop closing it; injClosed
	// is set once Stop let go of held and closed inject.
	injMu     sync.Mutex
	injClosed bool
	// openInjector and closeVDD replace newInjector and vdd.close.
	openInjector func() (input.Injector, error)
	closeVDD     func() error
	logf         func(string, ...any)

	pinMu sync.Mutex
	pin   string
	// requireLANPIN pairs an unpaired client on a LAN address only by the
	// PIN, never by its Tailscale identity.
	requireLANPIN bool

	srv  atomic.Pointer[transport.Server]
	scfg transport.ServerConfig
	// listen opens the control listener: transport.Listen unless a test
	// sets another.
	listen   func(addr string, cfg transport.ServerConfig) (*transport.Server, error)
	disc     *discovery.Port
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
	// done closes once the host stopped; haltErr says why, when it stopped
	// on its own.
	done    chan struct{}
	haltErr atomic.Pointer[error]
	// stopWait bounds each wait for a stream's teardown.
	stopWait time.Duration

	mu sync.Mutex
	// stopping is set once Stop runs, so a listener opened again after
	// that is closed rather than published.
	stopping bool
	cur      *stream
	report   wire.StatsReport
	lastErr  string
	sessions atomic.Uint64
	inputs   atomic.Uint64
	inputErr atomic.Uint64
	// absMoves counts the absolute moves injected, and absTarget holds
	// the last one's, X in the high half, for warpCapture.
	absMoves  atomic.Uint32
	absTarget atomic.Uint32

	inputTrace *tracer[inputTraceRow]

	caps hostCapsCache
	vdd  vddManager
	clip *clipSync

	sys  hostSys
	pads hostPads
	// lastInput is when the client last sent input, on clock.
	lastInput atomic.Int64
	clock     func() int64
}

// NewHost loads or creates the host's identity and paired clients.
func NewHost(cfg HostConfig) (*Host, error) {
	dir := cfg.ConfigDir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	stored, err := config.LoadHostConfig(dir)
	if err != nil {
		return nil, err
	}
	if err := config.SaveHostConfig(dir, stored); err != nil {
		return nil, err
	}
	cfg.Policy = defaults.Merge(stored.Policy, cfg.Policy)
	if !stored.AllowTailscaleSameUser {
		cfg.NoTailscale = true
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, fmt.Errorf("session: host policy: %w", err)
	}
	cert, err := config.LoadOrCreateIdentity(stored.CertPath, stored.KeyPath, "planck-host "+stored.HostID)
	if err != nil {
		return nil, err
	}
	pairs, err := loadPairings(filepath.Join(dir, PairedClientsFile))
	if err != nil {
		return nil, err
	}
	pads, err := config.LoadGamepadConfig(dir)
	if err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = fmt.Sprintf(":%d", config.DefaultControlPort)
	}
	if cfg.DiscoveryAddr == "" {
		cfg.DiscoveryAddr = fmt.Sprintf(":%d", discovery.DefaultPort)
	}
	if cfg.FPS <= 0 && cfg.Synth {
		cfg.FPS = 60
	}
	h := &Host{inputTrace: newTracer[inputTraceRow](traceInputEnv), cfg: cfg, dir: dir, hostID: stored.HostID, cert: cert, fp: transport.CertFingerprint(cert), pairs: pairs,
		whois: cfg.WhoIs, open: cfg.OpenVideo, logf: cfg.Logf, pin: cfg.PIN, requireLANPIN: stored.RequirePINForLAN,
		listen: transport.Listen, done: make(chan struct{}), stopWait: defaultStopWait}
	if h.logf == nil {
		h.logf = log.Printf
	}
	h.openInjector, h.closeVDD = newInjector, h.vdd.close
	h.sys.set, h.sys.hooks = cfg.Session, cfg.System.withDefaults()
	h.pads.set = GamepadHostSettingsOf(pads)
	name := cfg.Name
	if name == "" {
		name = stored.Name
	}
	h.setLiveName(name)
	if h.open == nil {
		h.open = DefaultVideoOpener
	}
	if h.whois == nil && !cfg.NoTailscale {
		h.whois = tailscale.New()
	}
	if h.pin == "" {
		if h.pin, err = newPIN(); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func (h *Host) HostID() string { return h.hostID }

// Name is the name clients see.
func (h *Host) Name() string { return *h.name.Load() }

// MaxHostNameLen is the longest name SetName takes, in bytes.
const MaxHostNameLen = 64

// SetName renames the host in host.json and, at once, in its discovery
// replies, its next beacon and later handshakes. An empty name advertises
// the machine's own name.
func (h *Host) SetName(name string) error {
	name = strings.TrimSpace(name)
	if len(name) > MaxHostNameLen {
		return fmt.Errorf("session: host name is %d bytes, at most %d", len(name), MaxHostNameLen)
	}
	err := config.UpdateHostConfig(h.dir, func(c *config.HostConfig) error {
		c.Name = name
		return nil
	})
	if err != nil {
		return err
	}
	live := h.setLiveName(name)
	if srv := h.srv.Load(); srv != nil {
		srv.SetName(live)
	}
	return nil
}

func (h *Host) setLiveName(name string) string {
	if name == "" {
		name = config.Hostname()
	}
	h.name.Store(&name)
	return name
}

// Fingerprint is the lowercase hex SHA-256 of the host certificate, which
// clients pin.
func (h *Host) Fingerprint() string { return hex.EncodeToString(h.fp[:]) }

// PIN is the pairing PIN to show the user.
func (h *Host) PIN() string {
	h.pinMu.Lock()
	defer h.pinMu.Unlock()
	return h.pin
}

// PairedClients lists the trusted client certificates by fingerprint.
func (h *Host) PairedClients() map[string]PairedClient { return h.pairs.list() }

// ErrNotPaired is a revoke of a fingerprint the host does not trust.
var ErrNotPaired = errors.New("session: no paired client has that fingerprint")

// RevokeClient forgets the paired client with this certificate
// fingerprint (hex) and ends its stream if it is connected; it needs the
// PIN to pair again.
func (h *Host) RevokeClient(fingerprint string) error {
	return h.revoke([]string{strings.ToLower(fingerprint)})
}

// RevokeAllClients forgets every paired client and ends the stream.
func (h *Host) RevokeAllClients() error { return h.revoke(nil) }

func (h *Host) revoke(fps []string) error {
	gone, err := h.pairs.remove(fps)
	if err != nil {
		return err
	}
	if fps != nil && len(gone) == 0 {
		return ErrNotPaired
	}
	h.mu.Lock()
	st := h.cur
	if st != nil {
		fp := st.sess.PeerFingerprint()
		if slices.Contains(gone, hex.EncodeToString(fp[:])) {
			h.cur = nil
		} else {
			st = nil
		}
	}
	h.mu.Unlock()
	if st != nil {
		h.stopStream(st)
	}
	return nil
}

// LoadPairedClients reads the clients a host with config directory dir
// trusts, for when it is not running.
func LoadPairedClients(dir string) (map[string]PairedClient, error) {
	p, err := loadPairings(filepath.Join(dir, PairedClientsFile))
	if err != nil {
		return nil, err
	}
	return p.list(), nil
}

// RevokePairedClients is Host.RevokeClient, or with fingerprints nil
// RevokeAllClients, for a host that is not running.
func RevokePairedClients(dir string, fingerprints []string) error {
	p, err := loadPairings(filepath.Join(dir, PairedClientsFile))
	if err != nil {
		return err
	}
	for i, fp := range fingerprints {
		fingerprints[i] = strings.ToLower(fp)
	}
	gone, err := p.remove(fingerprints)
	if err == nil && fingerprints != nil && len(gone) == 0 {
		return ErrNotPaired
	}
	return err
}

// ConnectedClient is the certificate fingerprint (hex) of the client
// streaming now, "" without one.
func (h *Host) ConnectedClient() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur == nil {
		return ""
	}
	fp := h.cur.sess.PeerFingerprint()
	return hex.EncodeToString(fp[:])
}

// MediaPort is the fixed UDP media port, or 0 when each session picks one.
func (h *Host) MediaPort() int { return h.cfg.MediaPort }

// Addr is the control listener's address once started.
func (h *Host) Addr() net.Addr {
	srv := h.srv.Load()
	if srv == nil {
		return nil
	}
	return srv.Addr()
}

// DiscoveryAddr is the discovery socket's address, or nil when off.
func (h *Host) DiscoveryAddr() net.Addr {
	if h.disc == nil {
		return nil
	}
	return h.disc.LocalAddr()
}

// Start listens and serves clients until Stop or ctx ends.
func (h *Host) Start(ctx context.Context) error {
	if h.cfg.OpenVideo == nil || h.cfg.Capabilities != nil {
		h.capabilities()
	}
	mediaHost, _, err := net.SplitHostPort(h.cfg.ListenAddr)
	if err != nil {
		return err
	}
	scfg := transport.ServerConfig{
		Certificate:  h.cert,
		Name:         h.Name(),
		Authorize:    h.authorize,
		PIN:          h.PIN,
		Negotiate:    h.negotiate,
		MediaAddr:    net.JoinHostPort(mediaHost, fmt.Sprint(h.cfg.MediaPort)),
		FECMinLoss:   h.cfg.FECMinLoss,
		SimulateLoss: h.cfg.SimulateLoss,
		Handlers: transport.Handlers{
			Input:   h.onInput,
			Control: h.onControl,
		},
	}
	if !h.cfg.NoMic {
		scfg.Handlers.Mic = h.onMic
	}
	h.openInput()
	if h.cfg.Clipboard.any() {
		h.clip = openClipSync(h.cfg.OpenClipboard, h.cfg.Clipboard, h.liveSession, h.peerReceivesClipboard, h.logf)
		if h.clip != nil {
			scfg.Handlers.Clipboard = h.clip.incoming
		}
	}
	if h.cfg.OpenVideo == nil || h.cfg.Capabilities != nil {
		scfg.Capabilities, scfg.Resolve, scfg.Resize, scfg.SwitchDisplay = h.capabilitiesJSON, h.resolve, true, true
	}
	srv, err := h.listen(h.cfg.ListenAddr, scfg)
	if err != nil {
		h.abortStart()
		return err
	}
	h.scfg = scfg
	h.srv.Store(srv)
	ctx, h.cancel = context.WithCancel(ctx)
	if h.cfg.DiscoveryAddr != "off" {
		if err := h.startDiscovery(ctx); err != nil {
			h.cancel()
			srv.Close()
			h.srv.Store(nil)
			h.abortStart()
			return err
		}
	}
	h.wg.Go(func() { h.acceptLoop(ctx) })
	h.setStarted(true)
	return nil
}

// openInput puts the injector in place before the listener opens, since
// the listener hands clients' input to onInput as soon as it serves.
func (h *Host) openInput() {
	if !h.cfg.InjectInput {
		return
	}
	inj := h.cfg.Injector
	if inj == nil {
		var err error
		if inj, err = h.openInjector(); err != nil {
			h.logf("planck: input injection unavailable: %v", err)
			return
		}
	}
	h.injMu.Lock()
	defer h.injMu.Unlock()
	h.inject, h.injClosed = inj, false
	if pi, ok := inj.(PadInjector); ok {
		pi.SetGamepadType(h.GamepadSettings().Type)
	}
}

// abortStart closes what a Start that failed opened.
func (h *Host) abortStart() {
	h.closeInput()
	h.closeClipboard()
	h.clip = nil
}

func (h *Host) startDiscovery(ctx context.Context) error {
	conn, err := discovery.OpenPort(h.cfg.DiscoveryAddr)
	if err != nil {
		return fmt.Errorf("session: discovery: %w", err)
	}
	h.disc = conn
	base := discovery.Beacon{
		HostID:          h.hostID,
		ControlPort:     uint16(h.Addr().(*net.UDPAddr).Port),
		CertFingerprint: h.Fingerprint(),
		OS:              runtime.GOOS,
	}
	beacon := func() discovery.Beacon {
		b := base
		b.Name = h.Name()
		return b
	}
	h.wg.Go(func() { _ = conn.Serve(ctx, beacon) })
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && !ua.IP.IsLoopback() {
		dst := discovery.BroadcastAddrs(uint16(ua.Port))
		h.wg.Go(func() { _ = discovery.AnnounceFunc(ctx, conn, beacon, dst, 2*time.Second) })
	}
	return nil
}

func (h *Host) acceptLoop(ctx context.Context) {
	for {
		sess, err := h.srv.Load().Accept(ctx)
		var he *transport.HandshakeError
		switch {
		case errors.As(err, &he):
			h.fail("client %v: %v", he.Remote, he.Err)
			continue
		case err != nil && ctx.Err() != nil:
			return
		case err != nil:
			if err := h.relisten(ctx, err); err != nil {
				if ctx.Err() != nil {
					return
				}
				h.halt(err)
				return
			}
			continue
		}
		if ctx.Err() != nil {
			sess.Close()
			return
		}
		if _, ok := h.pairs.lookup(sess.PeerFingerprint()); !ok {
			// Revoked between its handshake and here.
			sess.Close()
			continue
		}
		h.sessions.Add(1)
		st := newStream(h, sess)
		h.mu.Lock()
		old := h.cur
		h.cur = st
		h.mu.Unlock()
		if old != nil {
			h.stopWithin(old)
		}
		h.claimInput(sess)
		h.logf("planck: %s connected from %v (%s)", sess.Identity(), sess.RemoteControlAddr(), st.path)
		h.sessionEvent(st, true)
		h.wg.Go(func() {
			st.run(ctx)
			h.sessionEvent(st, false)
			h.mu.Lock()
			if h.cur == st {
				h.cur = nil
			}
			replaced := h.cur != nil
			h.mu.Unlock()
			h.endInput(st)
			if !replaced {
				h.releasePads()
			}
			h.streamEnded(st)
		})
	}
}

// SessionEvent is a client's stream starting or ending: its codec and the
// kind of route, nothing of who the client is.
type SessionEvent struct {
	Started bool
	Codec   encode.Codec
	Path    ratectl.Path
}

func (h *Host) sessionEvent(st *stream, started bool) {
	if h.cfg.OnSession != nil {
		h.cfg.OnSession(SessionEvent{Started: started, Codec: st.params.Codec, Path: st.path})
	}
}

const (
	// relistenTries is how often a host opens its control port again after
	// the listener died, backing off from relistenBackoff, before it stops.
	relistenTries   = 5
	relistenBackoff = 100 * time.Millisecond
	// defaultStopWait is how long a new client, or Stop, waits for a
	// stream to tear down before it goes on without it.
	defaultStopWait = 5 * time.Second
)

// relisten opens the control port again after its listener died. The
// port stays the same, as discovery advertises it.
func (h *Host) relisten(ctx context.Context, cause error) error {
	old := h.srv.Load()
	addr := old.Addr().String()
	if host, _, err := net.SplitHostPort(h.cfg.ListenAddr); err == nil {
		addr = net.JoinHostPort(host, fmt.Sprint(old.Addr().(*net.UDPAddr).Port))
	}
	h.fail("control listener on %s stopped: %v; listening again", addr, cause)
	_ = old.Close()
	wait := relistenBackoff
	var err error
	for range relistenTries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		var srv *transport.Server
		if srv, err = h.listen(addr, h.scfg); err == nil {
			srv.SetName(h.Name())
			h.mu.Lock()
			stopping := h.stopping
			if !stopping {
				h.srv.Store(srv)
			}
			h.mu.Unlock()
			if stopping {
				_ = srv.Close()
				return transport.ErrServerClosed
			}
			h.logf("planck: listening again on %v", srv.Addr())
			return nil
		}
		wait *= 2
	}
	return fmt.Errorf("session: control listener on %s: %w", addr, err)
}

// halt stops a host that can no longer take clients, discovery included,
// so it never advertises a port nothing answers on.
func (h *Host) halt(err error) {
	h.haltErr.Store(&err)
	h.fail("%v; stopping the host", err)
	go h.Stop()
}

// Done closes once the host stopped, by Stop or on its own; Err then says
// why it stopped on its own.
func (h *Host) Done() <-chan struct{} { return h.done }

// Err is why the host stopped on its own, or nil.
func (h *Host) Err() error {
	if e := h.haltErr.Load(); e != nil {
		return *e
	}
	return nil
}

// stopStream ends st, which the caller took out of h.cur: it drops the
// rest of its client's input and lets go of what that client held before
// the teardown, which an input callback still running can hold up.
func (h *Host) stopStream(st *stream) {
	h.retireStream(st)
	st.end()
	<-st.done
}

// stopWithin is stopStream waiting up to stopWait, so a teardown that
// hangs cannot hold up the client taking over.
func (h *Host) stopWithin(st *stream) {
	if !h.within(func() { h.stopStream(st) }) {
		h.fail("the stream of %s took over %v to stop; going on without it", st.sess.Identity(), h.stopWait)
	}
}

// within runs fs at once and waits up to stopWait for them all,
// reporting whether they returned; they keep running after that.
func (h *Host) within(fs ...func()) bool {
	var wg sync.WaitGroup
	for _, f := range fs {
		wg.Go(f)
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return true
	case <-time.After(h.stopWait):
		return false
	}
}

func (h *Host) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	h.mu.Lock()
	h.lastErr = msg
	h.mu.Unlock()
	h.logf("planck: %s", msg)
}

// authorize trusts, in order: a paired certificate, a PIN proven on this
// connection (which pairs the certificate), and a device of the host's own
// Tailscale user (which pairs it too).
func (h *Host) authorize(ctx context.Context, info transport.AuthInfo) (string, error) {
	id, err := h.trust(ctx, info)
	if err == nil {
		// The media port is fixed, so the current client's socket must close
		// before this handshake binds it.
		h.preempt()
	}
	return id, err
}

func (h *Host) preempt() {
	h.mu.Lock()
	st := h.cur
	h.cur = nil
	h.mu.Unlock()
	if st == nil {
		// What is held is from a client that already left, whose stream
		// may not have let go yet; the one authorizing has sent nothing.
		h.retireInput(nil)
		return
	}
	h.stopWithin(st)
}

func (h *Host) trust(ctx context.Context, info transport.AuthInfo) (string, error) {
	name := info.DeviceName
	now := time.Now()
	if c, ok := h.pairs.lookup(info.ClientFingerprint); ok {
		if err := h.pairs.seen(info.ClientFingerprint, now); err != nil {
			h.logf("planck: note when %s was last seen: %v", c.Name, err)
		}
		return "paired:" + c.Name, nil
	}
	if info.PINVerified {
		if err := h.pairs.add(info.ClientFingerprint, PairedClient{Name: name, Method: "pin", PairedAt: now, LastSeen: now}); err != nil {
			return "", err
		}
		if h.cfg.PIN == "" {
			if pin, err := newPIN(); err == nil {
				h.pinMu.Lock()
				h.pin = pin
				h.pinMu.Unlock()
			}
		}
		return "pin:" + name, nil
	}
	if h.whois != nil && !h.cfg.NoTailscale && !(h.requireLANPIN && isLANAddr(info.RemoteAddr.Addr())) {
		id, err := h.whois.WhoIs(ctx, info.RemoteAddr.String())
		if err == nil && id.IsSameUser {
			if err := h.pairs.add(info.ClientFingerprint, PairedClient{Name: name, Method: "tailscale:" + id.LoginName, PairedAt: now, LastSeen: now}); err != nil {
				return "", err
			}
			return "tailscale:" + id.LoginName, nil
		}
	}
	if info.Method == wire.AuthPIN {
		return "", errors.New("wrong PIN")
	}
	return "", errors.New("this device is not paired: enter the host's PIN")
}

// tailnetULA is Tailscale's IPv6 range, inside the private fc00::/7.
var tailnetULA = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// isLANAddr reports whether a is a private or link-local address off the
// tailnet: a peer on the local network.
func isLANAddr(a netip.Addr) bool {
	a = a.Unmap()
	return (a.IsPrivate() || a.IsLinkLocalUnicast()) && !tailnetULA.Contains(a)
}

func (h *Host) negotiate(req wire.SessionConfig) (wire.SessionConfig, error) {
	c := h.cfg
	acc := req
	switch {
	case c.Codec != 0:
		acc.Codec = codecToWire(c.Codec)
	case acc.Codec == 0:
		acc.Codec = wire.CodecHEVC
	}
	switch {
	case c.Chroma != 0:
		acc.Chroma = chromaToWire(c.Chroma)
	case acc.Chroma == 0:
		acc.Chroma = wire.Chroma420
	}
	if c.Width > 0 && c.Height > 0 && (c.Synth || req.Width == 0) {
		acc.Width, acc.Height = uint16(c.Width), uint16(c.Height)
	}
	if c.FPS > 0 && (req.FPS == 0 || int(req.FPS) > c.FPS) {
		acc.FPS = uint16(c.FPS)
	}
	if c.BitrateKbps > 0 && (req.BitrateKbps == 0 || int(req.BitrateKbps) > c.BitrateKbps) {
		acc.BitrateKbps = uint32(c.BitrateKbps)
	}
	if acc.BitrateKbps == 0 {
		acc.BitrateKbps = 20_000
	}
	if c.Slices > 0 {
		acc.Slices = uint8(c.Slices)
	}
	acc.Slices = max(1, min(acc.Slices, wire.MaxSlices))
	acc.SubFrame = c.SubFrame && acc.Slices > 1
	if c.NoAudio || req.AudioChannels == 0 {
		acc.AudioSampleRate, acc.AudioChannels = 0, 0
	} else {
		acc.AudioSampleRate, acc.AudioChannels = audio.SampleRate, audio.Channels
	}
	return acc, nil
}

// chroma is the coded chroma for an accepted config: the host's own choice
// when set, else the wire's subsampling at 8 bits.
func (h *Host) chroma(acc wire.SessionConfig) encode.Chroma {
	if h.cfg.Chroma != 0 {
		return h.cfg.Chroma
	}
	if acc.Chroma == wire.Chroma444 {
		return encode.YUV444
	}
	return encode.NV12
}

func (h *Host) onInput(from *transport.Session, in wire.Input) {
	recv := transport.Mono()
	if h.inputTrace != nil {
		defer h.traceInput(in, recv)
	}
	ev, err := fromWire(in)
	if err != nil {
		h.inputErr.Add(1)
		return
	}
	h.inputs.Add(1)
	h.lastInput.Store(h.now())
	if h.cfg.OnInput != nil {
		h.cfg.OnInput(ev)
	}
	if h.inject != nil {
		injected, err := h.injectHeld(from, ev)
		if err != nil {
			h.inputErr.Add(1)
			return
		}
		if injected && ev.Type == input.TypeMouseMoveAbs {
			h.absTarget.Store(uint32(ev.MouseAbs.X)<<16 | uint32(ev.MouseAbs.Y))
			h.absMoves.Add(1)
		}
	}
}

// injectHeld injects ev from the session from, asking for Ctrl+Alt+Del
// when it is that, and notes what it holds down, unless Stop closed the
// injector, another session owns the input, from was retired, or the
// gamepad settings drop it. Checking and noting under injMu keeps a press,
// or a Ctrl+Alt+Del, from landing after its client's keys were let go of.
func (h *Host) injectHeld(from *transport.Session, ev input.Event) (bool, error) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if h.injClosed {
		return false, nil
	}
	if _, gone := h.retired[from]; gone || h.inputOwner != nil && h.inputOwner != from {
		return false, nil
	}
	h.inputOwner = from
	h.secureAttention(ev)
	if ev.Type == input.TypeGamepad {
		if dropped, err := h.injectGamepad(ev); dropped || err != nil {
			return false, err
		}
	} else if err := h.inject.Inject(ev); err != nil {
		return false, err
	}
	h.held.Note(ev)
	return true, nil
}

// claimInput makes sess, just admitted, the session whose input is
// injected, letting go of what another session held.
func (h *Host) claimInput(sess *transport.Session) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if h.inputOwner != nil && h.inputOwner != sess {
		h.retireLocked(h.inputOwner)
	}
	delete(h.retired, sess)
	h.inputOwner = sess
}

// retireInput drops the rest of sess's input and lets go of what it held
// when it owns the input; a nil sess retires whichever session does. A
// session that ends after another took over leaves that one's keys down.
func (h *Host) retireInput(sess *transport.Session) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if sess == nil {
		sess = h.inputOwner
		if sess == nil {
			if h.inject != nil && !h.injClosed {
				h.releaseLocked()
			}
			return
		}
	}
	h.retireLocked(sess)
}

func (h *Host) retireLocked(sess *transport.Session) {
	if h.retired == nil {
		h.retired = make(map[*transport.Session]struct{})
	}
	h.retired[sess] = struct{}{}
	if h.inputOwner != sess {
		return
	}
	h.inputOwner = nil
	if h.inject != nil && !h.injClosed {
		h.releaseLocked()
	}
}

// retireStream retires st's session unless st already ended, so a late
// stop cannot leave an ended session in retired for good.
func (h *Host) retireStream(st *stream) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if !st.inputDone {
		h.retireLocked(st.sess)
	}
}

// endInput lets go of what st's session held, when it owns the input,
// and drops it from retired once its session closed, when no callback of
// it can run any more.
func (h *Host) endInput(st *stream) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	st.inputDone = true
	h.retireLocked(st.sess)
	delete(h.retired, st.sess)
}

// closeInput lets go of what is held and closes an injector the host
// opened. A stream whose teardown outlives Stop then injects nothing.
func (h *Host) closeInput() {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if h.inject == nil || h.injClosed {
		return
	}
	h.releaseLocked()
	h.injClosed = true
	if h.cfg.Injector == nil {
		h.inject.Close()
	}
}

func (h *Host) releaseLocked() {
	for _, ev := range h.held.Release() {
		if err := h.inject.Inject(ev); err != nil {
			h.logf("planck: let go of %+v: %v", ev, err)
		}
	}
}

// targetInput points the injector's absolute moves at the display a newly
// opened pipe captures: a virtual display, one that changed size or
// place, or the physical one.
func (h *Host) targetInput(info VideoInfo) {
	h.injMu.Lock()
	defer h.injMu.Unlock()
	if t, ok := h.inject.(input.Targeter); ok && !h.injClosed {
		t.SetTarget(info.Display, info.Desktop)
		if info.Display.W > 0 {
			h.logf("planck: input on %+v of desktop %+v", info.Display, info.Desktop)
		}
	}
}

func (h *Host) traceInput(in wire.Input, recv int64) {
	h.mu.Lock()
	st := h.cur
	h.mu.Unlock()
	if st == nil {
		return
	}
	c := st.sess.Clock()
	h.inputTrace.add(inputTraceRow{Seq: in.Seq, Sent: c.ToLocal(in.Timestamp), Recv: recv, Injected: transport.Mono(), RTTNs: int64(c.RTT())})
}

func (h *Host) onControl(m wire.Message) {
	switch m := m.(type) {
	case *wire.RefInvalidate:
		h.mu.Lock()
		st := h.cur
		h.mu.Unlock()
		if st != nil {
			st.invalidate(m.FrameID)
		}
	case *wire.StatsReport:
		h.mu.Lock()
		h.report = *m
		st := h.cur
		h.mu.Unlock()
		if st != nil && st.sess.PeerFeatures()&wire.FeatureHostStats != 0 {
			_ = st.sess.SendControl(st.hostStats())
		}
	case *wire.Resize:
		h.mu.Lock()
		st := h.cur
		h.mu.Unlock()
		if st != nil {
			st.requestResize(resizeReq{w: int(m.Width), h: int(m.Height), display: clientDisplayOf(m.Display)})
		}
	case *wire.SwitchDisplay:
		if st := h.current(); st != nil {
			st.requestDisplay(m.DisplayID)
		}
	}
}

func (h *Host) current() *stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur
}

func (h *Host) onMic(hdr wire.AudioHeader, payload []byte) {
	if st := h.current(); st != nil && st.mic != nil {
		st.mic.push(hdr, payload)
	}
}

func (h *Host) liveSession() *transport.Session {
	if st := h.current(); st != nil {
		return st.sess
	}
	return nil
}

func (h *Host) peerReceivesClipboard() bool {
	st := h.current()
	return st != nil && st.sess.PeerState().ClipboardRecv
}

// micStatus is the virtual input device the client's microphone would
// play into, or why there is none.
func (h *Host) micStatus() (audio.VirtualMic, string) {
	if h.cfg.NoMic {
		return audio.VirtualMic{}, "The host turned the microphone off"
	}
	find := h.cfg.FindVirtualMic
	if find == nil {
		find = audio.FindVirtualMic
	}
	dev, err := find(h.cfg.MicDevice)
	switch {
	case err == nil:
		return dev, ""
	case errors.Is(err, audio.ErrNoVirtualMic):
		return audio.VirtualMic{}, audio.VirtualMicHint
	case errors.Is(err, audio.ErrVirtualMicSetup):
		return audio.VirtualMic{}, strings.TrimPrefix(err.Error(), "audio: ")
	}
	return audio.VirtualMic{}, "Virtual mic unavailable: " + err.Error()
}

// SetClipboard changes what the host shares, at once.
func (h *Host) SetClipboard(c ClipboardConfig) {
	if h.clip == nil {
		return
	}
	h.clip.setConfig(c)
	if st := h.current(); st != nil {
		st.sendPeerState()
	}
}

// Stats is a snapshot for a UI or a log line.
func (h *Host) Stats() HostStats {
	h.mu.Lock()
	st, report, lastErr := h.cur, h.report, h.lastErr
	h.mu.Unlock()
	out := HostStats{Report: report, Sessions: h.sessions.Load(), InputEvents: h.inputs.Load(),
		InputErrors: h.inputErr.Load(), LastError: lastErr}
	if st != nil {
		st.fill(&out)
	}
	if h.clip != nil {
		out.Clipboard = h.clip.stats()
	}
	return out
}

func (h *Host) closeClipboard() {
	if h.clip != nil {
		h.clip.close()
	}
}

func (h *Host) closeDisplays() {
	if err := h.closeVDD(); err != nil {
		h.logf("planck: virtual display driver: %v", err)
	}
}

// Stop disconnects the client and closes the listeners.
func (h *Host) Stop() {
	h.stopOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
		h.mu.Lock()
		h.stopping = true
		srv, st := h.srv.Load(), h.cur
		h.mu.Unlock()
		if srv != nil {
			srv.Close()
		}
		if h.disc != nil {
			h.disc.Close()
		}
		if !h.within(func() {
			if st != nil {
				h.stopStream(st)
			}
			h.wg.Wait()
		}) {
			h.fail("the host's streams took over %v to stop; stopping without them", h.stopWait)
		}
		if !h.within(h.closeInput, h.closeClipboard, h.closeDisplays) {
			h.fail("the host's input, clipboard, or virtual displays took over %v to close; stopping without them", h.stopWait)
		}
		h.setStarted(false)
		close(h.done)
	})
}
