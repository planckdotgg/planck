// Package player shows a stream of encoded pictures in a window: a hardware
// decoder on the window surface's own GPU device, each picture presented
// from the decoder's texture with no copy.
//
// Decoding and presenting run on different threads. Submit decodes every
// access unit on the caller's goroutine, so reference pictures stay
// intact, and leaves the picture in a mailbox of one; the player's present
// thread shows the newest picture in it. A present that waits for a
// drawable never holds up decoding, and a picture superseded before it was
// shown goes straight back to the decoder instead of queueing.
package player

import (
	"errors"
	"sync"
	"time"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/rtprio"
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
	// Pictures counts the pictures decoded.
	Pictures int
	// NoPicture counts access units that gave no picture in time.
	NoPicture int
	// FormatChanges counts changes of format or size, the first included.
	FormatChanges int
	// Presented counts the pictures shown; Superseded those a newer
	// picture replaced in the mailbox before they were shown.
	Presented, Superseded int
	PresentErrors         int
}

// Observer sees the present thread's work. Its methods must return quickly.
type Observer interface {
	// Presented runs on the present thread after each present, with the
	// present's error.
	Presented(t Timing, err error)
	// Glass runs when a presented picture reached the screen, on the
	// platform's thread: glass is from the present call to the screen,
	// since how long before this call that was. Only macOS reports it,
	// and not for pictures a newer one replaced first.
	Glass(pts int64, glass, since time.Duration)
}

// Player decodes and presents. Submit, Format, Resize and Close are called
// from one goroutine; Stats and Timings from any.
type Player struct {
	surf   *gpusurface.Surface
	dec    decode.Decoder
	obs    Observer
	show   func(decode.Frame) error
	resize func(w, h int) error

	format        capture.Format
	width, height int

	mu      sync.Mutex
	mailbox decode.Frame
	closed  bool
	stats   Stats
	timings []Timing
	next    int

	wake  chan struct{}
	calls chan func()
	quit  chan struct{}
	done  chan struct{}

	// shown is the present thread's: the picture on screen, kept from the
	// decoder until a newer one replaces it.
	shown decode.Frame
}

// New shows pictures from dec, which must decode onto surf's device (see
// Open), in surf, from a present thread of its own. It keeps the timing of
// the last keep pictures shown.
func New(surf *gpusurface.Surface, dec decode.Decoder, keep int) *Player {
	p := start(dec, keep, func(f decode.Frame) error { return present(surf, f) }, surf.Resize)
	p.surf = surf
	return p
}

func start(dec decode.Decoder, keep int, show func(decode.Frame) error, resize func(w, h int) error) *Player {
	p := &Player{dec: dec, show: show, resize: resize, timings: make([]Timing, 0, keep),
		wake: make(chan struct{}, 1), calls: make(chan func()), quit: make(chan struct{}), done: make(chan struct{})}
	go p.presentLoop()
	return p
}

// SetObserver sets obs to see the present thread's work. Call it before
// the first Submit.
func (p *Player) SetObserver(obs Observer) {
	p.obs = obs
	if p.surf != nil {
		observeGlass(p.surf, obs)
	}
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

// Submit decodes one access unit and leaves its picture for the present
// thread, giving back a picture still waiting there unshown. Its Timing
// has no Present: the Observer sees that.
func (p *Player) Submit(au []byte, pts int64) (Timing, error) {
	f, err := p.dec.Decode(au, pts)
	if err != nil {
		if errors.Is(err, decode.ErrNoFrame) {
			p.mu.Lock()
			p.stats.NoPicture++
			p.mu.Unlock()
		}
		return Timing{PTS: pts}, err
	}
	if f.Format != p.format || f.Width != p.width || f.Height != p.height {
		p.format, p.width, p.height = f.Format, f.Width, f.Height
		p.mu.Lock()
		p.stats.FormatChanges++
		p.mu.Unlock()
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.dec.Release(f)
		return Timing{PTS: pts}, errClosed
	}
	old := p.mailbox
	p.mailbox = f
	p.stats.Pictures++
	if old.Surface != 0 {
		p.stats.Superseded++
	}
	p.mu.Unlock()
	if old.Surface != 0 {
		p.dec.Release(old)
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return Timing{PTS: f.PTS, Decode: f.Latency}, nil
}

var errClosed = errors.New("player: closed")

func (p *Player) presentLoop() {
	defer close(p.done)
	_ = rtprio.Boost()
	for {
		select {
		case <-p.quit:
			p.mu.Lock()
			p.closed = true
			f := p.mailbox
			p.mailbox = decode.Frame{}
			p.mu.Unlock()
			p.release(&f)
			p.release(&p.shown)
			return
		case fn := <-p.calls:
			fn()
		case <-p.wake:
			p.mu.Lock()
			f := p.mailbox
			p.mailbox = decode.Frame{}
			p.mu.Unlock()
			if f.Surface != 0 {
				p.present(f)
			}
		}
	}
}

func (p *Player) present(f decode.Frame) {
	start := time.Now()
	err := p.show(f)
	t := Timing{PTS: f.PTS, Decode: f.Latency, Present: time.Since(start)}
	p.mu.Lock()
	if err != nil {
		p.stats.PresentErrors++
	} else {
		p.stats.Presented++
		p.record(t)
	}
	p.mu.Unlock()
	if err != nil {
		p.dec.Release(f)
	} else {
		p.release(&p.shown)
		p.shown = f
	}
	if p.obs != nil {
		p.obs.Presented(t, err)
	}
}

func (p *Player) release(f *decode.Frame) {
	if f.Surface != 0 {
		p.dec.Release(*f)
		*f = decode.Frame{}
	}
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
// Windows) on the present thread and shows the current picture again at
// the new size. It returns once done.
func (p *Player) Resize(w, h int) error {
	errc := make(chan error, 1)
	fn := func() {
		err := p.resize(w, h)
		if err == nil && p.shown.Surface != 0 {
			err = p.show(p.shown)
		}
		errc <- err
	}
	select {
	case p.calls <- fn:
		return <-errc
	case <-p.done:
		return errClosed
	}
}

// Format is the format and visible size of the pictures decoded last.
func (p *Player) Format() (capture.Format, int, int) { return p.format, p.width, p.height }

func (p *Player) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Timings returns the recorded timings, oldest first.
func (p *Player) Timings() []Timing {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Timing, 0, len(p.timings))
	out = append(out, p.timings[p.next:]...)
	return append(out, p.timings[:p.next]...)
}

// Close stops the present thread, gives every picture it holds back and
// closes the decoder. The surface stays open.
func (p *Player) Close() error {
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
	<-p.done
	return p.dec.Close()
}
