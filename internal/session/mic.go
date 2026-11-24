package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

const (
	micOpusBitrate = 32000
	// micMaxDepth bounds the host's mic jitter buffer: voice that comes
	// later than this is worse than a gap.
	micMaxDepth = 40 * time.Millisecond
)

// MicStats describe the client's microphone on either end.
type MicStats struct {
	// Device is the host's virtual input device apps record the client
	// from; Hint says why there is none.
	Device string
	Hint   string
	Muted  bool
	// Packets sent (client) or received (host); Concealed frames the host
	// filled in for lost or late ones.
	Packets   uint64
	Concealed uint64
}

func frameOf(h wire.AudioHeader) time.Duration {
	if h.FrameQuarterMs == 0 {
		return audio.DefaultFrameDuration
	}
	return time.Duration(h.FrameQuarterMs) * 250 * time.Microsecond
}

// micReceiver plays one client's microphone into the host's virtual mic.
// The first packet fixes the codec and frame for the session and starts
// play-out; the sink opens then, not before.
type micReceiver struct {
	ctx    context.Context
	cancel context.CancelFunc
	dev    audio.VirtualMic
	open   func(audio.VirtualMic) (audio.MicSink, error)
	logf   func(string, ...any)
	wg     *sync.WaitGroup

	once  sync.Once
	ready atomic.Bool
	codec wire.AudioCodec
	frame time.Duration
	jb    *audio.JitterBuffer

	pkts, mismatched atomic.Uint64
}

func (m *micReceiver) push(h wire.AudioHeader, payload []byte) {
	m.once.Do(func() {
		m.codec, m.frame = h.Codec, frameOf(h)
		if m.codec != wire.AudioOpus && m.codec != wire.AudioPCM16 {
			return
		}
		m.jb = audio.NewJitterBuffer(audio.JitterConfig{FrameDuration: m.frame, TargetDepth: max(2*m.frame, 10*time.Millisecond), MaxDepth: micMaxDepth})
		m.ready.Store(true)
		m.wg.Go(m.loop)
	})
	if !m.ready.Load() || h.Codec != m.codec || frameOf(h) != m.frame {
		m.mismatched.Add(1)
		return
	}
	m.jb.Push(uint16(h.Seq), payload)
	m.pkts.Add(1)
}

func (m *micReceiver) loop() {
	var dec audio.FrameDecoder
	if m.codec == wire.AudioPCM16 {
		dec = audio.NewPCMDecoder(m.frame, audio.MicChannels)
	} else {
		d, err := audio.NewDecoderChannels(m.frame, audio.MicChannels)
		if err != nil {
			m.logf("planck: client microphone: %v", err)
			return
		}
		dec = d
	}
	sink, err := m.open(m.dev)
	if err != nil {
		m.logf("planck: client microphone: %v", err)
		return
	}
	defer sink.Close()
	m.logf("planck: client microphone plays into %s", sink.Device())
	playout := audio.NewPlayout(m.jb, dec)
	pcm := make([]int16, dec.FrameSamples()*audio.MicChannels)
	step := int64(m.frame)
	next := transport.Mono()
	for m.ctx.Err() == nil {
		next += step
		transport.SleepUntilMono(next)
		if now := transport.Mono(); now-next > 5*step {
			next = now
		}
		if st, err := playout.Next(pcm); err != nil || st == audio.Buffering {
			continue
		}
		_ = sink.Write(pcm)
	}
}

func (m *micReceiver) stats() (pkts, concealed uint64) {
	if !m.ready.Load() {
		return m.pkts.Load(), 0
	}
	return m.pkts.Load(), m.jb.Stats().Concealment
}

// micSender captures the client's microphone and sends it while unmuted.
// Every session starts muted, so a client on speakers does not feed the
// host its own audio back until the user says so. Capture opens on the
// first unmute and stays open for the stream.
type micSender struct {
	cc   *conn
	open func() (audio.MicCapturer, error)
	logf func(string, ...any)

	muted atomic.Bool
	pkts  atomic.Uint64

	mu   sync.Mutex
	capt audio.MicCapturer
	err  error

	// Used on the capture callback only.
	buf    []int16
	opus   *audio.Encoder
	packet []byte
	seq    uint32
}

func newMicSender(cc *conn, open func() (audio.MicCapturer, error), logf func(string, ...any)) *micSender {
	s := &micSender{cc: cc, open: open, logf: logf, packet: make([]byte, audio.MaxPacketSize)}
	s.muted.Store(true)
	return s
}

func (s *micSender) setMuted(muted bool) error {
	s.muted.Store(muted)
	if muted {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capt != nil {
		return nil
	}
	open := s.open
	if open == nil {
		open = audio.NewMicCapturer
	}
	capt, err := open()
	if err == nil {
		err = capt.Start(s.cc.ctx, s.onPCM)
	}
	if err != nil {
		s.muted.Store(true)
		s.err = err
		return err
	}
	s.capt = capt
	return nil
}

// onPCM cuts the capture into frames: 5 ms raw PCM on a LAN route, 10 ms
// Opus elsewhere.
func (s *micSender) onPCM(pcm []int16, _ int64) {
	if s.muted.Load() {
		s.buf = s.buf[:0]
		return
	}
	rt := s.cc.live.Load()
	raw := rt.kind == discovery.PathLANDirect
	frame := audio.DefaultFrameDuration
	if raw {
		frame = audio.MicPCMFrameDuration
	}
	n := audio.FrameSamples(frame) * audio.MicChannels
	s.buf = append(s.buf, pcm...)
	for len(s.buf) >= n {
		s.send(rt.sess, s.buf[:n], raw, frame)
		s.buf = s.buf[:copy(s.buf, s.buf[n:])]
	}
}

func (s *micSender) send(sess *transport.Session, frame []int16, raw bool, d time.Duration) {
	h := wire.AudioHeader{Seq: s.seq, Timestamp: transport.Mono(), FrameQuarterMs: uint8(d / (250 * time.Microsecond))}
	var size int
	if raw {
		h.Codec = wire.AudioPCM16
		size = audio.PutPCM16(s.packet, frame)
	} else {
		if s.opus == nil {
			enc, err := audio.NewEncoder(audio.EncoderConfig{Channels: audio.MicChannels, Voice: true, Bitrate: micOpusBitrate})
			if err != nil {
				return
			}
			s.opus = enc
		}
		var err error
		if size, err = s.opus.Encode(frame, s.packet); err != nil {
			return
		}
	}
	s.seq++
	if err := sess.SendMic(h, s.packet[:size]); err == nil {
		s.pkts.Add(1)
	}
}

func (s *micSender) close() {
	s.mu.Lock()
	capt := s.capt
	s.capt = nil
	s.mu.Unlock()
	if capt != nil {
		capt.Close()
	}
}

var errNoHostMic = errors.New("this host does not take a microphone")
