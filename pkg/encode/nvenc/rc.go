package nvenc

import "github.com/planckdotgg/planck/pkg/encode"

// singlePassBPP is the bits a pixel a frame from which the encoder runs
// one pass. The P1 ultra-low-latency preset runs a first pass at quarter
// resolution (multiPass 1) to place bits and find long motion vectors; on
// an RTX 4080, 5120x1440 Main10 at 120 fps and 125 Mbit/s, it took the
// encode from 1.92 to 2.60 ms p50 with the two-engine split (3.22 to 4.57
// without). LAN and Tailscale-direct rates, 0.1 bpp and up, do not need
// it; relay rates, about 0.05, keep it.
const singlePassBPP = 0.1

// singlePass reports whether cfg's stream encodes in one pass.
func singlePass(cfg encode.Config) bool {
	px := float64(cfg.Width) * float64(cfg.Height) * float64(cfg.FPS)
	return px > 0 && float64(cfg.BitrateKbps)*1000/px >= singlePassBPP
}
