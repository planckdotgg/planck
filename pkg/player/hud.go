package player

import (
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/gpusurface"
)

// Signal is the state of the stream's path, the colour of the HUD's dot.
type Signal uint8

const (
	SignalDirect Signal = iota + 1
	SignalRelay
	SignalFault
)

// HUDState is what the HUD pill shows.
type HUDState struct {
	Visible   bool
	Signal    Signal
	Route     string // "LAN", "Tailscale", "Relay"
	FPS       float64
	LatencyMs float64 // glass to glass p50
	Mbps      float64
	Codec     string // "PyroWave", "HEVC 10-bit", ...
}

// Pill geometry, in points, shared with the window's own HUD so that the
// two pills lie exactly on one another.
const (
	HUDPillTopPt    = 12
	HUDPillHeightPt = 32
	HUDPillRadiusPt = 16
	HUDPillPadPt    = 12
)

const (
	hudDotPt      = 8
	hudDotGapPt   = 8
	hudMaxRects   = 64
	hudTextBytes  = 128
	hudInterval   = time.Second / 4
	hudSeparator  = " · "
	hudSeparatorA = 0.45
)

var (
	hudCarbon = rgba(0x0E0F12, 0.85)
	hudBone   = rgba(0xF2EFEA, 1)
	hudEmber  = rgba(0xFF7A1F, 1)
	hudDim    = rgba(0xF2EFEA, hudSeparatorA)
	hudSignal = [...][4]float32{
		SignalDirect: rgba(0x4CD69B, 1),
		SignalRelay:  rgba(0xF2C94C, 1),
		SignalFault:  rgba(0xFF5A5A, 1),
	}
)

func rgba(rgb uint32, a float32) [4]float32 {
	return [4]float32{float32(rgb>>16&0xff) / 255, float32(rgb>>8&0xff) / 255, float32(rgb&0xff) / 255, a}
}

// overlaySink is where the HUD goes: the surface.
type overlaySink interface {
	SetOverlay([]gpusurface.OverlayRect)
	SetOverlayAtlas(pix []byte, w, h int) error
	Size() (w, h int)
	Scale() float64
}

// hud lays the pill out into a fixed set of rects, at most every
// hudInterval, coalescing the states set between.
type hud struct {
	mu      sync.Mutex
	sink    overlaySink
	state   HUDState
	font    *hudFont
	failed  bool // the sink takes no atlas
	rects   [hudMaxRects]gpusurface.OverlayRect
	text    [hudTextBytes]byte
	spans   [8]hudSpan
	last    time.Time
	timer   *time.Timer
	pending bool
	// size is the drawable size laid out for, w << 32 | h, for the present
	// thread to compare; 0 when hidden.
	size atomic.Uint64
}

// hudSpan colours text[from:to].
type hudSpan struct {
	from, to int
	color    [4]float32
}

// SetHUD shows st in the pill drawn into the frames, or hides it. A change
// of Visible shows at once; other changes at most four times a second, the
// newest winning. Call it from any goroutine.
func (p *Player) SetHUD(st HUDState) {
	h := &p.hud
	if h.sink == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	toggled := st.Visible != h.state.Visible
	h.state = st
	if h.pending && !toggled {
		return
	}
	if wait := hudInterval - time.Since(h.last); wait > 0 && !toggled {
		h.pending = true
		if h.timer == nil {
			h.timer = time.AfterFunc(wait, p.flushHUD)
		} else {
			h.timer.Reset(wait)
		}
		return
	}
	h.layout()
}

func (p *Player) flushHUD() {
	h := &p.hud
	h.mu.Lock()
	defer h.mu.Unlock()
	h.layout()
}

// hudResized lays the pill out again when the drawables changed size
// since, as when direct mode began or ended. The present thread calls it.
func (p *Player) hudResized() {
	h := &p.hud
	if h.sink == nil {
		return
	}
	w, ht := h.sink.Size()
	if laid := h.size.Load(); laid == 0 || laid == uint64(w)<<32|uint64(uint32(ht)) {
		return
	}
	h.mu.Lock()
	h.layout()
	h.mu.Unlock()
}

// stop ends the HUD's timer and takes the pill off the surface.
func (h *hud) stop() {
	if h.sink == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
	h.pending = false
	h.state.Visible = false
	h.size.Store(0)
	h.sink.SetOverlay(nil)
}

// layout runs with mu held.
func (h *hud) layout() {
	h.pending = false
	h.last = time.Now()
	if !h.state.Visible || h.failed {
		h.size.Store(0)
		h.sink.SetOverlay(nil)
		return
	}
	scale := h.sink.Scale()
	if h.font == nil || h.font.scale != scale {
		f, err := newHUDFont(scale)
		if err == nil {
			err = h.sink.SetOverlayAtlas(f.atlas, f.aw, f.ah)
		}
		if err != nil {
			h.failed = true
			h.sink.SetOverlay(nil)
			return
		}
		h.font = f
	}
	w, ht := h.sink.Size()
	h.size.Store(uint64(w)<<32 | uint64(uint32(ht)))
	h.sink.SetOverlay(h.build(w))
}

// build lays the pill out centred at the top of a drawable w pixels wide.
// It allocates nothing.
func (h *hud) build(w int) []gpusurface.OverlayRect {
	st, f, s := &h.state, h.font, h.font.scale
	text, spans := h.compose(st)
	runes := 0
	for _, c := range text {
		if utf8.RuneStart(c) {
			runes++
		}
	}
	pad, dot, gap := px(HUDPillPadPt, s), px(hudDotPt, s), px(hudDotGapPt, s)
	pillH := px(HUDPillHeightPt, s)
	pillW := pad + dot + gap + runes*f.advance + pad
	x0 := (w - pillW) / 2
	y0 := px(HUDPillTopPt, s)
	rs := h.rects[:0]
	rs = append(rs, gpusurface.OverlayRect{X: float32(x0), Y: float32(y0), W: float32(pillW), H: float32(pillH),
		Radius: float32(px(HUDPillRadiusPt, s)), Color: hudCarbon})
	sig := hudSignal[SignalFault]
	if int(st.Signal) < len(hudSignal) && st.Signal != 0 {
		sig = hudSignal[st.Signal]
	}
	rs = append(rs, gpusurface.OverlayRect{X: float32(x0 + pad), Y: float32(y0 + (pillH-dot)/2), W: float32(dot), H: float32(dot),
		Radius: float32(dot) / 2, Color: sig})
	pen := x0 + pad + dot + gap
	baseline := y0 + (pillH+f.capHeight)/2
	for i := 0; i < len(text) && len(rs) < cap(rs); {
		r, n := utf8.DecodeRune(text[i:])
		if g := f.glyph(r); g != nil && g.ok {
			color := hudBone
			for _, sp := range spans {
				if i >= sp.from && i < sp.to {
					color = sp.color
				}
			}
			rs = append(rs, gpusurface.OverlayRect{X: float32(pen + g.x), Y: float32(baseline + g.y), W: float32(g.w), H: float32(g.h),
				Color: color, UV: g.uv})
		}
		pen += f.advance
		i += n
	}
	return rs
}

// compose writes the pill's text, "LAN · 4.2 ms · 120 fps · 85 Mbps · HEVC
// 10-bit", into the fixed buffer, with the spans to colour.
func (h *hud) compose(st *HUDState) ([]byte, []hudSpan) {
	b := h.text[:0]
	spans := h.spans[:0]
	b = appendClipped(b, st.Route)
	b, spans = appendSeparator(b, spans)
	from := len(b)
	b = strconv.AppendFloat(b, finite(st.LatencyMs), 'f', 1, 64)
	spans = append(spans, hudSpan{from, len(b), hudEmber})
	b = append(b, " ms"...)
	b, spans = appendSeparator(b, spans)
	b = strconv.AppendFloat(b, math.Round(finite(st.FPS)), 'f', 0, 64)
	b = append(b, " fps"...)
	b, spans = appendSeparator(b, spans)
	prec := 0
	if st.Mbps < 10 {
		prec = 1
	}
	b = strconv.AppendFloat(b, finite(st.Mbps), 'f', prec, 64)
	b = append(b, " Mbps"...)
	if st.Codec != "" {
		b, spans = appendSeparator(b, spans)
		b = appendClipped(b, st.Codec)
	}
	return b, spans
}

func appendSeparator(b []byte, spans []hudSpan) ([]byte, []hudSpan) {
	from := len(b)
	b = append(b, hudSeparator...)
	return b, append(spans, hudSpan{from, len(b), hudDim})
}

// appendClipped appends s, at most 24 bytes of it.
func appendClipped(b []byte, s string) []byte {
	if len(s) > 24 {
		s = s[:24]
	}
	return append(b, s...)
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return min(v, 99999)
}
