package session

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"runtime"
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
	// ReadCursor replaces cursor.Read, the host's system cursor that
	// clients reading wire.CursorState hear of.
	ReadCursor func() (cursor.State, error)
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
	logf   func(string, ...any)

	pinMu sync.Mutex
	pin   string

	srv      *transport.Server
	disc     net.PacketConn
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu       sync.Mutex
	cur      *stream
	report   wire.StatsReport
	lastErr  string
	sessions atomic.Uint64
	inputs   atomic.Uint64
	inputErr atomic.Uint64

	inputTrace *tracer[inputTraceRow]

	caps hostCapsCache
	vdd  vddManager
	clip *clipSync
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
		whois: cfg.WhoIs, open: cfg.OpenVideo, logf: cfg.Logf, pin: cfg.PIN}
	if h.logf == nil {
		h.logf = log.Printf
	}
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
	if h.srv != nil {
		h.srv.SetName(live)
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

// Addr is the control listener's address once started.
func (h *Host) Addr() net.Addr {
	if h.srv == nil {
		return nil
	}
	return h.srv.Addr()
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
	if h.cfg.Clipboard.any() {
		h.clip = openClipSync(h.cfg.OpenClipboard, h.cfg.Clipboard, h.liveSession, h.peerReceivesClipboard, h.logf)
		if h.clip != nil {
			scfg.Handlers.Clipboard = h.clip.incoming
		}
	}
	if h.cfg.OpenVideo == nil || h.cfg.Capabilities != nil {
		scfg.Capabilities, scfg.Resolve, scfg.Resize = h.capabilitiesJSON, h.resolve, true
	}
	srv, err := transport.Listen(h.cfg.ListenAddr, scfg)
	if err != nil {
		if h.clip != nil {
			h.clip.close()
		}
		return err
	}
	h.srv = srv
	if h.cfg.InjectInput && h.cfg.Injector == nil {
		inj, err := newInjector()
		if err != nil {
			h.logf("planck: input injection unavailable: %v", err)
		} else {
			h.inject = inj
		}
	} else if h.cfg.InjectInput {
		h.inject = h.cfg.Injector
	}
	ctx, h.cancel = context.WithCancel(ctx)
	if h.cfg.DiscoveryAddr != "off" {
		if err := h.startDiscovery(ctx); err != nil {
			h.cancel()
			srv.Close()
			return err
		}
	}
	h.wg.Go(func() { h.acceptLoop(ctx) })
	return nil
}

func (h *Host) startDiscovery(ctx context.Context) error {
	conn, err := net.ListenPacket("udp4", h.cfg.DiscoveryAddr)
	if err != nil {
		return fmt.Errorf("session: discovery: %w", err)
	}
	h.disc = conn
	base := discovery.Beacon{
		HostID:          h.hostID,
		ControlPort:     uint16(h.srv.Addr().(*net.UDPAddr).Port),
		CertFingerprint: h.Fingerprint(),
		OS:              runtime.GOOS,
	}
	beacon := func() discovery.Beacon {
		b := base
		b.Name = h.Name()
		return b
	}
	h.wg.Go(func() { _ = discovery.ServeFunc(ctx, conn, beacon) })
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && !ua.IP.IsLoopback() {
		dst := discovery.BroadcastAddrs(uint16(ua.Port))
		h.wg.Go(func() { _ = discovery.AnnounceFunc(ctx, conn, beacon, dst, 2*time.Second) })
	}
	return nil
}

func (h *Host) acceptLoop(ctx context.Context) {
	for {
		sess, err := h.srv.Accept(ctx)
		var he *transport.HandshakeError
		switch {
		case errors.As(err, &he):
			h.fail("client %v: %v", he.Remote, he.Err)
			continue
		case err != nil:
			return
		}
		h.sessions.Add(1)
		st := newStream(h, sess)
		h.mu.Lock()
		old := h.cur
		h.cur = st
		h.mu.Unlock()
		if old != nil {
			old.stop()
		}
		h.logf("planck: %s connected from %v (%s)", sess.Identity(), sess.RemoteControlAddr(), st.path)
		h.wg.Go(func() {
			st.run(ctx)
			h.mu.Lock()
			if h.cur == st {
				h.cur = nil
			}
			h.mu.Unlock()
		})
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
	if st != nil {
		st.stop()
	}
}

func (h *Host) trust(ctx context.Context, info transport.AuthInfo) (string, error) {
	name := info.DeviceName
	if c, ok := h.pairs.lookup(info.ClientFingerprint); ok {
		return "paired:" + c.Name, nil
	}
	if info.PINVerified {
		if err := h.pairs.add(info.ClientFingerprint, PairedClient{Name: name, Method: "pin", PairedAt: time.Now()}); err != nil {
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
	if h.whois != nil && !h.cfg.NoTailscale {
		id, err := h.whois.WhoIs(ctx, info.RemoteAddr.String())
		if err == nil && id.IsSameUser {
			if err := h.pairs.add(info.ClientFingerprint, PairedClient{Name: name, Method: "tailscale:" + id.LoginName, PairedAt: time.Now()}); err != nil {
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

func (h *Host) onInput(in wire.Input) {
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
	if h.cfg.OnInput != nil {
		h.cfg.OnInput(ev)
	}
	if h.inject != nil {
		if err := h.inject.Inject(ev); err != nil {
			h.inputErr.Add(1)
		}
	}
}

// targetInput points the injector's absolute moves at the display a newly
// opened pipe captures: a virtual display, one that changed size or
// place, or the physical one.
func (h *Host) targetInput(info VideoInfo) {
	if t, ok := h.inject.(input.Targeter); ok {
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
			st.requestResize(int(m.Width), int(m.Height))
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

// Stop disconnects the client and closes the listeners.
func (h *Host) Stop() {
	h.stopOnce.Do(func() {
		if h.cancel != nil {
			h.cancel()
		}
		if h.srv != nil {
			h.srv.Close()
		}
		if h.disc != nil {
			h.disc.Close()
		}
		h.mu.Lock()
		st := h.cur
		h.mu.Unlock()
		if st != nil {
			st.stop()
		}
		h.wg.Wait()
		if h.clip != nil {
			h.clip.close()
		}
		if err := h.vdd.close(); err != nil {
			h.logf("planck: virtual display driver: %v", err)
		}
		if h.inject != nil && h.cfg.Injector == nil {
			h.inject.Close()
		}
	})
}
