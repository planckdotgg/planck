package session

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/internal/rtprio"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/decode"
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
	// PIN is the host's pairing PIN, needed the first time unless both
	// devices belong to the same Tailscale user.
	PIN     string
	NoAudio bool

	// OpenSink makes the video sink; nil decodes headless (OpenDecodeSink).
	OpenSink SinkOpener
	// OpenAudioPlayer replaces the default output device.
	OpenAudioPlayer func() (audio.Player, error)
	// Tailscale replaces the local daemon client for path detection.
	Tailscale PathInfoer
	// OnRumble receives gamepad rumble from the host.
	OnRumble         func(input.RumbleEvent)
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
	// Probe replaces discovery.Probe.
	Probe func(ctx context.Context, addr netip.AddrPort) (discovery.Beacon, time.Duration, error)
}

const (
	DefaultProbeInterval = 4 * time.Second
	DefaultStallTimeout  = 2500 * time.Millisecond
	lanUpgradeAfter      = 2
	// raceBeforeAddr is how long the candidates may race before a target's
	// address is dialed instead.
	raceBeforeAddr = time.Second
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
	Width     int
	Height    int
	FPS       float64
	Kbps      float64
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

	// FECParityPct is the parity share of the video datagrams received,
	// estimated from datagram counts.
	FECParityPct    float64
	LossPct         float64
	FramesReceived  uint64
	FramesLost      uint64
	FramesDropped   uint64
	SlicesRecovered uint64
	SinkErrors      uint64
	AudioBufferMs   float64
	AudioPackets    uint64
	InputSent       uint64
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
	codec  encode.Codec
	// video is the accepted config the sink was opened for; a new route
	// must accept the same.
	video    wire.SessionConfig
	switches atomic.Uint64

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
	recv      rate
	dataShard uint64

	lastPLI                             atomic.Int64
	dropped, sinkErr, audioPkts, inputs atomic.Uint64
	// decoding is set once the sink gave its first picture; until then the
	// queue may grow to warmupQueue. needKey discards frames until the next
	// keyframe, after one was lost.
	decoding, needKey atomic.Bool

	trace *tracer[clientTraceRow]
}

// capturedAt maps a frame, by the PTS its picture carries, to the host's
// capture time on the client's clock, for pictures that reach the screen
// after the decode loop moved on.
// route is one transport session of a stream and the path it runs on.
type route struct {
	sess *transport.Session
	kind discovery.PathKind
	addr string
	// bye is set when the host said goodbye: it ended the stream, or gave
	// it to another client, and the stream must not come back.
	bye atomic.Bool
}

func (cc *conn) session() *transport.Session { return cc.live.Load().sess }

// end stops the stream for good; the first reason sticks.
func (cc *conn) end(err error) {
	cc.endOnce.Do(func() {
		cc.err = err
		close(cc.done)
	})
}

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
		glassW: newWindow(latencyWindow), e2eGlassW: newWindow(latencyWindow), done: make(chan struct{})}
	for range warmupQueue + 2 {
		cc.free <- &frameBuf{}
	}
	cc.needKey.Store(true)
	cc.trace = newTracer[clientTraceRow](traceClientEnv)
	if !cfg.NoAudio {
		cc.jb = audio.NewJitterBuffer(audio.JitterConfig{})
	}
	rt, err := c.dialRoute(ctx, cc, r)
	if err != nil {
		return nil, err
	}
	sess := rt.sess
	cc.live.Store(rt)
	if cc.name == "" {
		cc.name = sess.PeerName()
	}
	acc := sess.Config()
	cc.video = acc
	cc.codec = codecFromWire(acc.Codec)
	open := cfg.OpenSink
	if open == nil {
		open = OpenDecodeSink
	}
	sink, err := open(decode.Config{Codec: cc.codec, Width: int(acc.Width), Height: int(acc.Height), FPS: int(acc.FPS)})
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("session: video sink: %w", err)
	}
	if o, ok := sink.(interface{ SetObserver(player.Observer) }); ok {
		o.SetObserver(viewObserver{cc})
	}
	cc.ctx, cc.cancel = context.WithCancel(context.Background())
	cc.wg.Go(func() { cc.decodeLoop(sink, c.logf) })
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

// dialRoute opens a session to the host on r for cc, checking the host's
// certificate against its pin, and pins the host on first approval. It
// records the route's address on the pin.
func (c *Client) dialRoute(ctx context.Context, cc *conn, r resolved) (*route, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
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

	rt := &route{kind: r.kind, addr: r.addr}
	sess, err := transport.Dial(ctx, r.addr, transport.ClientConfig{
		Certificate: cert, Name: cfg.Name, VerifyHost: verify, Auth: method, PIN: cfg.PIN, Session: req,
		HandshakeTimeout: cfg.HandshakeTimeout, SimulateLoss: cfg.SimulateLoss, LANPath: r.kind == discovery.PathLANDirect,
		Handlers: transport.Handlers{
			VideoFrame: cc.onFrame,
			Audio:      cc.onAudio,
			Control: func(m wire.Message) {
				if _, ok := m.(*wire.Goodbye); ok {
					rt.bye.Store(true)
				}
			},
			Rumble: func(r wire.Rumble) {
				if cfg.OnRumble != nil {
					cfg.OnRumble(input.RumbleEvent{Index: r.Pad, LowFreq: r.Low, HighFreq: r.High, DurationMs: r.DurationMs})
				}
			},
		},
	})
	if err != nil {
		return nil, err
	}
	rt.sess = sess
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
}

// resolve picks the control address and path: the race winner among the
// candidates, or the explicit address classified with Tailscale's view.
func (c *Client) resolve(ctx context.Context, t Target) (resolved, error) {
	r := resolved{addr: t.Addr, hostID: t.HostID, fingerprint: t.Fingerprint, kind: discovery.PathLANDirect}
	if len(t.Candidates) > 0 {
		rctx := ctx
		if t.Addr != "" {
			// A host with discovery off never answers; its address does.
			var cancel context.CancelFunc
			rctx, cancel = context.WithTimeout(ctx, raceBeforeAddr)
			defer cancel()
		}
		res, err := discovery.Racer{Probe: c.cfg.Route.Probe}.Race(rctx, t.Candidates)
		if err == nil {
			return r.from(res), nil
		}
		if t.Addr == "" || ctx.Err() != nil {
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

// from is r on the path a race won.
func (r resolved) from(res discovery.Result) resolved {
	if r.fingerprint == "" {
		r.fingerprint = res.Beacon.CertFingerprint
	}
	r.addr, r.hostID, r.name, r.kind = res.ControlAddr.String(), res.Beacon.HostID, res.Beacon.Name, res.Kind
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
	probe := rc.Probe
	if probe == nil {
		probe = discovery.Probe
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
		res, err := c.probeLAN(cc.ctx, lan, probe, rc.ProbeInterval)
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
func (c *Client) probeLAN(ctx context.Context, lan []discovery.Candidate, probe func(context.Context, netip.AddrPort) (discovery.Beacon, time.Duration, error), every time.Duration) (discovery.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, min(time.Second, every))
	defer cancel()
	return discovery.Racer{Probe: probe}.Race(ctx, lan)
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
	acc := rt.sess.Config()
	if acc.Codec != cc.video.Codec || acc.Width != cc.video.Width || acc.Height != cc.video.Height {
		rt.sess.Close()
		return fmt.Errorf("session: the host changed the stream to %dx%d codec %d on %s", acc.Width, acc.Height, acc.Codec, r.kind)
	}
	if cc.jb != nil {
		cc.jb.Restart()
	}
	old := cc.live.Swap(rt)
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
	if cc.needKey.Load() {
		if !info.Keyframe {
			cc.drop()
			return
		}
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
		cc.drop()
		return
	}
	if len(cc.frames) >= limit {
		cc.free <- fb
		cc.drop()
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
		cc.drop()
	}
}

func (cc *conn) drop() {
	cc.dropped.Add(1)
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
func (cc *conn) decodeLoop(sink Sink, logf func(string, ...any)) {
	defer sink.Close()
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
			cc.needKey.Store(true)
			cc.requestKey()
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
		_ = cc.player.Write(pcm)
	}
}

// reportLoop tells the host what the client sees, once a second.
func (cc *conn) reportLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-cc.ctx.Done():
			return
		case <-t.C:
		}
		s := cc.stats()
		_ = cc.session().SendControl(&wire.StatsReport{
			RTTMicros:       uint32(s.RTTMs * 1000),
			LossPermille:    uint16(s.LossPct * 10),
			FramesReceived:  uint32(s.FramesReceived),
			FramesDropped:   uint32(s.FramesDropped),
			FramesRecovered: uint32(s.SlicesRecovered),
			DecodeMicros:    uint32(s.DecodeMs * 1000),
			ReceivedKbps:    uint32(s.Kbps),
		})
	}
}

func (cc *conn) stats() ClientStats {
	rt := cc.live.Load()
	sess := rt.sess
	ss := sess.Stats()
	acc := sess.Config()
	s := ClientStats{
		Connected: true, HostName: cc.name, HostID: cc.hostID, Identity: sess.Identity(), Addr: rt.addr, Path: rt.kind.String(),
		DatagramBytes: sess.MaxDatagram(), MaxBitrateKbps: int(acc.BitrateKbps), RouteSwitches: cc.switches.Load(),
		Codec: cc.codec.String(), Width: int(acc.Width), Height: int(acc.Height),
		RTTMs:          ms(int64(sess.RTT())),
		FramesReceived: ss.FramesComplete, FramesLost: ss.FramesLost, SlicesRecovered: ss.SlicesRecovered,
		FramesDropped: cc.dropped.Load(), SinkErrors: cc.sinkErr.Load(), AudioPackets: cc.audioPkts.Load(), InputSent: cc.inputs.Load(),
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
	s.E2E, s.Network = cc.e2e.summary(), cc.network.summary()
	s.Encode, s.Decode, s.Present = cc.encodeW.summary(), cc.decodeW.summary(), cc.presentW.summary()
	s.Glass, s.E2EGlass = cc.glassW.summary(), cc.e2eGlassW.summary()
	s.E2EMs, s.NetworkMs = cc.e2e.last(), cc.network.last()
	cc.mu.Unlock()
	return s
}

func (cc *conn) close() {
	cc.cancel()
	cc.session().Close()
	cc.wg.Wait()
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
	return cc.stats()
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
