package session

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"runtime"
	"sync"

	"github.com/planckdotgg/planck/pkg/audio"
	"github.com/planckdotgg/planck/pkg/defaults"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/linkinfo"
	"github.com/planckdotgg/planck/pkg/ratectl"
	"github.com/planckdotgg/planck/pkg/transport"
	"github.com/planckdotgg/planck/pkg/wire"
)

// clientCaps is the client's wire.Capabilities body.
type clientCaps struct {
	V         int                         `json:"v"`
	Client    defaults.ClientCapabilities `json:"client"`
	Route     defaults.RouteInfo          `json:"route"`
	Overrides defaults.UserOverrides      `json:"overrides"`
}

const clientCapsVersion = 1

func routeKind(k discovery.PathKind) defaults.RouteKind {
	switch k {
	case discovery.PathLANDirect:
		return defaults.RouteLAN
	case discovery.PathTailscaleDirect:
		return defaults.RouteTailscaleDirect
	}
	return defaults.RouteWAN
}

func routeFromPath(p ratectl.Path) defaults.RouteKind {
	switch p {
	case ratectl.PathLAN:
		return defaults.RouteLAN
	case ratectl.PathTailscaleDirect:
		return defaults.RouteTailscaleDirect
	}
	return defaults.RouteWAN
}

// streamChroma is the coded chroma for a profile's sampling and depth.
func streamChroma(p *defaults.ResolvedProfile) encode.Chroma {
	switch {
	case p.Chroma444 && p.BitDepth == 10:
		return encode.YUV444P10
	case p.Chroma444:
		return encode.YUV444
	case p.BitDepth == 10:
		return encode.P010
	}
	return encode.NV12
}

// hostCapsCache probes the encoders once; displays and the virtual display
// driver are read fresh for each session.
type hostCapsCache struct {
	once     sync.Once
	encoders []defaults.EncoderCap
}

func (h *Host) capabilities() defaults.HostCapabilities {
	if h.cfg.Capabilities != nil {
		c := h.cfg.Capabilities()
		c.Forced = defaults.Merge(c.Forced, h.cfg.Policy)
		return c
	}
	h.caps.once.Do(func() {
		h.caps.encoders = probeEncoders(h.cfg)
		for _, e := range h.caps.encoders {
			h.logf("planck: encoder %s on %s: %d codecs, %d engines", e.API, e.Adapter.Name, len(e.Codecs), e.Engines)
		}
	})
	c := defaults.HostCapabilities{
		OS:            runtime.GOOS,
		Encoders:      h.caps.encoders,
		Displays:      hostDisplays(h.cfg),
		PinnedDisplay: h.cfg.Display,
		Forced:        h.cfg.Policy,
	}
	if !h.cfg.Synth {
		c.VDD = vddCap()
	}
	return c
}

func (h *Host) capabilitiesJSON() []byte {
	b, err := json.Marshal(h.capabilities())
	if err != nil {
		return []byte("{}")
	}
	return b
}

// resolve settles a session with a client that sent its capabilities.
// Unreadable capabilities fall back to negotiate.
func (h *Host) resolve(r transport.ResolveRequest) (transport.ResolveResult, error) {
	var cc clientCaps
	if err := json.Unmarshal(r.ClientCaps, &cc); err != nil || cc.V != clientCapsVersion {
		h.logf("planck: client capabilities unreadable (%v), using the legacy request", err)
		acc, err := h.negotiate(r.Request)
		if acc.Codec == wire.CodecPyroWave {
			acc.Codec = wire.CodecHEVC
		}
		return transport.ResolveResult{Config: acc}, err
	}
	if err := cc.Overrides.Validate(); err != nil {
		return transport.ResolveResult{}, fmt.Errorf("client overrides: %w", err)
	}
	prof := h.resolveProfile(cc, r.Remote.Addr(), r.Path, r.LANDatagram)
	for _, w := range prof.Warnings {
		h.logf("planck: profile: %s", w)
	}
	js, err := json.Marshal(prof)
	if err != nil {
		return transport.ResolveResult{}, err
	}
	return transport.ResolveResult{
		Config: h.profileConfig(&prof, r.Request), Profile: js,
		StartKbps: prof.BitrateKbps, MaxKbps: prof.MaxBitrateKbps,
		Parity: transport.ParityProfile{MinPercent: prof.FEC.MinParityPercent,
			KeyframePercent: prof.FEC.KeyframeParityPercent, CriticalPercent: prof.FEC.CriticalParityPercent},
	}, nil
}

// routeLink is linkinfo.Route; tests give it a link of their own.
var routeLink = linkinfo.Route

// resolveProfile settles a stream from both ends' capabilities, on the
// route to remote.
func (h *Host) resolveProfile(cc clientCaps, remote netip.Addr, path ratectl.Path, lanDatagram bool) defaults.ResolvedProfile {
	caps := h.capabilities()
	if name, link, err := routeLink(remote); err == nil {
		caps.Link = link
		h.logf("planck: route to %v via %s: wired %v, %d Mbps", remote, name, link.Wired, link.SpeedMbps)
	}
	route := cc.Route
	if route.Kind == 0 {
		route.Kind = routeFromPath(path)
	}
	route.LANDatagram = lanDatagram
	return defaults.Resolve(caps, cc.Client, route, cc.Overrides)
}

// profileConfig is a profile's legacy SessionConfig, which SessionReady
// carries.
func (h *Host) profileConfig(p *defaults.ResolvedProfile, req wire.SessionConfig) wire.SessionConfig {
	acc := profileVideoConfig(p)
	if !h.cfg.NoAudio && req.AudioChannels > 0 {
		acc.AudioSampleRate, acc.AudioChannels = audio.SampleRate, audio.Channels
	}
	return acc
}

// scalePercent is the display scale, in percent, for a client display of
// backing scale s: 100 at 1x or unknown.
func scalePercent(s float64) int {
	if s <= 1 {
		return 100
	}
	return int(math.Round(s * 100))
}

// resizeDisplay is d as a Resize carries it.
func resizeDisplay(d defaults.ClientDisplay) wire.ResizeDisplay {
	u16 := func(v int) uint16 { return uint16(min(max(v, 0), math.MaxUint16)) }
	return wire.ResizeDisplay{Width: u16(d.WidthPx), Height: u16(d.HeightPx), ScaleMilli: u16(int(math.Round(d.Scale * 1000))),
		MaxHz: u16(d.MaxHz), MinHz: u16(d.MinHz), HDR: d.HDR}
}

// clientDisplayOf is the display a Resize carries, nil when it has none.
func clientDisplayOf(d wire.ResizeDisplay) *defaults.ClientDisplay {
	if d.Width == 0 || d.Height == 0 {
		return nil
	}
	return &defaults.ClientDisplay{WidthPx: int(d.Width), HeightPx: int(d.Height), Scale: float64(d.ScaleMilli) / 1000,
		MaxHz: int(d.MaxHz), MinHz: int(d.MinHz), HDR: d.HDR}
}

// profileVideoConfig is the video part of a profile's SessionConfig.
func profileVideoConfig(p *defaults.ResolvedProfile) wire.SessionConfig {
	acc := wire.SessionConfig{Codec: wire.Codec(p.Codec), Chroma: wire.Chroma420, Width: uint16(p.Width), Height: uint16(p.Height),
		FPS: uint16(p.FPS), BitrateKbps: uint32(p.MaxBitrateKbps), Slices: uint8(max(1, p.Slices)), SubFrame: p.SubFrame && p.Slices > 1}
	if p.Chroma444 {
		acc.Chroma = wire.Chroma444
	}
	return acc
}

// paramsFromProfile is the video pipeline a profile asks for.
func paramsFromProfile(base VideoParams, p *defaults.ResolvedProfile, shard int) VideoParams {
	v := base
	v.Adapter = p.EncodeAdapter.Name
	v.Codec = encode.Codec(p.Codec)
	v.Chroma = streamChroma(p)
	v.Width, v.Height, v.FPS = p.Width, p.Height, p.FPS
	v.Slices, v.SubFrame = max(1, p.Slices), p.SubFrame && p.Slices > 1
	v.Split = encode.SplitOff
	if p.Split {
		v.Split = encode.SplitOn
	}
	if p.CaptureDisplay != "" {
		v.Display = p.CaptureDisplay
	}
	v.VirtualDisplay = p.VirtualDisplay
	if v.Codec == encode.PyroWave {
		v.Slices, v.SubFrame, v.ChunkLen = 0, false, shard
	}
	return v
}

// ParseProfile reads a wire.Profile body.
func ParseProfile(b []byte) (*defaults.ResolvedProfile, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var p defaults.ResolvedProfile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("session: profile: %w", err)
	}
	return &p, nil
}
