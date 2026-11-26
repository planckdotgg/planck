package session

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/internal/rtprio"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/clipboard"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/player"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/tailscale"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// PathInfoer reports how the tailnet reaches a peer; *tailscale.Client is
// one.
type PathInfoer interface {
	PathInfo(ctx context.Context, peerIP netip.Addr) (tailscale.PathInfo, error)
}

// Quality is what the client asks the host to stream.
type Quality struct {
	Codec       encode.Codec
	Chroma      encode.Chroma
	Width       int
	Height      int
	FPS         int
	BitrateKbps int
	Slices      int
}

// ClientConfig configures a Client.
type ClientConfig struct {
	// ConfigDir holds client.json (the pinned hosts) and the client
	// identity. Empty uses config.Dir().
	ConfigDir string
	// Name is the device name the host shows and pairs under.
	Name    string
	Quality Quality
	// Overrides go to a host that resolves the stream from both ends'
	// capabilities; an older host gets Quality only.
	Overrides defaults.UserOverrides
	// Capabilities replaces the probe of this device's display and
	// decoders.
	Capabilities func() defaults.ClientCapabilities
	// ViewSize is the size in pixels of the window the stream shows in;
	// nil, as headless, has none. A stream on a virtual display at
	// Resolution auto starts at this size and follows Resize.
	ViewSize func() (w, h int)
	// PIN is the host's pairing PIN, needed the first time unless both
	// devices belong to the same Tailscale user.
	PIN     string
	NoAudio bool

	// OpenSink makes the video sink; nil decodes headless (OpenDecodeSink).
	OpenSink SinkOpener
	// OpenAudioPlayer replaces the default output device.
	OpenAudioPlayer func() (audio.Player, error)
	// OpenMic replaces audio.NewMicCapturer.
	OpenMic func() (audio.MicCapturer, error)
	// Clipboard is what this client shares: Send is client to host,
	// Receive host to client.
	Clipboard ClipboardConfig
	// OpenClipboard replaces clipboard.Open.
	OpenClipboard func() (clipboard.Board, error)
	// Tailscale replaces the local daemon client for path detection.
	Tailscale PathInfoer
	// OnRumble receives gamepad rumble from the host.
	OnRumble func(input.RumbleEvent)
	// Gamepads sends this device's controllers while a stream runs, and
	// plays the host's rumble on them.
	Gamepads *GamepadPump
	// OnCursor receives the host's cursor state, from a host that sends it.
	OnCursor func(wire.CursorState)
	// OnCursorShape receives the host's cursor image, whole, from a host
	// that sends it; setting it asks the host for it.
	OnCursorShape    func(wire.CursorShape)
	HandshakeTimeout time.Duration
	SimulateLoss     float64
	// Route says how a live stream moves between the host's routes.
	Route RouteConfig
	Logf  func(format string, args ...any)
}

// RouteConfig tunes the route monitor that runs beside each stream. A
// stream whose route stalls or drops moves to another candidate that
// answers, and a stream on a tailnet route moves to the LAN when
// PreferLAN is set and a LAN candidate answers lanUpgradeAfter probes in a
// row. Each move dials the host again on the new route, so the media path
// takes that route's datagram size and bitrate ceiling, while the video
// sink, the decoder and audio play-out carry on.
type RouteConfig struct {
	PreferLAN bool
	// ProbeInterval is how often the LAN is probed from a tailnet route;
	// zero is DefaultProbeInterval.
	ProbeInterval time.Duration
	// StallTimeout is how long a route may deliver nothing before the
	// stream leaves it; zero is DefaultStallTimeout. The host pings four
	// times a second, so a live route is never quiet this long.
	StallTimeout time.Duration
}

const (
	DefaultProbeInterval = 4 * time.Second
	DefaultStallTimeout  = 2500 * time.Millisecond
	lanUpgradeAfter      = 2
	// raceBeforeAddr is how long the candidates may race before a target's
	// address is dialed instead.
	raceBeforeAddr = time.Second
	// raceTimeout bounds the race of a target with no address to fall
	// back on: a host discovery found may have gone since.
	raceTimeout = 5 * time.Second
	// rerouteFor is how long a stream that lost its route tries the
	// others before it ends.
	rerouteFor = 10 * time.Second
)

// Target says where to connect: discovery candidates, raced LAN direct
// first, then Tailscale direct, then DERP; or an explicit host:port, which
// is also the fallback when no candidate answers.
type Target struct {
	Candidates []discovery.Candidate
	Addr       string
	// HostID and Fingerprint, when known from a beacon, key the pin and
	// must match the host on first contact.
	HostID      string
	Fingerprint string
}

// TargetFromAnnouncement connects to a host found by discovery.
func TargetFromAnnouncement(a discovery.Announcement) Target {
	return Target{Candidates: []discovery.Candidate{a.Candidate()}, HostID: a.Beacon.HostID, Fingerprint: a.Beacon.CertFingerprint}
}

// ClientStats is a live snapshot. Latencies are milliseconds; Encode is on
// the host's clock, the rest map host stamps onto the client's clock with
// the ping/pong offset.
type ClientStats struct {
	Connected bool
	HostName  string
	HostID    string
	Identity  string
	Addr      string
	Path      string
	Codec     string
	Chroma    string
	// Profile is what the host resolved the stream to, nil from an older
	// host.
	Profile *defaults.ResolvedProfile
	Width   int
	Height  int
	FPS     float64
	Kbps    float64
	// DatagramBytes is the largest media datagram of the route, and
	// MaxBitrateKbps the bitrate the host accepted on it.
	DatagramBytes  int
	MaxBitrateKbps int
	// RouteSwitches counts the moves to another route.
	RouteSwitches uint64

	EncodeMs  float64
	NetworkMs float64
	RTTMs     float64
	DecodeMs  float64
	PresentMs float64
	E2EMs     float64
	E2E       Latency
	Network   Latency
	// Encode, Decode and Present summarize the last frames' stage times.
	Encode  Latency
	Decode  Latency
	Present Latency
	// Glass is from the present call to the picture reaching the screen,
	// and E2EGlass from the host's capture to that: what a viewer sees.
	// Only a sink that presents on macOS reports them.
	Glass    Latency
	E2EGlass Latency

	// CaptureMs and HostEncodeMs split Encode where the host acquired the
	// picture, as p50s; both are 0 from a host that does not send
	// HostStats.
	CaptureMs    float64
	HostEncodeMs float64

	// FECParityPct is the parity share of the video datagrams received,
	// estimated from datagram counts.
	FECParityPct   float64
	LossPct        float64
	FramesReceived uint64
	FramesLost     uint64
	FramesDropped  uint64
	// FramesPartial counts PyroWave frames shown with chunks missing.
	FramesPartial   uint64
	SlicesRecovered uint64
	SinkErrors      uint64
	// Keyframes counts the keyframes decoded, and FrameTimesMs holds the
	// gaps between the last FrameTimeRing pictures decoded, oldest first.
	Keyframes    uint64
	FrameTimesMs []float64
	// HostPolicy is what the host's owner forces on clients, which beats
	// this client's overrides; all auto from a host that sent none.
	HostPolicy defaults.UserOverrides
	// HostDisplays are the displays the host advertised, which
	// SwitchDisplay moves the stream between; nil from an older host.
	HostDisplays  []defaults.HostDisplay
	AudioBufferMs float64
	AudioPackets  uint64
	InputSent     uint64
	Mic           MicStats
	Clipboard     ClipboardStats
}

// Client connects to one host at a time.
type Client struct {
	cfg  ClientConfig
	dir  string
	logf func(string, ...any)
	// activity counts what a route delivered, for stall detection.
	activity func(*transport.Session) uint64

	mu     sync.Mutex
	conn   *conn
	target Target
	clip   *clipSync
	// gain is the playback volume, a float64's bits.
	gain atomic.Uint64
	// beforeLive, for tests, runs as connect is about to make its route live.
	beforeLive func(*conn, *route)
}

// NewClient prepares a client; the identity is created on first Connect.
func NewClient(cfg ClientConfig) (*Client, error) {
	dir := cfg.ConfigDir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	c := &Client{cfg: cfg, dir: dir, logf: cfg.Logf,
		activity: func(s *transport.Session) uint64 { return s.Stats().PacketsReceived }}
	c.gain.Store(math.Float64bits(1))
	if c.logf == nil {
		c.logf = log.Printf
	}
	if c.cfg.Tailscale == nil {
		c.cfg.Tailscale = tailscale.New()
	}
	return c, nil
}

// conn is one stream and its goroutines. Its route changes under it.
type conn struct {
	live   atomic.Pointer[route]
	hostID string
	name   string
	// stream is the stream being decoded: its epoch and the sink it
	// needs. A new route must carry the same.
	stream   atomic.Pointer[streamState]
	switches atomic.Uint64
	// intra marks a PyroWave stream: frames decode alone, so loss never
	// waits for a keyframe.
	intra   atomic.Bool
	profile atomic.Pointer[defaults.ResolvedProfile]
	// policy is what the host's owner forces on its clients, from its
	// capabilities; nil from a host that sent none.
	policy atomic.Pointer[defaults.UserOverrides]
	// displays are the host's displays, from its capabilities; set before
	// the conn is shared, and never changed.
	displays []defaults.HostDisplay
	// openSink makes the sink of each stream.
	openSink SinkOpener
	// asked is the window last offered the host, at the connect or a
	// route's or in a Resize.
	asked atomic.Pointer[viewOffer]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// done closes when the stream ends for good, with err saying why.
	done    chan struct{}
	endOnce sync.Once
	err     error

	frames chan *frameBuf
	free   chan *frameBuf

	jb       *audio.JitterBuffer
	player   audio.Player
	gain     *atomic.Uint64
	mic      *micSender
	inputSeq atomic.Uint32

	mu        sync.Mutex
	e2e       *window
	network   *window
	encodeW   *window
	decodeW   *window
	presentW  *window
	glassW    *window
	e2eGlassW *window
	captured  [captureRing]capturedAt
	encodeMs  float64
	decodeMs  float64
	presentMs float64
	host      wire.HostStats
	recv      rate
	dataShard uint64
	// frameTimes are the gaps between the pictures decoded, and lastShown
	// when the last one was.
	frameTimes *window
	lastShown  int64
	keyframes  uint64
	// since is the session's counters when the current stream began.
	since transport.SessionStats

	lastPLI                             atomic.Int64
	dropped, sinkErr, audioPkts, inputs atomic.Uint64
	// decoding is set once the sink gave its first picture; until then the
	// queue may grow to warmupQueue. needKey discards frames until the next
	// keyframe, after one was lost.
	decoding, needKey atomic.Bool
	// ref skips the frames after a lost one, with reference invalidation.
	ref refRecovery

	trace *tracer[clientTraceRow]
}

// streamState is one stream of a session: the one it started with, epoch
// 0, or one a wire.Stream switched to. Only frames of its epoch decode.
type streamState struct {
	epoch uint8
	sink  decode.Config
}

// route is one transport session of a stream and the path it runs on.
type route struct {
	sess *transport.Session
	kind discovery.PathKind
	addr string
	// bye is set when the host said goodbye: it ended the stream, or gave
	// it to another client, and the stream must not come back.
	bye atomic.Bool
	// mu orders the route going live with the stream updates the host
	// sends on it first: those wait in pending, as the stream it describes
	// may be the one the connect opens its sink for.
	mu      sync.Mutex
	isLive  bool
	pending []wire.Message
}

func (cc *conn) session() *transport.Session { return cc.live.Load().sess }

// end stops the stream for good, and its goroutines; the first reason
// sticks.
func (cc *conn) end(err error) {
	cc.endOnce.Do(func() {
		cc.err = err
		close(cc.done)
		cc.cancel()
	})
}

// capturedAt maps a frame, by the PTS its picture carries, to the host's
// capture time on the client's clock, for pictures that reach the screen
// after the decode loop moved on.
type capturedAt struct {
	frame uint32
	at    int64
	row   int // the frame's trace row, or -1
}

const captureRing = 256

type frameBuf struct {
	info     wire.VideoInfo
	data     []byte
	arrival  int64
	firstPkt int64
	queued   int
}

const (
	frameQueue = 3
	// warmupQueue holds the frames that arrive while the decoder makes its
	// first picture: VideoToolbox takes tens of milliseconds for it.
	warmupQueue   = 16
	latencyWindow = 600
	minPLIGap     = 100 * time.Millisecond
)

// Connect dials the host, authenticates, pins the host on first approval
// and starts playback.
func (c *Client) Connect(ctx context.Context, t Target) error {
	c.Disconnect()
	cc, err := c.connect(ctx, t)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn, c.target = cc, t
	c.mu.Unlock()
	cc.wg.Go(func() { c.watchRoutes(cc, t) })
	if p := c.cfg.Gamepads; p != nil {
		id := p.Attach(c.SendInput)
		cc.wg.Go(func() {
			select {
			case <-cc.done:
			case <-cc.ctx.Done():
			}
			p.Detach(id)
		})
	}
	return nil
}

func (c *Client) connect(ctx context.Context, t Target) (*conn, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	r, err := c.resolve(ctx, t)
	if err != nil {
		return nil, err
	}
	cc := &conn{hostID: r.hostID, name: r.name, frames: make(chan *frameBuf, warmupQueue),
		free: make(chan *frameBuf, warmupQueue+2), e2e: newWindow(latencyWindow), network: newWindow(latencyWindow),
		encodeW: newWindow(latencyWindow), decodeW: newWindow(latencyWindow), presentW: newWindow(latencyWindow),
		glassW: newWindow(latencyWindow), e2eGlassW: newWindow(latencyWindow), frameTimes: newWindow(FrameTimeRing),
		done: make(chan struct{}), gain: &c.gain}
	for range warmupQueue + 2 {
		cc.free <- &frameBuf{}
	}
	cc.needKey.Store(true)
	cc.trace = newTracer[clientTraceRow](traceClientEnv)
	if !cfg.NoAudio {
		cc.jb = audio.NewJitterBuffer(audio.JitterConfig{})
	}
	rt, err := c.dialRoute(ctx, cc, r)
	for tried := []resolved{r}; errors.Is(err, transport.ErrUnanswered) && ctx.Err() == nil; tried = append(tried, r) {
		next, ok := c.nextRoute(ctx, t, tried)
		if !ok {
			break
		}
		c.logf("planck: %v; trying %s at %s", err, next.kind, next.addr)
		r = next
		rt, err = c.dialRoute(ctx, cc, r)
	}
	if err != nil {
		return nil, err
	}
	sess := rt.sess
	if cc.name == "" {
		cc.name = sess.PeerName()
	}
	acc := sess.Config()
	cc.intra.Store(acc.Codec == wire.CodecPyroWave)
	dcfg, prof := c.streamConfig(sess)
	cc.stream.Store(&streamState{sink: dcfg})
	cc.profile.Store(prof)
	cc.policy.Store(hostPolicy(sess))
	if caps := hostCaps(sess); caps != nil {
		cc.displays = caps.Displays
	}
	if c.beforeLive != nil {
		c.beforeLive(cc, rt)
	}
	// The receive goroutine reads the stream once the route is live.
	cc.goLive(rt, c.logf)
	dcfg = cc.stream.Load().sink
	cc.openSink = cfg.OpenSink
	if cc.openSink == nil {
		cc.openSink = OpenDecodeSink
	}
	sink, err := cc.newSink(dcfg)
	if err != nil {
		sess.Close()
		return nil, err
	}
	cc.ctx, cc.cancel = context.WithCancel(context.Background())
	cc.mic = newMicSender(cc, cfg.OpenMic, c.logf)
	c.startClipboard(cfg)
	cc.wg.Go(func() { cc.decodeLoop(sink, dcfg, c.logf) })
	if cc.jb != nil && acc.AudioChannels > 0 {
		openPlayer := cfg.OpenAudioPlayer
		if openPlayer == nil {
			openPlayer = newAudioPlayer
		}
		if p, err := openPlayer(); err != nil {
			c.logf("planck: audio output unavailable: %v", err)
		} else {
			cc.player = p
			cc.wg.Go(cc.audioLoop)
		}
	}
	cc.wg.Go(cc.reportLoop)
	return cc, nil
}

// newSink opens the sink for one stream, told its profile and observed.
func (cc *conn) newSink(dcfg decode.Config) (Sink, error) {
	sink, err := cc.openSink(dcfg)
	if err != nil {
		return nil, fmt.Errorf("session: video sink: %w", err)
	}
	if ps, ok := sink.(interface {
		SetProfile(*defaults.ResolvedProfile)
	}); ok {
		if prof := cc.profile.Load(); prof != nil {
			ps.SetProfile(prof)
		}
	}
	if o, ok := sink.(interface{ SetObserver(player.Observer) }); ok {
		o.SetObserver(viewObserver{cc})
	}
	return sink, nil
}

// dialRoute opens a session to the host on r for cc, checking the host's
// certificate against its pin, and pins the host on first approval. It
// records the route's address on the pin.
func (c *Client) dialRoute(ctx context.Context, cc *conn, r resolved) (*route, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	if p := cc.profile.Load(); p != nil {
		// A stream moving routes keeps the format its decoder was opened
		// for: the host resolves per route, and takes the stream over
		// from the old session before this client can refuse the new one.
		cfg.Overrides = defaults.Merge(cfg.Overrides, decoderFormat(p))
		if !p.VirtualDisplay && p.CaptureDisplay != "" {
			// And the host display it streams, which a switch may have
			// moved it to.
			cfg.Overrides.Display = p.CaptureDisplay
		}
	}
	stored, err := config.LoadClientConfig(c.dir)
	if err != nil {
		return nil, err
	}
	cert, err := config.LoadOrCreateIdentity(filepath.Join(c.dir, "client-cert.pem"), filepath.Join(c.dir, "client-key.pem"), "planck-client "+cfg.Name)
	if err != nil {
		return nil, err
	}
	pinKey := r.hostID
	if pinKey == "" {
		pinKey = "addr:" + r.addr
	}
	pinned, isPinned := stored.PinnedHosts[pinKey]
	var seen [32]byte
	verify := func(fp [32]byte) error {
		seen = fp
		want := r.fingerprint
		if isPinned {
			want = pinned.CertFingerprint
		}
		if want == "" {
			return nil
		}
		w, err := hex.DecodeString(want)
		if err != nil || subtle.ConstantTimeCompare(w, fp[:]) != 1 {
			return config.ErrFingerprintMismatch
		}
		return nil
	}
	method := wire.AuthTailscale
	switch {
	case cfg.PIN != "":
		method = wire.AuthPIN
	case isPinned:
		method = wire.AuthPaired
	}

	q := cfg.Quality
	req := wire.SessionConfig{Codec: codecToWire(q.Codec), Chroma: chromaToWire(q.Chroma), Width: uint16(q.Width), Height: uint16(q.Height),
		FPS: uint16(q.FPS), BitrateKbps: uint32(q.BitrateKbps), Slices: uint8(q.Slices)}
	if q.Codec == 0 {
		req.Codec = 0
	}
	if q.Chroma == 0 {
		req.Chroma = 0
	}
	if ceiling := r.kind.RecommendedMaxBitrateKbps(); req.BitrateKbps == 0 || int(req.BitrateKbps) > ceiling {
		req.BitrateKbps = uint32(ceiling)
	}
	if !cfg.NoAudio {
		req.AudioSampleRate, req.AudioChannels = audio.SampleRate, audio.Channels
	}

	capsJSON, offered, err := c.capabilities(cfg, r.addr, r.kind)
	if err != nil {
		return nil, err
	}
	cc.asked.Store(offerOf(offered, cfg.ViewSize != nil))

	rt := &route{kind: r.kind, addr: r.addr}
	var shapes shapeCache
	sess, err := transport.Dial(ctx, r.addr, transport.ClientConfig{
		Certificate: cert, Name: cfg.Name, VerifyHost: verify, Auth: method, PIN: cfg.PIN, Session: req,
		HandshakeTimeout: cfg.HandshakeTimeout, SimulateLoss: cfg.SimulateLoss, LANPath: r.kind == discovery.PathLANDirect,
		Capabilities: capsJSON, StreamSwitch: true, CursorShape: cfg.OnCursorShape != nil, RefInvalidation: true,
		Handlers: transport.Handlers{
			VideoFrame: cc.onFrame,
			Audio:      cc.onAudio,
			Control: func(m wire.Message) {
				switch m := m.(type) {
				case *wire.Goodbye:
					rt.bye.Store(true)
				case *wire.Profile, *wire.Stream:
					cc.streamUpdate(rt, m, c.logf)
				case *wire.HostStats:
					cc.mu.Lock()
					cc.host = *m
					cc.mu.Unlock()
				case *wire.CursorState:
					if cfg.OnCursor != nil {
						cfg.OnCursor(*m)
					}
				case *wire.CursorShape:
					if cfg.OnCursorShape == nil {
						break
					}
					if s, ok := shapes.resolve(*m); ok {
						cfg.OnCursorShape(s)
					} else {
						c.logf("planck: cursor shape %x is not cached", m.ID)
					}
				}
			},
			Clipboard: c.onClipboard,
			Rumble: func(r wire.Rumble) {
				if cfg.Gamepads != nil {
					cfg.Gamepads.Rumble(r)
				}
				if cfg.OnRumble != nil {
					cfg.OnRumble(input.RumbleEvent{Index: r.Pad, LowFreq: r.Low, HighFreq: r.High, DurationMs: r.DurationMs})
				}
			},
		},
	})
	if errors.Is(err, transport.ErrAuthDenied) && seen != ([32]byte{}) {
		return nil, &HostDeniedError{Fingerprint: hex.EncodeToString(seen[:]), Err: err}
	}
	if err != nil {
		return nil, err
	}
	rt.sess = sess
	c.sendPeerState(sess)
	if cfg.PIN != "" {
		// A PIN pairs once; the host rotates it, and reconnects use the pin.
		c.mu.Lock()
		c.cfg.PIN = ""
		c.mu.Unlock()
	}
	name := r.name
	if name == "" {
		name = sess.PeerName()
	}
	if err := c.pin(pinKey, r, name, seen); err != nil {
		sess.Close()
		return nil, err
	}
	return rt, nil
}

type resolved struct {
	addr, hostID, name, fingerprint string
	kind                            discovery.PathKind
	// probed is the candidate a race picked this from, zero for t.Addr.
	probed netip.AddrPort
}

// nextRoute resolves t again without the routes tried already: the
// candidates not raced to yet, then t.Addr.
func (c *Client) nextRoute(ctx context.Context, t Target, tried []resolved) (resolved, bool) {
	rest := t
	rest.Candidates = slices.DeleteFunc(slices.Clone(t.Candidates), func(cand discovery.Candidate) bool {
		return slices.ContainsFunc(tried, func(r resolved) bool { return r.probed == cand.Addr })
	})
	if slices.ContainsFunc(tried, func(r resolved) bool { return r.addr == t.Addr }) {
		rest.Addr = ""
	}
	if len(rest.Candidates) == 0 && rest.Addr == "" {
		return resolved{}, false
	}
	r, err := c.resolve(ctx, rest)
	if err != nil || slices.ContainsFunc(tried, func(p resolved) bool { return p.addr == r.addr && p.probed == r.probed }) {
		return resolved{}, false
	}
	return r, true
}

// resolve picks the control address and path: the race winner among the
// candidates, or the explicit address classified with Tailscale's view.
func (c *Client) resolve(ctx context.Context, t Target) (resolved, error) {
	r := resolved{addr: t.Addr, hostID: t.HostID, fingerprint: t.Fingerprint, kind: discovery.PathLANDirect}
	if len(t.Candidates) > 0 {
		// A host with discovery off never answers; its address does.
		wait := raceBeforeAddr
		if t.Addr == "" {
			wait = raceTimeout
		}
		rctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		res, err := discovery.Racer{}.Race(rctx, t.Candidates)
		if err == nil {
			return r.from(res), nil
		}
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		if t.Addr == "" {
			if errors.Is(err, context.DeadlineExceeded) {
				err = ErrNoAnswer
			}
			return r, err
		}
	}
	if t.Addr == "" {
		return r, errors.New("session: no host address or candidates")
	}
	host, _, err := net.SplitHostPort(t.Addr)
	if err != nil {
		return r, err
	}
	if ip, err := netip.ParseAddr(host); err == nil && ratectl.ClassifyAddr(ip) == ratectl.PathTailscaleDirect {
		r.kind = discovery.PathTailscaleDirect
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		pi, err := c.cfg.Tailscale.PathInfo(pctx, ip)
		cancel()
		if err == nil && !pi.Direct {
			r.kind = discovery.PathTailscaleDERP
		}
	}
	return r, nil
}

// decoderFormat is the overrides that make a host resolve p's codec,
// chroma and bit depth again.
func decoderFormat(p *defaults.ResolvedProfile) defaults.UserOverrides {
	o := defaults.UserOverrides{Codec: defaults.CodecPref(p.Codec.String()), Chroma: "420"}
	if p.Chroma444 {
		o.Chroma = "444"
	}
	if p.BitDepth == 8 || p.BitDepth == 10 {
		o.BitDepth = defaults.BitDepthPref(strconv.Itoa(p.BitDepth))
	}
	return o
}

// FrameTimeRing is how many frame times ClientStats keeps.
const FrameTimeRing = 120

// hostPolicy is the Forced overrides of the capabilities the host sent.
func hostPolicy(sess *transport.Session) *defaults.UserOverrides {
	if caps := hostCaps(sess); caps != nil {
		return &caps.Forced
	}
	return nil
}

// hostCaps are the capabilities the host sent, nil from an older host.
func hostCaps(sess *transport.Session) *defaults.HostCapabilities {
	js := sess.PeerCapabilities()
	if len(js) == 0 {
		return nil
	}
	var caps defaults.HostCapabilities
	if err := json.Unmarshal(js, &caps); err != nil {
		return nil
	}
	return &caps
}

// HostOS is the GOOS of the host streaming now, as it said, "" without a
// stream or from an older host.
func (c *Client) HostOS() string {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ""
	}
	if caps := hostCaps(cc.session()); caps != nil {
		return caps.OS
	}
	return ""
}

// streamConfig is the decoder config and resolved profile of sess's stream.
func (c *Client) streamConfig(sess *transport.Session) (decode.Config, *defaults.ResolvedProfile) {
	acc := sess.Config()
	codec := codecFromWire(acc.Codec)
	prof, err := ParseProfile(sess.Profile())
	if err != nil {
		c.logf("planck: %v", err)
	}
	b := sess.SocketBuffers()
	c.logf("planck: media socket buffers %d KiB receive, %d KiB send", b.Recv>>10, b.Send>>10)
	dcfg := decode.Config{Codec: codec, Width: int(acc.Width), Height: int(acc.Height), FPS: int(acc.FPS)}
	if prof != nil {
		dcfg.Chroma, dcfg.HDR10 = streamChroma(prof), prof.HDR10
		for _, w := range prof.Warnings {
			c.logf("planck: profile: %s", w)
		}
	}
	if codec == encode.PyroWave {
		dcfg.ChunkLen = sess.ShardSize()
	}
	return dcfg, prof
}

// capabilities is the JSON this client offers a host: its display,
// decoders and link, the route it found, and the user's overrides.
func (c *Client) capabilities(cfg ClientConfig, addr string, path discovery.PathKind) ([]byte, defaults.ClientDisplay, error) {
	probe := cfg.Capabilities
	if probe == nil {
		probe = ProbeClientCapabilities
	}
	caps := probe()
	if cfg.ViewSize != nil {
		caps.Display.ViewWidthPx, caps.Display.ViewHeightPx = cfg.ViewSize()
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		if name, link, err := routeLink(ap.Addr().Unmap()); err == nil {
			caps.Link = link
			c.logf("planck: route to %v via %s: wired %v, %d Mbps", ap.Addr(), name, link.Wired, link.SpeedMbps)
		}
	}
	if err := cfg.Overrides.Validate(); err != nil {
		return nil, caps.Display, err
	}
	js, err := json.Marshal(clientCaps{V: clientCapsVersion, Client: caps, Route: defaults.RouteInfo{Kind: routeKind(path)}, Overrides: cfg.Overrides})
	return js, caps.Display, err
}

// ProbeClientCapabilities reads this device's main display and hardware
// decoders.
func ProbeClientCapabilities() defaults.ClientCapabilities {
	return defaults.ClientCapabilities{OS: runtime.GOOS, Display: clientDisplay(), Decoders: clientDecoders(),
		Present10Bit: clientPresent10Bit, DirectDisplay: clientDirectDisplay, PresentHDR10: clientPresentHDR10}
}

// from is r on the path a race won.
func (r resolved) from(res discovery.Result) resolved {
	if r.fingerprint == "" {
		r.fingerprint = res.Beacon.CertFingerprint
	}
	r.addr, r.hostID, r.name, r.kind, r.probed = res.ControlAddr.String(), res.Beacon.HostID, res.Beacon.Name, res.Kind, res.Candidate.Addr
	return r
}

// pin records the host's certificate on first approval, and the name and
// route the host was just reached by.
func (c *Client) pin(key string, r resolved, name string, fp [32]byte) error {
	return config.UpdateClientConfig(c.dir, func(stored *config.ClientConfig) error {
		p, ok := stored.PinnedHosts[key]
		if !ok || !strings.EqualFold(p.CertFingerprint, hex.EncodeToString(fp[:])) {
			p.HostID, p.CertFingerprint, p.PairedAt = key, hex.EncodeToString(fp[:]), time.Now()
		}
		p.LastAddr = r.addr
		if name != "" {
			p.Name = name
		}
		if r.kind == discovery.PathLANDirect {
			p.LastLANAddr = r.addr
		} else {
			p.LastTailscaleAddr = r.addr
		}
		return stored.Pin(p)
	})
}

// watchRoutes follows cc's route until the stream ends: it moves the
// stream off a route that stalls or drops, and onto the LAN when that is
// preferred and answers.
func (c *Client) watchRoutes(cc *conn, t Target) {
	c.mu.Lock()
	rc := c.cfg.Route
	c.mu.Unlock()
	if rc.ProbeInterval <= 0 {
		rc.ProbeInterval = DefaultProbeInterval
	}
	if rc.StallTimeout <= 0 {
		rc.StallTimeout = DefaultStallTimeout
	}
	var lan []discovery.Candidate
	for _, cand := range t.Candidates {
		if cand.Kind == discovery.PathLANDirect {
			lan = append(lan, cand)
		}
	}
	tick := time.NewTicker(min(rc.ProbeInterval, rc.StallTimeout) / 4)
	defer tick.Stop()

	var (
		rt        = cc.live.Load()
		heard     uint64
		heardAt   = time.Now()
		probedAt  = time.Now()
		lanAnswer int
	)
	moved := func() {
		rt, heard, heardAt, probedAt, lanAnswer = cc.live.Load(), 0, time.Now(), time.Now(), 0
	}
	for {
		select {
		case <-cc.ctx.Done():
			return
		case <-rt.sess.Done():
			if rt.bye.Load() || transport.ClosedByPeer(rt.sess.Err()) {
				cc.end(rt.sess.Err())
				return
			}
			c.logf("planck: %s route to %s dropped: %v", rt.kind, rt.addr, rt.sess.Err())
			if err := c.reroute(cc, t, rt); err != nil {
				cc.end(err)
				return
			}
			moved()
			continue
		case <-tick.C:
		}
		now := time.Now()
		if n := c.activity(rt.sess); n != heard {
			heard, heardAt = n, now
		} else if now.Sub(heardAt) >= rc.StallTimeout {
			c.logf("planck: %s route to %s stalled for %v", rt.kind, rt.addr, now.Sub(heardAt).Round(time.Millisecond))
			if err := c.reroute(cc, t, rt); err != nil {
				cc.end(err)
				return
			}
			moved()
			continue
		}
		c.mu.Lock()
		preferLAN := c.cfg.Route.PreferLAN
		c.mu.Unlock()
		if !preferLAN || rt.kind == discovery.PathLANDirect || len(lan) == 0 || now.Sub(probedAt) < rc.ProbeInterval {
			continue
		}
		probedAt = now
		res, err := probeLAN(cc.ctx, lan, rc.ProbeInterval)
		if err != nil {
			lanAnswer = 0
			continue
		}
		if lanAnswer++; lanAnswer < lanUpgradeAfter {
			continue
		}
		r := resolved{hostID: cc.hostID, fingerprint: t.Fingerprint}.from(res)
		if err := c.switchRoute(cc, r); err != nil {
			c.logf("planck: move to the LAN at %s: %v", r.addr, err)
			lanAnswer = 0
			continue
		}
		moved()
	}
}

// probeLAN races the LAN candidates once, within a second or the probe
// interval if shorter.
func probeLAN(ctx context.Context, lan []discovery.Candidate, every time.Duration) (discovery.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, min(time.Second, every))
	defer cancel()
	return discovery.Racer{}.Race(ctx, lan)
}

// reroute moves cc off dead, trying routes of another kind first, until
// one takes the stream or rerouteFor passes.
func (c *Client) reroute(cc *conn, t Target, dead *route) error {
	deadline := time.Now().Add(rerouteFor)
	var others []discovery.Candidate
	for _, cand := range t.Candidates {
		if cand.Kind != dead.kind {
			others = append(others, cand)
		}
	}
	var err error
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		cands := t.Candidates
		if attempt == 0 && len(others) > 0 {
			cands = others
		}
		ctx, cancel := context.WithDeadline(cc.ctx, deadline)
		var r resolved
		r, err = c.resolve(ctx, Target{Candidates: cands, Addr: t.Addr, HostID: cc.hostID, Fingerprint: t.Fingerprint})
		if err == nil {
			err = c.switchRoute(cc, r)
		}
		cancel()
		if err == nil || cc.ctx.Err() != nil {
			return err
		}
		c.logf("planck: reroute: %v", err)
		select {
		case <-cc.ctx.Done():
			return cc.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("session: no route to the host: %w", err)
}

// switchRoute dials the host on r and moves cc's stream onto it. The host
// takes the new session over from the old one. The decoder waits for the
// new stream's first keyframe.
func (c *Client) switchRoute(cc *conn, r resolved) error {
	ctx, cancel := context.WithTimeout(cc.ctx, 5*time.Second)
	defer cancel()
	cc.needKey.Store(true)
	rt, err := c.dialRoute(ctx, cc, r)
	if err != nil {
		return err
	}
	dcfg, prof := c.streamConfig(rt.sess)
	cur := cc.stream.Load().sink
	dcfg.FPS = cur.FPS
	if dcfg.Codec != encode.PyroWave {
		// The hardware decoders take a new size from the stream itself,
		// which a resize or the window's size on this route may give.
		dcfg.Width, dcfg.Height = cur.Width, cur.Height
	}
	if dcfg != cur {
		rt.sess.Close()
		return fmt.Errorf("session: the host changed the stream to %dx%d %v chroma %v chunk %d on %s", dcfg.Width, dcfg.Height, dcfg.Codec, dcfg.Chroma, dcfg.ChunkLen, r.kind)
	}
	cc.profile.Store(prof)
	cc.policy.Store(hostPolicy(rt.sess))
	cc.ref.reset()
	cc.stream.Store(&streamState{sink: cur})
	cc.mu.Lock()
	cc.since = transport.SessionStats{}
	cc.mu.Unlock()
	if cc.jb != nil {
		cc.jb.Restart()
	}
	old := cc.goLive(rt, c.logf)
	cc.switches.Add(1)
	c.logf("planck: stream moved from %s %s to %s %s (%d-byte datagrams)", old.kind, old.addr, rt.kind, rt.addr, rt.sess.MaxDatagram())
	old.sess.Close()
	return nil
}

// onFrame runs on the receive goroutine: it copies the frame out and never
// blocks. A full queue drops the frame and asks for a keyframe, and the
// frames up to it are dropped too: they would reference the lost one, and a
// decoder fed them fails on each.
func (cc *conn) onFrame(info wire.VideoInfo, frame []byte) {
	now := transport.Mono()
	rt := cc.live.Load()
	if rt == nil {
		return
	}
	sess := rt.sess
	st := cc.stream.Load()
	if st == nil || info.Epoch != st.epoch {
		// A frame of the stream before a switch, or of the next one
		// before its wire.Stream: neither decodes on this sink.
		return
	}
	waitKey := cc.needKey.Load() && !cc.intra.Load()
	if waitKey && !info.Keyframe {
		cc.drop()
		return
	}
	if sess.RefInvalidation() && !cc.intra.Load() {
		a := cc.ref.admit(info, now)
		if a.invalidate {
			_ = sess.SendControl(&wire.RefInvalidate{FrameID: a.invalidFrom})
		}
		switch {
		case a.giveUp:
			cc.drop()
			return
		case !a.decode:
			cc.dropped.Add(1)
			return
		}
	}
	if waitKey {
		cc.needKey.Store(false)
	}
	limit := frameQueue
	if !cc.decoding.Load() {
		limit = warmupQueue
	}
	var fb *frameBuf
	select {
	case fb = <-cc.free:
	default:
		cc.lose(sess, info, now)
		return
	}
	if len(cc.frames) >= limit {
		cc.free <- fb
		cc.lose(sess, info, now)
		return
	}
	fb.info, fb.arrival = info, now
	fb.firstPkt, fb.queued = sess.FirstArrival(info.FrameID), len(cc.frames)
	fb.data = append(fb.data[:0], frame...)
	shard := sess.ShardSize()
	slices := max(1, int(sess.Config().Slices))
	data := 0
	for s := 0; s < slices; s++ {
		n := len(frame)*(s+1)/slices - len(frame)*s/slices
		data += (n + shard - 1) / shard
	}
	cc.mu.Lock()
	cc.dataShard += uint64(data)
	cc.mu.Unlock()
	cc.recv.add(now, len(frame))
	select {
	case cc.frames <- fb:
	default:
		cc.free <- fb
		cc.lose(sess, info, now)
	}
}

// lose drops a frame that was admitted to decode: the frames after it
// wait for a Recovery frame, or with no reference invalidation for a
// keyframe.
func (cc *conn) lose(sess *transport.Session, info wire.VideoInfo, now int64) {
	if !sess.RefInvalidation() || cc.intra.Load() || info.Keyframe {
		cc.drop()
		return
	}
	cc.dropped.Add(1)
	cc.ref.lose(info.FrameID, now)
	_ = sess.SendControl(&wire.RefInvalidate{FrameID: info.FrameID})
}

func (cc *conn) drop() {
	cc.dropped.Add(1)
	if cc.intra.Load() {
		return
	}
	cc.needKey.Store(true)
	cc.requestKey()
}

// requestKey sends at most one PLI per minPLIGap.
func (cc *conn) requestKey() {
	rt := cc.live.Load()
	now := transport.Mono()
	last := cc.lastPLI.Load()
	if rt == nil || now-last < int64(minPLIGap) || !cc.lastPLI.CompareAndSwap(last, now) {
		return
	}
	_ = rt.sess.SendControl(&wire.PLI{})
}

// decodeLoop runs on a thread of its own at a raised class, which ends
// with it.
func (cc *conn) decodeLoop(sink Sink, sinkCfg decode.Config, logf func(string, ...any)) {
	defer func() {
		if sink != nil {
			sink.Close()
		}
	}()
	if err := rtprio.Boost(); err != nil {
		logf("planck: decode thread priority: %v", err)
	}
	for {
		var fb *frameBuf
		select {
		case <-cc.ctx.Done():
			return
		case fb = <-cc.frames:
		}
		if st := cc.stream.Load(); fb.info.Epoch != st.epoch {
			cc.free <- fb
			continue
		} else if newSinkFor(sinkCfg, st.sink) {
			sink.Close()
			sink = nil
			var err error
			if sink, err = cc.newSink(st.sink); err != nil {
				cc.free <- fb
				cc.end(err)
				return
			}
			sinkCfg = st.sink
			logf("planck: decoding %v %dx%d", sinkCfg.Codec, sinkCfg.Width, sinkCfg.Height)
		}
		clock := cc.session().Clock()
		dequeued := transport.Mono()
		t, err := sink.Submit(fb.data, int64(fb.info.FrameID))
		shown := transport.Mono()
		info, arrival := fb.info, fb.arrival
		row := -1
		if cc.trace != nil {
			off := clock.Offset()
			row = cc.trace.add(clientTraceRow{Frame: info.FrameID, Key: info.Keyframe, Bytes: len(fb.data),
				Present: info.CaptureTS - off, EncEnd: info.EncodeTS - off, FirstPkt: fb.firstPkt, Complete: arrival,
				QueueLen: fb.queued, Dequeue: dequeued, DecodeNs: int64(t.Decode), PresentNs: int64(t.Present), Done: shown,
				OffsetNs: off, RTTNs: int64(clock.RTT()), Err: err != nil})
		}
		cc.free <- fb
		if err != nil && !errors.Is(err, decode.ErrNoFrame) {
			if cc.sinkErr.Add(1) == 1 {
				logf("planck: video sink: %v", err)
			}
			if !cc.intra.Load() {
				cc.needKey.Store(true)
				cc.requestKey()
			}
			continue
		}
		if err == nil {
			cc.decoding.Store(true)
		}
		cc.mu.Lock()
		cc.encodeMs = ms(info.EncodeTS - info.CaptureTS)
		cc.decodeMs = ms(int64(t.Decode))
		cc.encodeW.add(cc.encodeMs)
		if err == nil {
			cc.decodeW.add(cc.decodeMs)
			if cc.lastShown != 0 {
				cc.frameTimes.add(ms(shown - cc.lastShown))
			}
			cc.lastShown = shown
			if info.Keyframe {
				cc.keyframes++
			}
		}
		if clock.Samples() > 0 {
			cc.network.add(ms(arrival - clock.ToLocal(info.EncodeTS)))
			if err == nil {
				cc.e2e.add(ms(shown - clock.ToLocal(info.CaptureTS)))
				cc.captured[info.FrameID%captureRing] = capturedAt{info.FrameID, clock.ToLocal(info.CaptureTS), row}
			}
		}
		cc.mu.Unlock()
	}
}

// newSinkFor reports whether a sink opened for cur cannot decode next:
// another codec or format, or another PyroWave size. The hardware
// decoders take a new size from the stream itself.
func newSinkFor(cur, next decode.Config) bool {
	if next.Codec != encode.PyroWave {
		next.Width, next.Height = cur.Width, cur.Height
	}
	return cur != next
}

// viewObserver takes a presenting sink's present and glass times, from
// its threads.
type viewObserver struct{ cc *conn }

func (o viewObserver) Presented(t player.Timing, err error) {
	if err != nil {
		return
	}
	cc := o.cc
	at := transport.Mono() - int64(t.Present)
	id := uint32(t.PTS)
	cc.mu.Lock()
	cc.presentMs = ms(int64(t.Present))
	cc.presentW.add(cc.presentMs)
	c := cc.captured[id%captureRing]
	cc.mu.Unlock()
	if c.frame == id {
		cc.trace.update(c.row, func(r *clientTraceRow) { r.PresentAt, r.PresentNs = at, int64(t.Present) })
	}
}

func (o viewObserver) Glass(pts int64, glass, since time.Duration) {
	cc := o.cc
	at := transport.Mono() - int64(since)
	id := uint32(pts)
	cc.mu.Lock()
	cc.glassW.add(ms(int64(glass)))
	c := cc.captured[id%captureRing]
	if c.frame == id && c.at != 0 {
		cc.e2eGlassW.add(ms(at - c.at))
	}
	cc.mu.Unlock()
	if c.frame == id {
		cc.trace.update(c.row, func(r *clientTraceRow) { r.GlassAt = at })
	}
}

func (cc *conn) onAudio(h wire.AudioHeader, opus []byte) {
	if cc.jb == nil {
		return
	}
	cc.jb.Push(uint16(h.Seq), opus)
	cc.audioPkts.Add(1)
}

// audioLoop plays one 10 ms frame per tick on transport.Mono deadlines; the
// jitter buffer and the device queue absorb clock drift.
func (cc *conn) audioLoop() {
	defer cc.player.Close()
	dec, err := audio.NewDecoder(audio.DefaultFrameDuration)
	if err != nil {
		return
	}
	playout := audio.NewPlayout(cc.jb, dec)
	pcm := make([]int16, dec.FrameSamples()*audio.Channels)
	step := int64(audio.DefaultFrameDuration)
	next := transport.Mono()
	for cc.ctx.Err() == nil {
		next += step
		transport.SleepUntilMono(next)
		if now := transport.Mono(); now-next > 5*step {
			next = now
		}
		if _, err := playout.Next(pcm); err != nil {
			continue
		}
		applyGain(pcm, math.Float64frombits(cc.gain.Load()))
		_ = cc.player.Write(pcm)
	}
}

// reportLoop tells the host what the client sees, once a second.
func (cc *conn) reportLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var loss shardLoss
	for {
		select {
		case <-cc.ctx.Done():
			return
		case <-t.C:
		}
		s := cc.stats()
		sess := cc.session()
		_ = sess.SendControl(&wire.StatsReport{
			RTTMicros:       uint32(s.RTTMs * 1000),
			LossPermille:    loss.permille(sess.Stats()),
			FramesReceived:  uint32(s.FramesReceived),
			FramesDropped:   uint32(s.FramesDropped),
			FramesRecovered: uint32(s.SlicesRecovered),
			DecodeMicros:    uint32(s.DecodeMs * 1000),
			ReceivedKbps:    uint32(s.Kbps),
		})
	}
}

// shardLoss is the pre-FEC shard loss between reports, the measure the
// host's FEC loss estimate keeps: the share of frames lost since the start
// stayed at 0.3% for minutes after the host's estimate was back to 0.
type shardLoss struct{ expected, received uint64 }

func (l *shardLoss) permille(ss transport.SessionStats) uint16 {
	if ss.ShardsExpected < l.expected || ss.ShardsReceived < l.received {
		// A new session after a route switch counts from zero.
		*l = shardLoss{}
	}
	exp, recv := ss.ShardsExpected-l.expected, ss.ShardsReceived-l.received
	l.expected, l.received = ss.ShardsExpected, ss.ShardsReceived
	if exp == 0 {
		return 0
	}
	return uint16((1000*(exp-min(recv, exp)) + exp - 1) / exp)
}

func (cc *conn) stats() ClientStats {
	rt := cc.live.Load()
	sess := rt.sess
	ss := sess.Stats()
	cc.mu.Lock()
	since := cc.since
	cc.mu.Unlock()
	ss.FramesComplete -= min(since.FramesComplete, ss.FramesComplete)
	ss.FramesLost -= min(since.FramesLost, ss.FramesLost)
	ss.SlicesRecovered -= min(since.SlicesRecovered, ss.SlicesRecovered)
	ss.FramesPartial -= min(since.FramesPartial, ss.FramesPartial)
	acc := sess.Config()
	s := ClientStats{
		Connected: true, HostName: cc.name, HostID: cc.hostID, Identity: sess.Identity(), Addr: rt.addr, Path: rt.kind.String(),
		DatagramBytes: sess.MaxDatagram(), MaxBitrateKbps: int(acc.BitrateKbps), RouteSwitches: cc.switches.Load(),
		Codec: cc.stream.Load().sink.Codec.String(), Width: int(acc.Width), Height: int(acc.Height),
		RTTMs:          ms(int64(sess.RTT())),
		FramesReceived: ss.FramesComplete, FramesLost: ss.FramesLost, SlicesRecovered: ss.SlicesRecovered,
		FramesDropped: cc.dropped.Load(), FramesPartial: ss.FramesPartial, Profile: cc.profile.Load(), SinkErrors: cc.sinkErr.Load(), AudioPackets: cc.audioPkts.Load(), InputSent: cc.inputs.Load(),
	}
	s.Chroma = "4:2:0"
	if acc.Chroma == wire.Chroma444 {
		s.Chroma = "4:4:4"
	}
	if total := ss.FramesComplete + ss.FramesLost; total > 0 {
		s.LossPct = 100 * float64(ss.FramesLost) / float64(total)
	}
	s.FPS, s.Kbps = cc.recv.per(transport.Mono())
	if cc.jb != nil {
		s.AudioBufferMs = ms(int64(cc.jb.Stats().Depth))
		s.AudioBufferMs += ms(int64(playerQueued(cc.player)))
	}
	// Video datagrams are what remains after audio and the clock probes:
	// each side pings four times a second once media is up.
	video := float64(ss.PacketsReceived) - float64(s.AudioPackets) - 2*float64(sess.Clock().Samples())
	cc.mu.Lock()
	if cc.dataShard > 0 && video > float64(cc.dataShard) {
		s.FECParityPct = 100 * (video - float64(cc.dataShard)) / float64(cc.dataShard)
	}
	s.EncodeMs, s.DecodeMs, s.PresentMs = cc.encodeMs, cc.decodeMs, cc.presentMs
	s.CaptureMs, s.HostEncodeMs = float64(cc.host.CaptureMicros)/1000, float64(cc.host.EncodeMicros)/1000
	s.E2E, s.Network = cc.e2e.summary(), cc.network.summary()
	s.Encode, s.Decode, s.Present = cc.encodeW.summary(), cc.decodeW.summary(), cc.presentW.summary()
	s.Glass, s.E2EGlass = cc.glassW.summary(), cc.e2eGlassW.summary()
	s.E2EMs, s.NetworkMs = cc.e2e.last(), cc.network.last()
	s.FrameTimesMs, s.Keyframes = cc.frameTimes.ordered(nil), cc.keyframes
	cc.mu.Unlock()
	if p := cc.policy.Load(); p != nil {
		s.HostPolicy = *p
	}
	s.HostDisplays = cc.displays
	return s
}

func (cc *conn) close() {
	cc.cancel()
	cc.mic.close()
	cc.wg.Wait()
	cc.session().Close()
	cc.end(transport.ErrClosed)
	if err := cc.trace.flush(); err != nil {
		log.Printf("planck: trace: %v", err)
	}
}

// Stats is a snapshot of the current session; Connected is false without
// one.
func (c *Client) Stats() ClientStats {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ClientStats{}
	}
	select {
	case <-cc.done:
		return ClientStats{}
	default:
	}
	s := cc.stats()
	s.Mic = cc.micStats()
	c.mu.Lock()
	if c.clip != nil {
		s.Clipboard = c.clip.stats()
	}
	c.mu.Unlock()
	return s
}

// Done is closed when the current stream ends, which a route change does
// not; nil without one.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.done
}

// Err is why the current stream ended.
func (c *Client) Err() error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return nil
	}
	select {
	case <-cc.done:
		return cc.err
	default:
		return nil
	}
}

var ErrNotConnected = errors.New("session: not connected")

// HostDeniedError is a connect the host refused, which errors.Is finds as
// transport.ErrAuthDenied, with the fingerprint (hex) of the certificate
// the host presented: the one to check on both screens before pairing.
type HostDeniedError struct {
	Fingerprint string
	Err         error
}

func (e *HostDeniedError) Error() string { return e.Err.Error() }
func (e *HostDeniedError) Unwrap() error { return e.Err }

// ErrNoAnswer is a connect to a host that answered on none of its routes.
var ErrNoAnswer = errors.New("the host did not answer on any route")

// SendInput sends one input event to the host. Safe from any goroutine.
func (c *Client) SendInput(ev input.Event) error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ErrNotConnected
	}
	in, err := toWire(ev)
	if err != nil {
		return err
	}
	in.Seq = cc.inputSeq.Add(1)
	in.Timestamp = transport.Mono()
	if err := cc.session().SendInput(in); err != nil {
		return err
	}
	cc.inputs.Add(1)
	return nil
}

// RequestKeyframe asks the host for an IDR frame.
func (c *Client) RequestKeyframe() error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ErrNotConnected
	}
	return cc.session().SendControl(&wire.PLI{})
}

// ErrNoDisplaySwitch is SwitchDisplay's answer from a host that cannot
// move a stream to another display.
var ErrNoDisplaySwitch = errors.New("session: this host cannot switch displays")

// HostDisplays lists the displays the host streaming now advertised, nil
// without a stream or from an older host.
func (c *Client) HostDisplays() []defaults.HostDisplay {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return nil
	}
	return cc.displays
}

// StreamedDisplay is the ID of the host display streaming now, "" on a
// virtual display or without a stream.
func (c *Client) StreamedDisplay() string {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ""
	}
	if p := cc.profile.Load(); p != nil && !p.VirtualDisplay {
		return p.CaptureDisplay
	}
	return ""
}

// SwitchDisplay asks the host to stream its display id, one of
// HostDisplays, in this session. The host answers with a new stream
// (switchStream), which shows from its first keyframe.
func (c *Client) SwitchDisplay(id string) error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ErrNotConnected
	}
	sess := cc.session()
	if sess.PeerFeatures()&wire.FeatureSwitchDisplay == 0 {
		return ErrNoDisplaySwitch
	}
	return sess.SendControl(&wire.SwitchDisplay{DisplayID: id})
}

// SetQuality changes the requested stream. Protocol v1 fixes the config at
// the handshake, so a live session reconnects with the new one.
func (c *Client) SetQuality(ctx context.Context, q Quality) error {
	c.mu.Lock()
	c.cfg.Quality = q
	cc, t := c.conn, c.target
	c.mu.Unlock()
	if cc == nil {
		return nil
	}
	return c.Connect(ctx, t)
}

// SetPreferLAN turns the move of a stream on a tailnet route to the LAN
// on or off, for the stream now and later ones.
func (c *Client) SetPreferLAN(on bool) {
	c.mu.Lock()
	c.cfg.Route.PreferLAN = on
	c.mu.Unlock()
}

func (c *Client) liveSession() *transport.Session {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return nil
	}
	return cc.session()
}

// startClipboard starts sharing the clipboard the first time cfg asks for
// it.
func (c *Client) startClipboard(cfg ClientConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clip != nil || !cfg.Clipboard.any() {
		return
	}
	c.clip = openClipSync(cfg.OpenClipboard, cfg.Clipboard, c.liveSession, func() bool {
		s := c.liveSession()
		return s != nil && s.PeerState().ClipboardRecv
	}, c.logf)
}

func (c *Client) onClipboard(clip *wire.Clip) {
	c.mu.Lock()
	s := c.clip
	c.mu.Unlock()
	if s != nil {
		s.incoming(clip)
	}
}

// sendPeerState tells a host that reads it what this client does with the
// clipboard.
func (c *Client) sendPeerState(sess *transport.Session) {
	if sess.PeerFeatures()&wire.FeaturePeerState == 0 {
		return
	}
	c.mu.Lock()
	cfg := c.cfg.Clipboard
	if c.clip != nil {
		cfg = c.clip.config()
	}
	c.mu.Unlock()
	_ = sess.SendControl(&wire.PeerState{ClipboardSend: cfg.Send, ClipboardRecv: cfg.Receive})
}

// SetClipboard changes what this client shares, at once.
func (c *Client) SetClipboard(cfg ClipboardConfig) {
	c.mu.Lock()
	c.cfg.Clipboard = cfg
	s := c.clip
	full := c.cfg
	c.mu.Unlock()
	if s != nil {
		s.setConfig(cfg)
	} else {
		c.startClipboard(full)
	}
	if sess := c.liveSession(); sess != nil {
		c.sendPeerState(sess)
	}
}

// SetVolume sets the stream's playback level, from 0 (silent) to 1 (as
// the host sent it), at once and for every stream after.
func (c *Client) SetVolume(v float64) {
	c.gain.Store(math.Float64bits(min(max(v, 0), 1)))
}

func applyGain(pcm []int16, g float64) {
	if g >= 1 {
		return
	}
	for i, v := range pcm {
		pcm[i] = int16(float64(v) * g)
	}
}

// SetMicMuted mutes or unmutes this client's microphone on the host. Each
// stream starts muted. Unmuting fails when the host has no virtual mic to
// play it into.
func (c *Client) SetMicMuted(muted bool) error {
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	if cc == nil {
		return ErrNotConnected
	}
	if !muted {
		if st := cc.micStats(); st.Device == "" {
			return errors.New(st.Hint)
		}
	}
	return cc.mic.setMuted(muted)
}

func (cc *conn) micStats() MicStats {
	sess := cc.session()
	st := MicStats{Muted: cc.mic.muted.Load(), Packets: cc.mic.pkts.Load()}
	if sess.PeerFeatures()&wire.FeatureMic == 0 {
		st.Hint = errNoHostMic.Error()
		return st
	}
	ps := sess.PeerState()
	st.Device, st.Hint = ps.MicDevice, ps.MicHint
	if st.Device == "" && st.Hint == "" {
		st.Hint = "Waiting for the host"
	}
	return st
}

// Close disconnects and stops sharing the clipboard.
func (c *Client) Close() {
	c.Disconnect()
	c.mu.Lock()
	s := c.clip
	c.clip = nil
	c.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Resize offers the host a w x h pixel window to stream at, on the
// display offered last. See ResizeOn.
func (c *Client) Resize(w, h int) error { return c.ResizeOn(w, h, defaults.ClientDisplay{}) }

// ResizeOn offers the host a w x h pixel window to stream at, on display
// d; a d without a size is the display offered last. It sends nothing
// unless the stream is on a virtual display at Resolution auto and the
// host takes Resize, nor when the size, made even and in bounds, and the
// display are the ones offered last. A host that does not read a
// Resize's display gets the size alone. The host may answer with a new
// stream in another codec (switchStream).
func (c *Client) ResizeOn(w, h int, d defaults.ClientDisplay) error {
	c.mu.Lock()
	cc, res := c.conn, c.cfg.Overrides.Resolution
	c.mu.Unlock()
	if cc == nil || (res != "" && res != defaults.Auto) {
		return nil
	}
	rt, p := cc.live.Load(), cc.profile.Load()
	if rt == nil || p == nil || !p.VirtualDisplay || rt.sess.PeerFeatures()&wire.FeatureResize == 0 {
		return nil
	}
	withDisplay := rt.sess.PeerFeatures()&wire.FeatureResizeDisplay != 0
	next := &viewOffer{}
	next.w, next.h = defaults.ViewSize(w, h)
	for {
		last := cc.asked.Load()
		switch {
		case withDisplay && d.WidthPx > 0 && d.HeightPx > 0:
			next.display = d
			next.display.ViewWidthPx, next.display.ViewHeightPx = 0, 0
		case last != nil:
			next.display = last.display
		}
		if last != nil && *last == *next {
			return nil
		}
		if cc.asked.CompareAndSwap(last, next) {
			break
		}
	}
	m := &wire.Resize{Width: uint16(next.w), Height: uint16(next.h)}
	if withDisplay && next.display.WidthPx > 0 {
		m.Display = resizeDisplay(next.display)
	}
	return rt.sess.SendControl(m)
}

// viewOffer is a window size in pixels, even and in bounds, and the
// display it is on, without the window's size.
type viewOffer struct {
	w, h    int
	display defaults.ClientDisplay
}

// offerOf is the window a client display offered: its view size when the
// client has a window, and the display.
func offerOf(d defaults.ClientDisplay, window bool) *viewOffer {
	o := &viewOffer{}
	if window {
		o.w, o.h = defaults.ViewSize(d.ViewWidthPx, d.ViewHeightPx)
	}
	d.ViewWidthPx, d.ViewHeightPx = 0, 0
	o.display = d
	return o
}

func packView(w, h int) uint64 { return uint64(uint32(w))<<32 | uint64(uint32(h)) }

// streamUpdate takes a Profile or Stream the host sent on rt, holding it
// until rt is live.
func (cc *conn) streamUpdate(rt *route, m wire.Message, logf func(string, ...any)) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.isLive {
		rt.pending = append(rt.pending, m)
		return
	}
	cc.applyUpdate(rt, m, logf)
}

func (cc *conn) applyUpdate(rt *route, m wire.Message, logf func(string, ...any)) {
	switch m := m.(type) {
	case *wire.Profile:
		cc.resized(m.JSON, logf)
	case *wire.Stream:
		cc.switchStream(rt, m, logf)
	}
}

// goLive makes rt cc's route, then applies what the host sent on it before.
// It returns the route rt replaces.
func (cc *conn) goLive(rt *route, logf func(string, ...any)) *route {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	old := cc.live.Swap(rt)
	rt.isLive = true
	for _, m := range rt.pending {
		cc.applyUpdate(rt, m, logf)
	}
	rt.pending = nil
	return old
}

// resized takes a Profile the host sent after a Resize. The decoder
// rebuilds for the new size on the next keyframe, as at the start.
func (cc *conn) resized(js []byte, logf func(string, ...any)) {
	p, err := ParseProfile(js)
	if err != nil || p == nil {
		logf("planck: resized profile: %v", err)
		return
	}
	cc.profile.Store(p)
	cc.decoding.Store(false)
	logf("planck: the host streams at %dx%d", p.Width, p.Height)
}

// switchStream takes a stream the host settled after a Resize, on rt: the
// session carries it from now on, and the decode loop moves to a sink for
// it at its first frame, a keyframe.
func (cc *conn) switchStream(rt *route, m *wire.Stream, logf func(string, ...any)) {
	if cc.live.Load() != rt {
		return
	}
	p, err := ParseProfile(m.Profile)
	if err != nil || p == nil {
		logf("planck: stream %d: %v", m.Epoch, err)
		return
	}
	acc := profileVideoConfig(p)
	old := rt.sess.Config()
	acc.AudioSampleRate, acc.AudioChannels = old.AudioSampleRate, old.AudioChannels
	rt.sess.SetStream(acc, 0, 0, transport.ParityProfile{})
	dcfg := decode.Config{Codec: encode.Codec(p.Codec), Width: p.Width, Height: p.Height, FPS: p.FPS, Chroma: streamChroma(p), HDR10: p.HDR10}
	if dcfg.Codec == encode.PyroWave {
		dcfg.ChunkLen = rt.sess.ShardSize()
	}
	cc.profile.Store(p)
	cc.intra.Store(dcfg.Codec == encode.PyroWave)
	cc.needKey.Store(true)
	cc.decoding.Store(false)
	cc.stream.Store(&streamState{epoch: m.Epoch, sink: dcfg})
	cc.mu.Lock()
	cc.since, cc.keyframes = rt.sess.Stats(), 0
	cc.mu.Unlock()
	cc.dropped.Store(0)
	logf("planck: the host streams %v at %dx%d@%d (stream %d)", p.Codec, p.Width, p.Height, p.FPS, m.Epoch)
}

// Disconnect says goodbye to the host and stops playback.
func (c *Client) Disconnect() {
	c.mu.Lock()
	cc := c.conn
	c.conn = nil
	c.mu.Unlock()
	if cc != nil {
		cc.close()
	}
}
