package transport

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/planckdotgg/planck/pkg/wire"
)

// ErrCalibrationUnsupported is a calibration dial to a host whose Hello
// does not carry wire.FeatureCalibration: an older Planck.
var ErrCalibrationUnsupported = errors.New("transport: the host cannot calibrate; update Planck on it")

// ErrProbeBusy is a probe while another of the session's runs.
var ErrProbeBusy = errors.New("transport: a probe train is already running")

// ProbeSpec asks the host for Count datagrams of Size bytes, AEAD sealing
// included, paced at RateKbps or back to back at 0.
type ProbeSpec struct {
	Count    int
	Size     int
	RateKbps int
}

// ProbeTrain is what a probe train did: Sent datagrams went out SendNs
// from the first to the last, and Arrivals holds each one's arrival on
// the client's Mono clock, 0 for one lost.
type ProbeTrain struct {
	Spec     ProbeSpec
	Sent     int
	SendNs   int64
	Arrivals []int64
}

// Received counts the datagrams that arrived.
func (t ProbeTrain) Received() int {
	n := 0
	for _, a := range t.Arrivals {
		if a != 0 {
			n++
		}
	}
	return n
}

// Prefix counts the datagrams that arrived before the first one lost.
func (t ProbeTrain) Prefix() int {
	for i, a := range t.Arrivals {
		if a == 0 {
			return i
		}
	}
	return len(t.Arrivals)
}

// Span is from the first arrival to the last.
func (t ProbeTrain) Span() time.Duration {
	var first, last int64
	for _, a := range t.Arrivals {
		if a == 0 {
			continue
		}
		if first == 0 || a < first {
			first = a
		}
		last = max(last, a)
	}
	return time.Duration(last - first)
}

// probeGrace is how long after ProbeDone a train's last datagrams may
// still arrive: they left before it, on the same network.
const probeGrace = 25 * time.Millisecond

// probeCollector records one train's arrivals from the receive loop.
type probeCollector struct {
	train uint32
	mu    sync.Mutex
	at    []int64
	done  chan wire.ProbeDone
}

func (c *probeCollector) record(h wire.ProbeHeader, now int64) {
	c.mu.Lock()
	if h.Train == c.train && int(h.Index) < len(c.at) && c.at[h.Index] == 0 {
		c.at[h.Index] = now
	}
	c.mu.Unlock()
}

// Probe asks a host with wire.FeatureCalibration for a train of probe
// datagrams and returns what arrived (client only). One train runs at a
// time; ctx ends the wait.
func (s *Session) Probe(ctx context.Context, spec ProbeSpec) (ProbeTrain, error) {
	switch {
	case s.isHost:
		return ProbeTrain{}, ErrWrongRole
	case s.peerFeatures&wire.FeatureCalibration == 0:
		return ProbeTrain{}, ErrPeerUnsupported
	case spec.Count <= 0 || spec.Count > wire.MaxProbeCount || spec.Size < AEADOverhead+wire.ProbeHeaderLen || spec.Size > s.maxDgram || spec.RateKbps < 0:
		return ProbeTrain{}, errors.New("transport: probe spec out of range")
	}
	c := &probeCollector{at: make([]int64, spec.Count), done: make(chan wire.ProbeDone, 1)}
	s.probeMu.Lock()
	if s.probe.Load() != nil {
		s.probeMu.Unlock()
		return ProbeTrain{}, ErrProbeBusy
	}
	s.probeSeq++
	c.train = s.probeSeq
	s.probe.Store(c)
	s.probeMu.Unlock()
	defer s.probe.Store(nil)
	req := &wire.ProbeRequest{Train: c.train, Count: uint32(spec.Count), Size: uint16(spec.Size - AEADOverhead), RateKbps: uint32(spec.RateKbps)}
	if err := s.SendControl(req); err != nil {
		return ProbeTrain{}, err
	}
	var done wire.ProbeDone
	select {
	case done = <-c.done:
	case <-ctx.Done():
		return ProbeTrain{}, ctx.Err()
	case <-s.done:
		return ProbeTrain{}, s.Err()
	}
	if done.Sent == 0 {
		return ProbeTrain{}, ErrProbeBusy
	}
	t := time.NewTimer(probeGrace + s.clock.RTT()/2)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return ProbeTrain{}, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ProbeTrain{Spec: spec, Sent: int(done.Sent), SendNs: int64(done.SendNs), Arrivals: c.at}, nil
}

// onProbeDone hands a host's ProbeDone to the train it ends.
func (s *Session) onProbeDone(m *wire.ProbeDone) {
	if c := s.probe.Load(); c != nil && c.train == m.Train {
		select {
		case c.done <- *m:
		default:
		}
	}
}

// serveProbe sends the train a client asked for, one train at a time,
// and says when it went out (host only).
func (s *Session) serveProbe(req *wire.ProbeRequest) {
	size := int(req.Size) + AEADOverhead
	if !s.probing.CompareAndSwap(false, true) {
		_ = s.SendControl(&wire.ProbeDone{Train: req.Train})
		return
	}
	if req.Count == 0 || req.Count > wire.MaxProbeCount || int(req.Size) < wire.ProbeHeaderLen || size > s.maxDgram {
		s.probing.Store(false)
		_ = s.SendControl(&wire.ProbeDone{Train: req.Train})
		return
	}
	s.wg.Go(func() {
		defer s.probing.Store(false)
		sent, ns := s.sendProbeTrain(req.Train, int(req.Count), int(req.Size), int(req.RateKbps))
		_ = s.SendControl(&wire.ProbeDone{Train: req.Train, Sent: uint32(sent), SendNs: uint64(ns)})
	})
}

// sendProbeTrain seals and sends count probe datagrams of size plaintext
// bytes, paced at rateKbps or in send-offload trains at 0. It stops early
// when the session ends.
func (s *Session) sendProbeTrain(train uint32, count, size, rateKbps int) (sent int, ns int64) {
	buf := make([]byte, AEADHeaderLen+size+AEADTagLen)
	var p Pacer
	p.SetRate(float64(rateKbps)*1000/8, 0)
	var first, last int64
	for i := 0; i < count; i++ {
		select {
		case <-s.done:
			return sent, last - first
		default:
		}
		if until := p.Reserve(size + AEADOverhead); until != 0 {
			s.sealMu.Lock()
			n, _ := s.train.flush(s.peer)
			s.sealMu.Unlock()
			s.stats.sent.Add(uint64(n))
			SleepUntilMono(until)
		}
		pkt := buf[:AEADHeaderLen+size]
		clear(pkt[AEADHeaderLen:])
		now := Mono()
		h := wire.ProbeHeader{Train: train, Index: uint32(i), Count: uint32(count), SentTS: now}
		h.Put(pkt[AEADHeaderLen:])
		s.sealMu.Lock()
		if !s.havePeer {
			s.sealMu.Unlock()
			return sent, last - first
		}
		sealed := s.sealer.Seal(pkt, pkt[AEADHeaderLen:])
		n, err := s.train.add(sealed, s.peer)
		s.sealMu.Unlock()
		s.stats.sent.Add(uint64(n))
		if err != nil {
			return sent, last - first
		}
		if first == 0 {
			first = now
		}
		last = now
		sent++
	}
	s.sealMu.Lock()
	n, _ := s.train.flush(s.peer)
	s.sealMu.Unlock()
	s.stats.sent.Add(uint64(n))
	return sent, Mono() - first
}

// Ping sends n pings every apart and returns the round trips of the pongs
// that came back by the time the last one is due (client only).
func (s *Session) Ping(ctx context.Context, n int, every time.Duration) ([]time.Duration, error) {
	if s.isHost {
		return nil, ErrWrongRole
	}
	tap := make(chan time.Duration, n)
	if !s.pongTap.CompareAndSwap(nil, &tap) {
		return nil, ErrProbeBusy
	}
	defer s.pongTap.Store(nil)
	t := time.NewTicker(every)
	defer t.Stop()
	var rtts []time.Duration
	collect := func() {
		for {
			select {
			case r := <-tap:
				rtts = append(rtts, r)
			default:
				return
			}
		}
	}
	for i := 0; i < n; i++ {
		s.sealMu.Lock()
		s.pingSeq++
		p := wire.Ping{Seq: s.pingSeq, T0: Mono()}
		pl := p.Put(s.txBuf[AEADHeaderLen:])
		err := s.sealAndWriteLocked(s.txBuf[:AEADHeaderLen+len(pl)])
		s.sealMu.Unlock()
		if err != nil {
			return rtts, err
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return rtts, ctx.Err()
		case <-s.done:
			return rtts, s.Err()
		}
		collect()
	}
	time.Sleep(min(every, 50*time.Millisecond))
	collect()
	return rtts, nil
}
