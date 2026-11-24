package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/wire"
	"github.com/quic-go/quic-go"
)

var (
	ErrWrongRole      = errors.New("transport: not allowed in this session role")
	ErrNoPeer         = errors.New("transport: media peer address not known yet")
	ErrClosed         = errors.New("transport: session closed")
	ErrTooBig         = errors.New("transport: payload does not fit one media datagram")
	ErrControlBacklog = errors.New("transport: control send queue full")
	// ErrPeerUnsupported: the peer's Hello did not offer the feature.
	ErrPeerUnsupported = errors.New("transport: peer does not support this")
)

const (
	defaultPingInterval = 250 * time.Millisecond
	probeInterval       = 20 * time.Millisecond
	minPLIInterval      = 100 * time.Millisecond
	sentRingSize        = 256
	udpBufferBytes      = 8 << 20
	// redundantInputFor is how long after the client last saw a media
	// datagram go missing it sends each input event twice.
	redundantInputFor = 2 * time.Second
	// redundantInputAge bounds how old an event may be to go out again.
	redundantInputAge = 100 * time.Millisecond
)

// VideoSlice is one rebuilt slice. Data is valid only during the callback.
type VideoSlice struct {
	Info      wire.VideoInfo
	Index     int
	Data      []byte
	Recovered bool
}

// Handlers receive media and control messages. Media handlers run on the
// session's receive goroutine and Control on its control goroutine; byte
// slices are valid only during the call. Handlers must not call Close
// synchronously. Nil handlers are skipped.
type Handlers struct {
	VideoSlice func(VideoSlice)
	VideoFrame func(info wire.VideoInfo, frame []byte)
	Audio      func(h wire.AudioHeader, opus []byte)
	// Mic receives the client's microphone (host only). Setting it offers
	// wire.FeatureMic.
	Mic     func(h wire.AudioHeader, payload []byte)
	Input   func(in wire.Input)
	Rumble  func(r wire.Rumble)
	Control func(m wire.Message)
	// Clipboard receives the peer's clipboard items on their own goroutine;
	// the item is the handler's to keep. Setting it offers
	// wire.FeatureClipboard.
	Clipboard func(c *wire.Clip)
}

// features are the Hello bits a side with these handlers offers.
func (h Handlers) features() uint32 {
	f := wire.FeaturePeerState
	if h.Clipboard != nil {
		f |= wire.FeatureClipboard
	}
	return f
}

// SessionStats are cumulative counters.
type SessionStats struct {
	PacketsSent      uint64
	PacketsReceived  uint64
	AuthFailures     uint64
	SimulatedDrops   uint64
	FramesComplete   uint64
	FramesLost       uint64
	SlicesRecovered  uint64
	PLIsSent         uint64
	PLIsReceived     uint64
	FeedbackReceived uint64
	// InputResent counts the client's redundant input copies, and
	// InputDuplicates the copies the host dropped.
	InputResent     uint64
	InputDuplicates uint64
	// FramesPartial counts intra frames handed on with chunks missing.
	FramesPartial uint64
	// ClipboardSent and ClipboardReceived count clipboard items;
	// ClipboardRefused counts received items dropped for their size, hash
	// or format.
	ClipboardSent     uint64
	ClipboardReceived uint64
	ClipboardRefused  uint64
}

type sentFrame struct {
	id    uint32
	at    int64
	bytes int
	valid bool
}

// Session is one authenticated host-client stream: a QUIC control stream
// plus an AEAD-sealed UDP media socket. The host sends video, audio and
// rumble and receives input and feedback; the client the reverse.
type Session struct {
	isHost    bool
	conn      *quic.Conn
	ctrl      *quic.Stream
	ctrlOut   chan wire.Message
	id        uint64
	cfg       atomic.Pointer[wire.SessionConfig]
	shardSize int
	maxDgram  int
	identity  string
	peerFP    [32]byte
	h         Handlers
	// peerFeatures are the wire.Feature bits of the peer's Hello.
	peerFeatures uint32
	peerName     string
	peerCaps     []byte
	profile      []byte
	// intra marks a PyroWave stream: every frame decodes alone, so a lost
	// chunk is delivered as zeros instead of costing a keyframe.
	intra atomic.Bool

	udp      *net.UDPConn
	sealMu   sync.Mutex
	sealer   *Sealer
	peer     netip.AddrPort
	havePeer bool
	impair   *Impairer
	txBuf    []byte
	pingSeq  uint32
	// lastInput is the client's previous input event and when it went out,
	// sent again ahead of the next one while loss is recent.
	lastInput   wire.Input
	lastInputAt int64
	// lossAt is when the client last saw a frame miss a datagram.
	lossAt atomic.Int64

	opener   *Opener
	rxBuf    []byte
	lastFrom netip.AddrPort
	inputs   seqWindow

	videoMu   sync.Mutex
	enc       *fec.Encoder
	emitVideo func([]byte) error
	policy    *fec.Policy
	pol       policies

	ringMu sync.Mutex
	sent   [sentRingSize]sentFrame

	rcMu     sync.Mutex
	rc       *ratectl.Controller
	target   atomic.Int64
	keyframe atomic.Bool

	dec       *fec.Decoder
	frameBuf  []byte
	finRecv   int
	finSent   int
	finLost   bool
	finID     uint32
	hasFin    bool
	lastPLI   int64
	sentPLI   bool
	pendingFB bool

	pendingInfo   wire.VideoInfo
	hasPending    bool
	lastDelivered uint32
	delivered     bool

	firstSeen [sentRingSize]struct {
		id uint32
		at int64
	}

	clock        ClockEstimator
	pingInterval time.Duration

	stats struct {
		sent, recv, authFail, simDrop, complete, lost, recovered, pliSent, pliRecv, feedback, inputResent, inputDup, partial atomic.Uint64
		clipSent, clipRecv, clipRefused                                                                                      atomic.Uint64
	}

	clipMu  sync.Mutex
	clipOut *quic.SendStream

	peerState atomic.Pointer[wire.PeerState]

	mediaUp     chan struct{}
	mediaUpOnce sync.Once
	done        chan struct{}
	closeOnce   sync.Once
	errMu       sync.Mutex
	err         error
	wg          sync.WaitGroup
}

type sessionParams struct {
	isHost       bool
	conn         *quic.Conn
	ctrl         *quic.Stream
	ready        wire.SessionReady
	maxDgram     int
	identity     string
	peerFP       [32]byte
	handlers     Handlers
	udp          *net.UDPConn
	peer         netip.AddrPort
	pingInterval time.Duration
	simLoss      float64
	rate         ratectl.Config
	path         ratectl.Path
	fecPolicy    *fec.Policy
	parity       ParityProfile
	peerFeatures uint32
	peerName     string
	peerCaps     []byte
	profile      []byte
}

func newSession(p sessionParams) (*Session, error) {
	tlsState := p.conn.ConnectionState().TLS
	keys, err := deriveMediaKeysV1(tlsState.ExportKeyingMaterial)
	if err != nil {
		return nil, err
	}
	sendKey, recvKey := keys.ClientToServer, keys.ServerToClient
	if p.isHost {
		sendKey, recvKey = recvKey, sendKey
	}
	sealer, err := NewSealer(sendKey)
	if err != nil {
		return nil, err
	}
	opener, err := NewOpener(recvKey)
	if err != nil {
		return nil, err
	}
	if p.pingInterval <= 0 {
		p.pingInterval = defaultPingInterval
	}
	s := &Session{
		isHost:       p.isHost,
		conn:         p.conn,
		ctrl:         p.ctrl,
		ctrlOut:      make(chan wire.Message, 64),
		id:           p.ready.SessionID,
		shardSize:    int(p.ready.ShardSize),
		maxDgram:     p.maxDgram,
		identity:     p.identity,
		peerFP:       p.peerFP,
		h:            p.handlers,
		peerFeatures: p.peerFeatures,
		peerName:     p.peerName,
		peerCaps:     p.peerCaps,
		profile:      p.profile,
		udp:          p.udp,
		sealer:       sealer,
		opener:       opener,
		peer:         p.peer,
		havePeer:     p.peer.IsValid(),
		txBuf:        make([]byte, p.maxDgram),
		rxBuf:        make([]byte, 64<<10),
		pingInterval: p.pingInterval,
		mediaUp:      make(chan struct{}),
		done:         make(chan struct{}),
	}
	_ = s.udp.SetReadBuffer(udpBufferBytes)
	_ = s.udp.SetWriteBuffer(udpBufferBytes)
	if p.simLoss > 0 {
		s.impair = NewImpairer(Impairment{Loss: p.simLoss, Seed: p.ready.SessionID | 1}, p.maxDgram, s.writeLocked)
	}
	s.cfg.Store(&p.ready.Config)
	s.intra.Store(p.ready.Config.Codec == wire.CodecPyroWave)
	if s.isHost {
		s.enc = fec.NewEncoder(s.shardSize, AEADHeaderLen, AEADTagLen)
		s.emitVideo = s.sendSealed
		s.policy = p.fecPolicy
		if s.policy == nil {
			s.policy = fec.NewPolicy()
		}
		s.pol = newPolicies(s.policy, p.parity)
		s.rc = ratectl.New(p.rate)
		s.rc.SetPath(p.path)
		s.target.Store(int64(s.rc.TargetKbps()))
	} else {
		s.dec = fec.NewDecoder(s.shardSize, s.onFrameFinal)
	}
	return s, nil
}

func (s *Session) start() {
	if s.h.Clipboard != nil {
		s.wg.Go(s.clipAcceptLoop)
	}
	s.wg.Add(5)
	go s.recvLoop()
	go s.pingLoop()
	go s.ctrlReadLoop()
	go s.ctrlWriteLoop()
	go func() {
		defer s.wg.Done()
		select {
		case <-s.conn.Context().Done():
			s.shutdown(fmt.Errorf("transport: control connection closed: %w", context.Cause(s.conn.Context())))
		case <-s.done:
		}
	}()
}

// waitMedia blocks until the first authenticated media packet arrives.
func (s *Session) waitMedia(timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-s.mediaUp:
		return nil
	case <-s.done:
		return s.Err()
	case <-t.C:
		return errors.New("transport: media path did not come up")
	}
}

func (s *Session) ID() uint64                  { return s.id }
func (s *Session) Config() wire.SessionConfig  { return *s.cfg.Load() }
func (s *Session) ShardSize() int              { return s.shardSize }
func (s *Session) MaxDatagram() int            { return s.maxDgram }
func (s *Session) PeerName() string            { return s.peerName }
func (s *Session) PeerFeatures() uint32        { return s.peerFeatures }
func (s *Session) Identity() string            { return s.identity }
func (s *Session) PeerFingerprint() [32]byte   { return s.peerFP }
func (s *Session) Clock() *ClockEstimator      { return &s.clock }
func (s *Session) RTT() time.Duration          { return s.clock.RTT() }
func (s *Session) Done() <-chan struct{}       { return s.done }
func (s *Session) RemoteControlAddr() net.Addr { return s.conn.RemoteAddr() }

// PeerState is the last wire.PeerState the peer sent, or the zero value.
func (s *Session) PeerState() wire.PeerState {
	if ps := s.peerState.Load(); ps != nil {
		return *ps
	}
	return wire.PeerState{}
}

// TargetBitrateKbps is the rate controller's current target (host only).
func (s *Session) TargetBitrateKbps() int { return int(s.target.Load()) }

// KeyframeRequested reports and clears a pending keyframe request, raised by
// a client PLI or by the rate controller after a lost frame (host only).
func (s *Session) KeyframeRequested() bool { return s.keyframe.Swap(false) }

// FECLoss is the smoothed pre-FEC loss estimate driving parity (host only).
func (s *Session) FECLoss() float64 {
	if s.policy == nil {
		return 0
	}
	return s.policy.Loss()
}

// SetPath applies a new path's bitrate ceiling (host only).
func (s *Session) SetPath(p ratectl.Path) {
	if s.rc == nil {
		return
	}
	s.rcMu.Lock()
	s.rc.SetPath(p)
	s.target.Store(int64(s.rc.TargetKbps()))
	s.rcMu.Unlock()
}

// SetStream moves the session to a stream settled mid-session: Config
// says what it carries from now on. On a host, the rate controller takes
// its cap and start rate (zero keeps the old ones, as for ResolveResult)
// and the parity its floors.
func (s *Session) SetStream(cfg wire.SessionConfig, startKbps, maxKbps int, parity ParityProfile) {
	s.cfg.Store(&cfg)
	s.intra.Store(cfg.Codec == wire.CodecPyroWave)
	if !s.isHost {
		return
	}
	if cfg.BitrateKbps > 0 && (maxKbps <= 0 || maxKbps > int(cfg.BitrateKbps)) {
		maxKbps = int(cfg.BitrateKbps)
	}
	s.videoMu.Lock()
	s.pol = newPolicies(s.policy, parity)
	s.videoMu.Unlock()
	s.rcMu.Lock()
	s.rc.SetLimits(maxKbps, startKbps)
	s.target.Store(int64(s.rc.TargetKbps()))
	s.rcMu.Unlock()
}

func (s *Session) Stats() SessionStats {
	return SessionStats{
		PacketsSent:      s.stats.sent.Load(),
		PacketsReceived:  s.stats.recv.Load(),
		AuthFailures:     s.stats.authFail.Load(),
		SimulatedDrops:   s.stats.simDrop.Load(),
		FramesComplete:   s.stats.complete.Load(),
		FramesLost:       s.stats.lost.Load(),
		SlicesRecovered:  s.stats.recovered.Load(),
		PLIsSent:         s.stats.pliSent.Load(),
		PLIsReceived:     s.stats.pliRecv.Load(),
		FeedbackReceived: s.stats.feedback.Load(),
		InputResent:      s.stats.inputResent.Load(),
		InputDuplicates:  s.stats.inputDup.Load(),
		FramesPartial:    s.stats.partial.Load(),

		ClipboardSent:     s.stats.clipSent.Load(),
		ClipboardReceived: s.stats.clipRecv.Load(),
		ClipboardRefused:  s.stats.clipRefused.Load(),
	}
}

// SendVideoFrame sends a whole encoded frame, split into info.SliceCount
// slices (the session's configured count if zero).
func (s *Session) SendVideoFrame(info wire.VideoInfo, frame []byte) error {
	if !s.isHost {
		return ErrWrongRole
	}
	if info.SliceCount == 0 {
		info.SliceCount = max(s.cfg.Load().Slices, 1)
	}
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	_, err := s.enc.EncodeFrame(info, frame, s.policyFor(info), s.emitVideo)
	s.recordSent(info.FrameID, len(frame))
	return err
}

// SendVideoSlice sends one slice as soon as the encoder produces it, for
// sub-frame streaming. info.SliceCount must be the frame's final count.
func (s *Session) SendVideoSlice(info wire.VideoInfo, slice int, data []byte) error {
	if !s.isHost {
		return ErrWrongRole
	}
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	_, err := s.enc.EncodeSlice(info, slice, data, s.policyFor(info), s.emitVideo)
	s.recordSent(info.FrameID, len(data))
	return err
}

func (s *Session) recordSent(id uint32, n int) {
	now := Mono()
	s.ringMu.Lock()
	f := &s.sent[id%sentRingSize]
	if !f.valid || f.id != id {
		*f = sentFrame{id: id, valid: true}
	}
	f.at = now
	f.bytes += n
	s.ringMu.Unlock()
}

// FirstArrival is when the first datagram of a recent frame was read
// (client only). Call it from a media handler.
func (s *Session) FirstArrival(id uint32) int64 {
	if fs := s.firstSeen[id%sentRingSize]; fs.id == id {
		return fs.at
	}
	return 0
}

func (s *Session) lookupSent(id uint32) (sentFrame, bool) {
	s.ringMu.Lock()
	f := s.sent[id%sentRingSize]
	s.ringMu.Unlock()
	return f, f.valid && f.id == id
}

// SendAudio sends one Opus packet (host only).
func (s *Session) SendAudio(seq uint32, ts int64, opus []byte) error {
	if !s.isHost {
		return ErrWrongRole
	}
	if AEADOverhead+wire.AudioHeaderLen+len(opus) > s.maxDgram {
		return ErrTooBig
	}
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	b := s.txBuf[:AEADHeaderLen+wire.AudioHeaderLen+len(opus)]
	h := wire.AudioHeader{Seq: seq, Timestamp: ts}
	h.Put(b[AEADHeaderLen:])
	copy(b[AEADHeaderLen+wire.AudioHeaderLen:], opus)
	return s.sealAndWriteLocked(b)
}

// SendMic sends one packet of the client's microphone (client only, to a
// host with wire.FeatureMic). h.Flow is set to wire.FlowMic.
func (s *Session) SendMic(h wire.AudioHeader, payload []byte) error {
	if s.isHost {
		return ErrWrongRole
	}
	if s.peerFeatures&wire.FeatureMic == 0 {
		return ErrPeerUnsupported
	}
	if AEADOverhead+wire.AudioHeaderLen+len(payload) > s.maxDgram {
		return ErrTooBig
	}
	h.Flow = wire.FlowMic
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	b := s.txBuf[:AEADHeaderLen+wire.AudioHeaderLen+len(payload)]
	h.Put(b[AEADHeaderLen:])
	copy(b[AEADHeaderLen+wire.AudioHeaderLen:], payload)
	return s.sealAndWriteLocked(b)
}

// SendClipboard writes one clipboard item on this side's clipboard stream,
// opening it on first use, to a peer with wire.FeatureClipboard. It blocks
// until the item is handed to QUIC; video and input do not wait on it.
func (s *Session) SendClipboard(ctx context.Context, c *wire.Clip) error {
	if s.peerFeatures&wire.FeatureClipboard == 0 {
		return ErrPeerUnsupported
	}
	if len(c.Data) > wire.MaxClip {
		return wire.ErrClipTooLarge
	}
	s.clipMu.Lock()
	defer s.clipMu.Unlock()
	if s.clipOut == nil {
		str, err := s.conn.OpenUniStreamSync(ctx)
		if err != nil {
			return err
		}
		if _, err := str.Write([]byte{wire.StreamClipboard}); err != nil {
			str.CancelWrite(0)
			return err
		}
		s.clipOut = str
	}
	stop := context.AfterFunc(ctx, func() { s.clipOut.SetWriteDeadline(time.Now()) })
	err := wire.WriteClip(s.clipOut, c)
	stop()
	if err != nil {
		if !errors.Is(err, wire.ErrClipTooLarge) && !errors.Is(err, wire.ErrClipFormat) {
			// A part-written frame cannot be resumed; the next item opens
			// a new stream.
			s.clipOut.CancelWrite(0)
			s.clipOut = nil
		}
		return err
	}
	s.stats.clipSent.Add(1)
	return nil
}

func (s *Session) clipAcceptLoop() {
	ctx := s.conn.Context()
	for {
		str, err := s.conn.AcceptUniStream(ctx)
		if err != nil {
			return
		}
		s.wg.Go(func() { s.readClips(str) })
	}
}

func (s *Session) readClips(str *quic.ReceiveStream) {
	var kind [1]byte
	if _, err := io.ReadFull(str, kind[:]); err != nil || kind[0] != wire.StreamClipboard {
		str.CancelRead(0)
		return
	}
	for {
		c, err := wire.ReadClip(str, wire.MaxClip)
		switch {
		case err == nil:
			s.stats.clipRecv.Add(1)
			s.h.Clipboard(c)
		case errors.Is(err, wire.ErrClipTooLarge), errors.Is(err, wire.ErrClipHash), errors.Is(err, wire.ErrClipFormat):
			s.stats.clipRefused.Add(1)
		default:
			str.CancelRead(0)
			return
		}
	}
}

// SendRumble sends a gamepad rumble command (host only).
func (s *Session) SendRumble(r wire.Rumble) error {
	if !s.isHost {
		return ErrWrongRole
	}
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	pl := r.Put(s.txBuf[AEADHeaderLen:])
	return s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
}

// SendInput sends one input event (client only). For two seconds after a
// frame missed a datagram, and when the host drops duplicate Seqs, the
// previous event goes out again just ahead of it, so one lost datagram
// does not lose an event.
func (s *Session) SendInput(in wire.Input) error {
	if s.isHost {
		return ErrWrongRole
	}
	s.sealMu.Lock()
	defer s.sealMu.Unlock()
	now := Mono()
	if loss := s.lossAt.Load(); loss != 0 && now-loss < int64(redundantInputFor) &&
		s.peerFeatures&wire.FeatureInputDedup != 0 && s.lastInputAt != 0 && now-s.lastInputAt < int64(redundantInputAge) {
		pl := s.lastInput.Put(s.txBuf[AEADHeaderLen:])
		if s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)]) == nil {
			s.stats.inputResent.Add(1)
		}
	}
	s.lastInput, s.lastInputAt = in, now
	pl := in.Put(s.txBuf[AEADHeaderLen:])
	return s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
}

// sendSealed seals and sends a packet whose plaintext starts at
// AEADHeaderLen and whose capacity has room for the tag.
func (s *Session) sendSealed(pkt []byte) error {
	s.sealMu.Lock()
	err := s.sealAndWriteLocked(pkt)
	s.sealMu.Unlock()
	return err
}

func (s *Session) sealAndWriteLocked(pkt []byte) error {
	if !s.havePeer {
		return ErrNoPeer
	}
	sealed := s.sealer.Seal(pkt, pkt[AEADHeaderLen:])
	if s.impair != nil {
		before := s.impair.Dropped
		err := s.impair.Send(sealed)
		if s.impair.Dropped != before {
			s.stats.simDrop.Add(1)
		}
		return err
	}
	return s.writeLocked(sealed)
}

func (s *Session) writeLocked(sealed []byte) error {
	if _, err := s.udp.WriteToUDPAddrPort(sealed, s.peer); err != nil {
		return err
	}
	s.stats.sent.Add(1)
	return nil
}

// SendControl queues a control message. It does not block.
func (s *Session) SendControl(m wire.Message) error {
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	select {
	case s.ctrlOut <- m:
		return nil
	default:
		return ErrControlBacklog
	}
}

// recvLoop reads, opens and handles every media datagram on one goroutine
// (on macOS and Windows its own thread at a raised class; see rawReader):
// input reaches its handler, and a video shard the FEC decoder, with no
// hand-off to another goroutine.
func (s *Session) recvLoop() {
	defer s.wg.Done()
	read := newMediaReader(s.udp)
	for {
		n, from, err := read(s.rxBuf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		plain, err := s.opener.Open(s.rxBuf[:n])
		if err != nil || len(plain) == 0 {
			s.stats.authFail.Add(1)
			continue
		}
		s.stats.recv.Add(1)
		if s.isHost && from != s.lastFrom {
			s.lastFrom = from
			s.sealMu.Lock()
			s.peer, s.havePeer = from, true
			s.sealMu.Unlock()
		}
		if s.isHost {
			s.markMediaUp()
		}
		s.handleMedia(plain, Mono())
	}
}

func (s *Session) markMediaUp() {
	s.mediaUpOnce.Do(func() { close(s.mediaUp) })
}

func (s *Session) handleMedia(plain []byte, now int64) {
	switch plain[0] {
	case wire.KindPing:
		var p wire.Ping
		if p.Parse(plain) == nil {
			s.sealMu.Lock()
			pong := wire.Pong{Seq: p.Seq, T0: p.T0, T1: now, T2: Mono()}
			pl := pong.Put(s.txBuf[AEADHeaderLen:])
			_ = s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
			s.sealMu.Unlock()
		}
	case wire.KindPong:
		var p wire.Pong
		if p.Parse(plain) == nil {
			s.clock.Add(p, now)
			s.markMediaUp()
		}
	case wire.KindVideo:
		if !s.isHost {
			s.handleVideo(plain, now)
		}
	case wire.KindFeedback:
		var fb wire.Feedback
		if s.isHost && fb.Parse(plain) == nil {
			s.handleFeedback(fb, now)
		}
	case wire.KindAudio:
		h, payload, err := wire.ParseAudio(plain)
		switch {
		case err != nil:
		case !s.isHost && h.Flow == wire.FlowDesktop && s.h.Audio != nil:
			s.h.Audio(h, payload)
		case s.isHost && h.Flow == wire.FlowMic && s.h.Mic != nil:
			s.h.Mic(h, payload)
		}
	case wire.KindInput:
		var in wire.Input
		if !s.isHost || s.h.Input == nil || in.Parse(plain) != nil {
			return
		}
		if s.inputs.seen(in.Seq) {
			s.stats.inputDup.Add(1)
			return
		}
		s.h.Input(in)
	case wire.KindRumble:
		var r wire.Rumble
		if !s.isHost && s.h.Rumble != nil && r.Parse(plain) == nil {
			s.h.Rumble(r)
		}
	}
}

func (s *Session) handleVideo(plain []byte, now int64) {
	res, err := s.dec.Push(plain)
	if err != nil {
		return
	}
	if fs := &s.firstSeen[res.Info.FrameID%sentRingSize]; fs.id != res.Info.FrameID || fs.at == 0 {
		fs.id, fs.at = res.Info.FrameID, now
	}
	if s.intra.Load() {
		if s.hasPending && newerID(res.Info.FrameID, s.pendingInfo.FrameID) {
			s.deliverPartial(s.pendingInfo)
		}
		if !s.hasPending || newerID(res.Info.FrameID, s.pendingInfo.FrameID) {
			s.pendingInfo, s.hasPending = res.Info, true
		}
	}
	if res.SliceReady && s.h.VideoSlice != nil {
		s.h.VideoSlice(VideoSlice{
			Info:      res.Info,
			Index:     res.SliceIndex,
			Data:      s.dec.Slice(res.Info.FrameID, res.SliceIndex),
			Recovered: res.Recovered,
		})
	}
	if !res.FrameComplete {
		if s.pendingFB {
			s.sendFeedback(wire.Feedback{FrameID: s.finID})
		}
		return
	}
	s.stats.complete.Add(1)
	late := false
	if s.intra.Load() {
		late = s.delivered && !newerID(res.Info.FrameID, s.lastDelivered)
		if !late {
			s.lastDelivered, s.delivered = res.Info.FrameID, true
		}
	}
	if s.h.VideoFrame != nil && !late {
		var ok bool
		s.frameBuf, ok = s.dec.AppendFrame(s.frameBuf[:0], res.Info.FrameID)
		if ok {
			s.h.VideoFrame(res.Info, s.frameBuf)
		}
	}
	s.sendFeedback(wire.Feedback{FrameID: res.Info.FrameID, ArrivalTS: now, Bytes: uint32(res.FrameBytes)})
}

// onFrameFinal runs inside Decoder.Push when a frame leaves the window. Its
// shard counts accumulate until the next feedback; a lost frame also sends
// a rate-limited PLI and forces feedback out on the next video packet, so
// the host hears about loss even when no frame completes.
func (s *Session) onFrameFinal(st fec.FrameStats) {
	s.finRecv += st.RecvShards
	s.finSent += st.SentShards
	s.finID = st.FrameID
	s.hasFin = true
	s.stats.recovered.Add(uint64(st.SlicesRecovered))
	if st.RecvShards < st.SentShards {
		s.lossAt.Store(Mono())
	}
	if st.Complete {
		return
	}
	s.finLost = true
	s.pendingFB = true
	s.stats.lost.Add(1)
	if s.intra.Load() {
		return
	}
	now := Mono()
	gap := max(int64(minPLIInterval), 2*int64(s.clock.RTT()))
	if !s.sentPLI || now-s.lastPLI >= gap {
		if s.SendControl(&wire.PLI{}) == nil {
			s.sentPLI = true
			s.lastPLI = now
			s.stats.pliSent.Add(1)
		}
	}
}

func (s *Session) sendFeedback(fb wire.Feedback) {
	if s.hasFin {
		fb.HasFinal = true
		fb.FinalOK = !s.finLost
		fb.FinalFrameID = s.finID
		fb.FinalRecv = uint16(min(s.finRecv, 0xffff))
		fb.FinalSent = uint16(min(s.finSent, 0xffff))
		s.finRecv, s.finSent, s.finLost, s.hasFin = 0, 0, false, false
	}
	s.pendingFB = false
	s.sealMu.Lock()
	pl := fb.Put(s.txBuf[AEADHeaderLen:])
	_ = s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
	s.sealMu.Unlock()
}

func (s *Session) handleFeedback(fb wire.Feedback, now int64) {
	s.stats.feedback.Add(1)
	a := ratectl.Ack{Now: now, RTT: s.clock.RTT(), Loss: -1, Bytes: int(fb.Bytes)}
	if fb.ArrivalTS != 0 {
		if sent, ok := s.lookupSent(fb.FrameID); ok {
			a.SendTS, a.ArrivalTS = sent.at, fb.ArrivalTS
		}
	}
	if fb.HasFinal && fb.FinalSent > 0 {
		s.policy.Observe(int(fb.FinalRecv), int(fb.FinalSent))
		a.Loss = 1 - float64(min(fb.FinalRecv, fb.FinalSent))/float64(fb.FinalSent)
		a.FrameLost = !fb.FinalOK
	}
	s.rcMu.Lock()
	d := s.rc.Update(a)
	s.rcMu.Unlock()
	s.target.Store(int64(d.TargetKbps))
	if d.RequestKeyframe {
		s.keyframe.Store(true)
	}
}

func (s *Session) pingLoop() {
	defer s.wg.Done()
	t := time.NewTicker(probeInterval)
	defer t.Stop()
	fast := true
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if fast {
			select {
			case <-s.mediaUp:
				fast = false
				t.Reset(s.pingInterval)
			default:
			}
		}
		s.sealMu.Lock()
		if s.havePeer {
			s.pingSeq++
			p := wire.Ping{Seq: s.pingSeq, T0: Mono()}
			pl := p.Put(s.txBuf[AEADHeaderLen:])
			_ = s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
		}
		s.sealMu.Unlock()
	}
}

func (s *Session) ctrlReadLoop() {
	defer s.wg.Done()
	for {
		m, err := wire.ReadMessage(s.ctrl)
		if err != nil {
			s.shutdown(fmt.Errorf("transport: control stream: %w", err))
			return
		}
		switch m := m.(type) {
		case *wire.PLI:
			s.stats.pliRecv.Add(1)
			if s.isHost {
				s.keyframe.Store(true)
			}
		case *wire.PeerState:
			s.peerState.Store(m)
		case *wire.Goodbye:
			if s.h.Control != nil {
				s.h.Control(m)
			}
			s.shutdown(fmt.Errorf("transport: peer said goodbye (%d): %s", m.Reason, m.Message))
			return
		}
		if s.h.Control != nil {
			s.h.Control(m)
		}
	}
}

func (s *Session) ctrlWriteLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case m := <-s.ctrlOut:
			if err := wire.WriteMessage(s.ctrl, m); err != nil {
				s.shutdown(fmt.Errorf("transport: control write: %w", err))
				return
			}
			if _, bye := m.(*wire.Goodbye); bye {
				s.shutdown(ErrClosed)
				return
			}
		}
	}
}

func (s *Session) shutdown(err error) {
	s.closeOnce.Do(func() {
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
		_ = s.conn.CloseWithError(0, "")
		_ = s.udp.Close()
	})
}

// ClosedByPeer reports whether err, a session's Err, is the peer closing
// the session on purpose. A goodbye can be lost with the close that
// follows it, which still tells an orderly end from a lost connection.
func ClosedByPeer(err error) bool {
	var ae *quic.ApplicationError
	return errors.As(err, &ae) && ae.Remote && ae.ErrorCode == 0
}

// Err is why the session ended, or nil while it runs.
func (s *Session) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close says goodbye to the peer, tears the session down and waits for its
// goroutines.
func (s *Session) Close() error {
	if s.SendControl(&wire.Goodbye{Reason: wire.GoodbyeNormal}) == nil {
		select {
		case <-s.done:
		case <-time.After(time.Second):
		}
	}
	s.shutdown(ErrClosed)
	s.wg.Wait()
	return nil
}
