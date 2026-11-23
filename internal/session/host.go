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
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
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
	Name      string
	// ListenAddr is the QUIC control address; empty means ":47998".
	ListenAddr string
	// MediaPort is the UDP media port; 0 picks a free one per session.
	MediaPort int
	// DiscoveryAddr answers LAN probes and sends beacons; empty means
	// ":47997", "off" disables discovery.
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
	// Synth streams a generated pattern at Width x Height and FPS on the
	// Adapter GPU instead of a display, for benchmarks without a monitor.
	Synth bool

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
	// OpenAudio replaces the platform loopback capturer.
	OpenAudio func() (audio.Capturer, error)
	// Injector replaces the platform injector when InjectInput is set.
	Injector input.Injector
	// OnInput sees every valid input event, injected or not.
	OnInput func(input.Event)
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
		ListenAddr:    fmt.Sprintf(":%d", 47998),
		MediaPort:     47999,
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
	InputEvents  uint64
	InputErrors  uint64
	// Report is the client's last StatsReport.
	Report    wire.StatsReport
	Sessions  uint64
	LastError string
}

// Host serves one client at a time; a new client takes over.
type Host struct {
	cfg    HostConfig
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
	if cfg.Name == "" {
		cfg.Name = stored.Name
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
		cfg.ListenAddr = ":47998"
	}
	if cfg.DiscoveryAddr == "" {
		cfg.DiscoveryAddr = fmt.Sprintf(":%d", discovery.DefaultPort)
	}
	if cfg.FPS <= 0 && cfg.Synth {
		cfg.FPS = 60
	}
	h := &Host{cfg: cfg, hostID: stored.HostID, cert: cert, fp: transport.CertFingerprint(cert), pairs: pairs,
		whois: cfg.WhoIs, open: cfg.OpenVideo, logf: cfg.Logf, pin: cfg.PIN}
	if h.logf == nil {
		h.logf = log.Printf
	}
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
func (h *Host) Name() string { return h.cfg.Name }

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
	mediaHost, _, err := net.SplitHostPort(h.cfg.ListenAddr)
	if err != nil {
		return err
	}
	srv, err := transport.Listen(h.cfg.ListenAddr, transport.ServerConfig{
		Certificate:  h.cert,
		Name:         h.cfg.Name,
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
	})
	if err != nil {
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
	b := discovery.Beacon{
		HostID:          h.hostID,
		Name:            h.cfg.Name,
		ControlPort:     uint16(h.srv.Addr().(*net.UDPAddr).Port),
		CertFingerprint: h.Fingerprint(),
		OS:              runtime.GOOS,
	}
	h.wg.Go(func() { _ = discovery.Serve(ctx, conn, b) })
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && !ua.IP.IsLoopback() {
		dst := discovery.BroadcastAddrs(uint16(ua.Port))
		h.wg.Go(func() { _ = discovery.Announce(ctx, conn, b, dst, 2*time.Second) })
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
		h.mu.Unlock()
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
		if h.inject != nil && h.cfg.Injector == nil {
			h.inject.Close()
		}
	})
}
