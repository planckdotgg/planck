// Package player shows a stream of encoded pictures in a window: a hardware
// decoder on the window surface's own GPU device, each picture presented
// from the decoder's texture with no copy.
package player

import (
	"errors"
	"time"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
)

// Timing is one picture's way through the player.
type Timing struct {
	PTS int64
	// Decode is from Decode's call to the picture being ready; Present is
	// the time Present took to hand it to the GPU and the compositor.
	Decode, Present time.Duration
}

// Stats counts what the player did.
type Stats struct {
	Pictures int
	// NoPicture counts access units that gave no picture in time.
	NoPicture int
	// FormatChanges counts changes of format or size, the first included.
	FormatChanges int
}

// Player decodes and presents. It is not safe for concurrent use: call it
// from the thread that owns the surface.
type Player struct {
	surf *gpusurface.Surface
	dec  decode.Decoder

	shown         decode.Frame
	format        capture.Format
	width, height int

	stats   Stats
	timings []Timing
	next    int
}

// New shows pictures from dec, which must decode onto surf's device (see
// Open), in surf. It keeps the timing of the last keep pictures.
func New(surf *gpusurface.Surface, dec decode.Decoder, keep int) *Player {
	return &Player{surf: surf, dec: dec, timings: make([]Timing, 0, keep)}
}

// Open makes the platform's hardware decoder on surf's device and a player
// of it. Close the player, then the surface.
func Open(surf *gpusurface.Surface, cfg decode.Config, keep int) (*Player, error) {
	dec, err := openDecoder(surf, cfg)
	if err != nil {
		return nil, err
	}
	return New(surf, dec, keep), nil
}

// Submit decodes one access unit and presents its picture at once. The
// picture shown before it goes back to the decoder.
func (p *Player) Submit(au []byte, pts int64) (Timing, error) {
	f, err := p.dec.Decode(au, pts)
	if errors.Is(err, decode.ErrNoFrame) {
		p.stats.NoPicture++
		return Timing{PTS: pts}, err
	}
	if err != nil {
		return Timing{PTS: pts}, err
	}
	if f.Format != p.format || f.Width != p.width || f.Height != p.height {
		p.format, p.width, p.height = f.Format, f.Width, f.Height
		p.stats.FormatChanges++
	}
	start := time.Now()
	err = present(p.surf, f)
	t := Timing{PTS: f.PTS, Decode: f.Latency, Present: time.Since(start)}
	if err != nil {
		p.dec.Release(f)
		return t, err
	}
	if p.shown.Surface != 0 {
		p.dec.Release(p.shown)
	}
	p.shown = f
	p.stats.Pictures++
	p.record(t)
	return t, nil
}

func (p *Player) record(t Timing) {
	if cap(p.timings) == 0 {
		return
	}
	if len(p.timings) < cap(p.timings) {
		p.timings = append(p.timings, t)
		return
	}
	p.timings[p.next] = t
	p.next = (p.next + 1) % len(p.timings)
}

// Resize resizes the surface to w x h pixels (zero: the window's size on
// Windows) and shows the current picture again at the new size.
func (p *Player) Resize(w, h int) error {
	if err := p.surf.Resize(w, h); err != nil {
		return err
	}
	if p.shown.Surface == 0 {
		return nil
	}
	return present(p.surf, p.shown)
}

// Format is the format and visible size of the pictures shown last.
func (p *Player) Format() (capture.Format, int, int) { return p.format, p.width, p.height }

func (p *Player) Stats() Stats { return p.stats }

// Timings returns the recorded timings, oldest first.
func (p *Player) Timings() []Timing {
	out := make([]Timing, 0, len(p.timings))
	out = append(out, p.timings[p.next:]...)
	return append(out, p.timings[:p.next]...)
}

// Close gives the shown picture back and closes the decoder. The surface
// stays open.
func (p *Player) Close() error {
	if p.shown.Surface != 0 {
		p.dec.Release(p.shown)
		p.shown = decode.Frame{}
	}
	return p.dec.Close()
}
