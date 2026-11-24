package session

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/internal/rtprio"
	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/config"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/input"
	"github.com/planckdotgg/planck/pkg/linkinfo"
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
	Logf             func(format string, args ...any)
}

// Target says where to connect: discovery candidates, raced LAN direct
// first, then Tailscale direct, then DERP; or an explicit host:port.
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
	FECParityPct   float64
	LossPct        float64
	FramesReceived uint64
	FramesLost     uint64
	FramesDropped  uint64
	// FramesPartial counts PyroWave frames shown with chunks missing.
	FramesPartial   uint64
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
	c := &Client{cfg: cfg, dir: dir, logf: cfg.Logf}
	if c.logf == nil {
		c.logf = log.Printf
	}
	if c.cfg.Tailscale == nil {
		c.cfg.Tailscale = tailscale.New()
	}
	return c, nil
}

// conn is one live session and its goroutines.
type conn struct {
	sess   *transport.Session
	live   atomic.Pointer[transport.Session]
	hostID string
	name   string
	path   string
	addr   string
	codec  encode.Codec
	// intra marks a PyroWave stream: frames decode alone, so loss never
	// waits for a keyframe.
	intra   atomic.Bool
	profile *defaults.ResolvedProfile

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

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
	addr, hostID, path := r.addr, r.hostID, r.kind
	t.Fingerprint = r.fingerprint
	stored, err := config.LoadClientConfig(c.dir)
	if err != nil {
		return nil, err
	}
	cert, err := config.LoadOrCreateIdentity(filepath.Join(c.dir, "client-cert.pem"), filepath.Join(c.dir, "client-key.pem"), "planck-client "+cfg.Name)
	if err != nil {
		return nil, err
	}
	pinKey := hostID
	if pinKey == "" {
		pinKey = "addr:" + addr
	}
	pinned, isPinned := stored.PinnedHosts[pinKey]
	var seen [32]byte
	verify := func(fp [32]byte) error {
		seen = fp
		want := t.Fingerprint
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
	if ceiling := path.RecommendedMaxBitrateKbps(); req.BitrateKbps == 0 || int(req.BitrateKbps) > ceiling {
		req.BitrateKbps = uint32(ceiling)
	}
	if !cfg.NoAudio {
		req.AudioSampleRate, req.AudioChannels = audio.SampleRate, audio.Channels
	}
	capsJSON, err := c.capabilities(cfg, addr, path)
	if err != nil {
		return nil, err
	}

	cc := &conn{hostID: hostID, name: r.name, path: path.String(), addr: addr, frames: make(chan *frameBuf, warmupQueue),
		free: make(chan *frameBuf, warmupQueue+2), e2e: newWindow(latencyWindow), network: newWindow(latencyWindow),
		encodeW: newWindow(latencyWindow), decodeW: newWindow(latencyWindow), presentW: newWindow(latencyWindow),
		glassW: newWindow(latencyWindow), e2eGlassW: newWindow(latencyWindow)}
	for range warmupQueue + 2 {
		cc.free <- &frameBuf{}
	}
	cc.needKey.Store(true)
	cc.trace = newTracer[clientTraceRow](traceClientEnv)
	if !cfg.NoAudio {
		cc.jb = audio.NewJitterBuffer(audio.JitterConfig{})
	}
	sess, err := transport.Dial(ctx, addr, transport.ClientConfig{
		Certificate: cert, Name: cfg.Name, VerifyHost: verify, Auth: method, PIN: cfg.PIN, Session: req,
		HandshakeTimeout: cfg.HandshakeTimeout, SimulateLoss: cfg.SimulateLoss, LANPath: path == discovery.PathLANDirect,
		Capabilities: capsJSON,
		Handlers: transport.Handlers{
			VideoFrame: cc.onFrame,
			Audio:      cc.onAudio,
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
	cc.sess = sess
	cc.live.Store(sess)
	if cfg.PIN != "" {
		// A PIN pairs once; the host rotates it, and reconnects use the pin.
		c.mu.Lock()
		c.cfg.PIN = ""
		c.mu.Unlock()
	}
	if !isPinned || !strings.EqualFold(pinned.CertFingerprint, hex.EncodeToString(seen[:])) {
		if err := c.pin(stored, pinKey, addr, seen); err != nil {
			sess.Close()
			return nil, err
		}
	}
	acc := sess.Config()
	cc.codec = codecFromWire(acc.Codec)
	cc.intra.Store(cc.codec == encode.PyroWave)
	if cc.profile, err = ParseProfile(sess.Profile()); err != nil {
		c.logf("planck: %v", err)
	}
	dcfg := decode.Config{Codec: cc.codec, Width: int(acc.Width), Height: int(acc.Height), FPS: int(acc.FPS)}
	if cc.profile != nil {
		dcfg.Chroma = streamChroma(cc.profile)
		for _, w := range cc.profile.Warnings {
			c.logf("planck: profile: %s", w)
		}
	}
	if cc.intra.Load() {
		dcfg.ChunkLen = sess.ShardSize()
	}
	open := cfg.OpenSink
	if open == nil {
		open = OpenDecodeSink
	}
	sink, err := open(dcfg)
	if err != nil {
		sess.Close()
		return nil, fmt.Errorf("session: video sink: %w", err)
	}
	if ps, ok := sink.(interface {
		SetProfile(*defaults.ResolvedProfile)
	}); ok && cc.profile != nil {
		ps.SetProfile(cc.profile)
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

type resolved struct {
	addr, hostID, name, fingerprint string
	kind                            discovery.PathKind
}

// resolve picks the control address and path: the race winner among the
// candidates, or the explicit address classified with Tailscale's view.
func (c *Client) resolve(ctx context.Context, t Target) (resolved, error) {
	r := resolved{addr: t.Addr, hostID: t.HostID, fingerprint: t.Fingerprint, kind: discovery.PathLANDirect}
	if len(t.Candidates) > 0 {
		res, err := discovery.Racer{}.Race(ctx, t.Candidates)
		if err != nil {
			return r, err
		}
		if r.fingerprint == "" {
			r.fingerprint = res.Beacon.CertFingerprint
		}
		r.addr, r.hostID, r.name, r.kind = res.ControlAddr.String(), res.Beacon.HostID, res.Beacon.Name, res.Kind
		return r, nil
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

// capabilities is the JSON this client offers a host: its display,
// decoders and link, the route it found, and the user's overrides.
func (c *Client) capabilities(cfg ClientConfig, addr string, path discovery.PathKind) ([]byte, error) {
	var caps defaults.ClientCapabilities
	if cfg.Capabilities != nil {
		caps = cfg.Capabilities()
	} else {
		caps = defaults.ClientCapabilities{OS: runtime.GOOS, Display: clientDisplay(), Decoders: clientDecoders(),
			Present10Bit: clientPresent10Bit, DirectDisplay: clientDirectDisplay}
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		if name, link, err := linkinfo.Route(ap.Addr().Unmap()); err == nil {
			caps.Link = link
			c.logf("planck: route to %v via %s: wired %v, %d Mbps", ap.Addr(), name, link.Wired, link.SpeedMbps)
		}
	}
	if err := cfg.Overrides.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(clientCaps{V: clientCapsVersion, Client: caps, Route: defaults.RouteInfo{Kind: routeKind(path)}, Overrides: cfg.Overrides})
}

func (c *Client) pin(stored config.ClientConfig, key, addr string, fp [32]byte) error {
	if err := stored.Pin(config.PinnedHost{HostID: key, CertFingerprint: hex.EncodeToString(fp[:]), PairedAt: time.Now(), LastAddr: addr}); err != nil {
		return err
	}
	return config.SaveClientConfig(c.dir, stored)
}

// onFrame runs on the receive goroutine: it copies the frame out and never
// blocks. A full queue drops the frame and asks for a keyframe, and the
// frames up to it are dropped too: they would reference the lost one, and a
// decoder fed them fails on each.
func (cc *conn) onFrame(info wire.VideoInfo, frame []byte) {
	now := transport.Mono()
	sess := cc.live.Load()
	if sess == nil {
		return
	}
	if cc.needKey.Load() && !cc.intra.Load() {
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
	if cc.intra.Load() {
		return
	}
	cc.needKey.Store(true)
	cc.requestKey()
}

// requestKey sends at most one PLI per minPLIGap.
func (cc *conn) requestKey() {
	sess := cc.live.Load()
	now := transport.Mono()
	last := cc.lastPLI.Load()
	if sess == nil || now-last < int64(minPLIGap) || !cc.lastPLI.CompareAndSwap(last, now) {
		return
	}
	_ = sess.SendControl(&wire.PLI{})
}

// decodeLoop runs on a thread of its own at a raised class, which ends
// with it.
func (cc *conn) decodeLoop(sink Sink, logf func(string, ...any)) {
	defer sink.Close()
	if err := rtprio.Boost(); err != nil {
		logf("planck: decode thread priority: %v", err)
	}
	clock := cc.sess.Clock()
	for {
		var fb *frameBuf
		select {
		case <-cc.ctx.Done():
			return
		case fb = <-cc.frames:
		}
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
		_ = cc.sess.SendControl(&wire.StatsReport{
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
	ss := cc.sess.Stats()
	acc := cc.sess.Config()
	s := ClientStats{
		Connected: true, HostName: cc.name, HostID: cc.hostID, Identity: cc.sess.Identity(), Addr: cc.addr, Path: cc.path,
		Codec: cc.codec.String(), Width: int(acc.Width), Height: int(acc.Height),
		RTTMs:          ms(int64(cc.sess.RTT())),
		FramesReceived: ss.FramesComplete, FramesLost: ss.FramesLost, SlicesRecovered: ss.SlicesRecovered,
		FramesDropped: cc.dropped.Load(), FramesPartial: ss.FramesPartial, Profile: cc.profile, SinkErrors: cc.sinkErr.Load(), AudioPackets: cc.audioPkts.Load(), InputSent: cc.inputs.Load(),
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
	video := float64(ss.PacketsReceived) - float64(s.AudioPackets) - 2*float64(cc.sess.Clock().Samples())
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
	cc.sess.Close()
	cc.cancel()
	cc.wg.Wait()
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
	case <-cc.sess.Done():
		return ClientStats{}
	default:
	}
	return cc.stats()
}

// Done is closed when the current session ends; nil without one.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.sess.Done()
}

// Err is why the current session ended.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	return c.conn.sess.Err()
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
	if err := cc.sess.SendInput(in); err != nil {
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
	return cc.sess.SendControl(&wire.PLI{})
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
