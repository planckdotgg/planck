package main

import (
	"time"

	"github.com/planckdotgg/planck/internal/session"
	usage "github.com/planckdotgg/planck/internal/telemetry"
	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/discovery"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/phaselock"
)

// sessionStats is the Statistics page's view of s, a stream that started
// at started.
func sessionStats(s session.ClientStats, started time.Time) ui.SessionStats {
	out := ui.SessionStats{
		Live: s.Connected, HostName: s.HostName, HostID: hostKey(s), Started: started,
		Capture: ui.StageStat{P50: s.CaptureMs}, Encode: stage(s.Encode), Network: stage(s.Network),
		Decode: stage(s.Decode), Present: stage(s.Present), Total: stage(s.E2E),
		FrameTimesMs: s.FrameTimesMs, FPS: s.FPS,
		FramesReceived: s.FramesReceived, FramesDropped: s.FramesDropped, FramesLost: s.FramesLost, Keyframes: s.Keyframes,
		BitrateMbps: s.Kbps / 1000, CeilingMbps: float64(s.MaxBitrateKbps) / 1000,
		LossPct: s.LossPct, FECParityPct: s.FECParityPct, FECRepaired: s.SlicesRecovered,
		AudioBufMs: s.AudioBufferMs, MicMuted: s.Mic.Muted, MicDevice: s.Mic.Device, MicHint: s.Mic.Hint,
		Width: s.Width, Height: s.Height, RTTMs: s.RTTMs, DatagramBytes: s.DatagramBytes,
		Profile: s.Profile, HostPolicy: s.HostPolicy, Chroma: encode.NV12, BitDepth: 8,
	}
	if s.HostEncodeMs > 0 {
		out.Encode = ui.StageStat{P50: s.HostEncodeMs}
	}
	if s.E2EGlass.Samples > 0 {
		out.Present, out.Total = stage(s.Glass), stage(s.E2EGlass)
	}
	out.Display = displayPhase(s)
	for _, k := range []discovery.PathKind{discovery.PathLANDirect, discovery.PathTailscaleDirect, discovery.PathTailscaleDERP} {
		if k.String() == s.Path {
			out.PathKind = k
		}
	}
	if c, err := session.ParseCodec(s.Codec); err == nil {
		out.Codec = c
	}
	if s.Chroma == "4:4:4" {
		out.Chroma = encode.YUV444
	}
	if p := s.Profile; p != nil {
		out.TargetFPS, out.BitDepth = p.FPS, p.BitDepth
		out.EncoderGPU, out.EncoderAPI = p.EncodeAdapter.Name, p.EncoderAPI
		switch {
		case p.Chroma444 && p.BitDepth == 10:
			out.Chroma = encode.YUV444P10
		case p.Chroma444:
			out.Chroma = encode.YUV444
		case p.BitDepth == 10:
			out.Chroma = encode.P010
		}
	}
	return out
}

// displayPhase is how the client's screen shows the stream, as the
// sink measured it.
func displayPhase(s session.ClientStats) ui.DisplayPhase {
	r := s.Phase
	var d ui.DisplayPhase
	if r.Mode != phaselock.ModeUnknown {
		d.ScanOut = r.Mode.String()
	}
	if r.Valid {
		d.RefreshHz, d.Locked, d.ErrorMs, d.Reported = r.RefreshHz(), r.Locked, float64(r.ErrorNs)/1e6, s.PhaseSent
	}
	return d
}

func stage(l session.Latency) ui.StageStat { return ui.StageStat{P50: l.P50, P99: l.P99} }

// endSessionLocked keeps the stream's last sample for LastSession and
// clears the live one. Hold b.mu.
func (b *backend) endSessionLocked() {
	if b.stats.Live {
		last := b.stats
		last.Live, last.Ended = false, time.Now()
		b.last = &last
	}
	b.stats = ui.SessionStats{}
	b.endUsageLocked()
}

// endUsageLocked counts the end of the stream usageStream started. Hold
// b.mu.
func (b *backend) endUsageLocked() {
	if b.usageStream != nil {
		b.usageTel.SessionEnded(*b.usageStream)
		b.usageStream = nil
	}
}

// hostSessionEvent counts a stream this machine hosts for another device.
func (b *backend) hostSessionEvent(e session.SessionEvent) {
	s := usage.Session{Mode: usage.ModeHost, Codec: e.Codec.String(), Transport: e.Path.String()}
	if e.Started {
		b.usageTel.SessionStarted(s)
	} else {
		b.usageTel.SessionEnded(s)
	}
}

func (b *backend) SessionStats() ui.SessionStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *backend) LastSession() (ui.SessionStats, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last == nil {
		return ui.SessionStats{}, false
	}
	return *b.last, true
}
