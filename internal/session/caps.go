package session

import (
	"encoding/json"
	"fmt"
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
	caps := h.capabilities()
	if name, link, err := linkinfo.Route(r.Remote.Addr()); err == nil {
		caps.Link = link
		h.logf("planck: route to %v via %s: wired %v, %d Mbps", r.Remote.Addr(), name, link.Wired, link.SpeedMbps)
	}
	route := cc.Route
	if route.Kind == 0 {
		route.Kind = routeFromPath(r.Path)
	}
	route.LANDatagram = r.LANDatagram
	prof := defaults.Resolve(caps, cc.Client, route, cc.Overrides)
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

// profileConfig is a profile's legacy SessionConfig, which SessionReady
// carries.
func (h *Host) profileConfig(p *defaults.ResolvedProfile, req wire.SessionConfig) wire.SessionConfig {
	acc := wire.SessionConfig{Codec: wire.Codec(p.Codec), Chroma: wire.Chroma420, Width: uint16(p.Width), Height: uint16(p.Height),
		FPS: uint16(p.FPS), BitrateKbps: uint32(p.MaxBitrateKbps), Slices: uint8(max(1, p.Slices)), SubFrame: p.SubFrame && p.Slices > 1}
	if p.Chroma444 {
		acc.Chroma = wire.Chroma444
	}
	if !h.cfg.NoAudio && req.AudioChannels > 0 {
		acc.AudioSampleRate, acc.AudioChannels = audio.SampleRate, audio.Channels
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
