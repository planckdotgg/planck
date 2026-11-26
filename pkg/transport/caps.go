package transport

import (
	"fmt"
	"net/netip"

	"github.com/planckdotgg/planck/pkg/fec"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/wire"
)

// ResolveRequest is what the host knows when it settles a session with a
// client that sent wire.Capabilities.
type ResolveRequest struct {
	Request    wire.SessionConfig
	ClientCaps []byte
	Path       ratectl.Path
	// LANDatagram is true when the session uses wire.MaxLANDatagram.
	LANDatagram bool
	Remote      netip.AddrPort
}

// ResolveResult is the settled session: Config goes out in SessionReady and
// Profile in wire.Profile right after it.
type ResolveResult struct {
	Config  wire.SessionConfig
	Profile []byte
	// StartKbps and MaxKbps seed and cap the rate controller; zero keeps
	// the defaults, capped by Config.BitrateKbps.
	StartKbps, MaxKbps int
	Parity             ParityProfile
}

// ParityProfile puts floors under the adaptive parity, in percent of a
// block's data shards.
type ParityProfile struct {
	MinPercent      int
	KeyframePercent int
	// CriticalPercent covers the leading slices SendChunkedFrame is told
	// are critical, with at least two parity shards.
	CriticalPercent int
}

// floorPolicy is the adaptive policy with a percentage floor and a minimum
// shard count.
type floorPolicy struct {
	base      fec.ParityPolicy
	pct       int
	minShards int
}

func (p *floorPolicy) Parity(data int) int {
	n := p.base.Parity(data)
	if p.pct > 0 {
		n = max(n, (data*p.pct+99)/100)
	}
	n = max(n, p.minShards)
	return min(n, wire.MaxShards-data)
}

// policies are a session's per-kind parity choices, built once so sends
// do not allocate.
type policies struct {
	normal, key, critical floorPolicy
}

func newPolicies(base fec.ParityPolicy, pp ParityProfile) policies {
	return policies{
		normal:   floorPolicy{base: base, pct: pp.MinPercent},
		key:      floorPolicy{base: base, pct: max(pp.MinPercent, pp.KeyframePercent)},
		critical: floorPolicy{base: base, pct: max(pp.MinPercent, pp.CriticalPercent), minShards: 2},
	}
}

// PeerCapabilities is the JSON the peer sent in wire.Capabilities, or nil
// when one end does not have wire.FeatureCapabilities.
func (s *Session) PeerCapabilities() []byte { return s.peerCaps }

// Profile is the JSON of the host's wire.Profile, or nil without one.
func (s *Session) Profile() []byte { return s.profile }

// SendChunkedFrame sends a frame already cut into slices ending at ends,
// such as PyroWave's packetizer output. The first critical slices get the
// critical parity floor. Keyframe parity does not apply: every such frame
// is intra.
func (s *Session) SendChunkedFrame(info wire.VideoInfo, frame []byte, ends []int, critical int) error {
	if !s.isHost {
		return ErrWrongRole
	}
	if len(ends) == 0 || len(ends) > wire.MaxSlices || ends[len(ends)-1] != len(frame) {
		return fmt.Errorf("transport: %d slice ends for a %d-byte frame", len(ends), len(frame))
	}
	info.SliceCount = uint8(len(ends))
	s.videoMu.Lock()
	defer s.videoMu.Unlock()
	s.paceFrame(info, len(frame))
	lo := 0
	for i, end := range ends {
		pol := &s.pol.normal
		if i < critical {
			pol = &s.pol.critical
		}
		if _, err := s.enc.EncodeSlice(info, i, frame[lo:end], pol, s.emitVideo); err != nil {
			s.recordSent(info.FrameID, lo)
			return err
		}
		lo = end
	}
	s.recordSent(info.FrameID, len(frame))
	return nil
}

func (s *Session) policyFor(info wire.VideoInfo) fec.ParityPolicy {
	if info.Keyframe {
		return &s.pol.key
	}
	return &s.pol.normal
}

// deliverPartial hands frame id to the frame handler as far as it arrived,
// for intra streams whose lost chunks only blur locally. It runs once per
// frame, when a newer frame's first shard shows id is not coming whole.
func (s *Session) deliverPartial(info wire.VideoInfo) {
	if s.delivered && !newerID(info.FrameID, s.lastDelivered) {
		return
	}
	var ok bool
	s.frameBuf, _, ok = s.dec.AppendPartial(s.frameBuf[:0], info.FrameID)
	if !ok || len(s.frameBuf) == 0 {
		return
	}
	s.lastDelivered, s.delivered = info.FrameID, true
	s.stats.partial.Add(1)
	if s.h.VideoFrame != nil {
		s.h.VideoFrame(info, s.frameBuf)
	}
}

func newerID(a, b uint32) bool { return int32(a-b) > 0 }
