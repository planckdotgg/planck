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
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/gpusurface"

	"github.com/planckdotgg/planck/internal/rtprio"
	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/phaselock"
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

// Player decodes and presents. Submit, Format and Close are called from
// one goroutine; Resize, Stats and Timings from any.
type Player struct {
	surf   *gpusurface.Surface
	dec    decode.Decoder
	obs    Observer
	show   func(decode.Frame) error
	resize func(w, h int) error

	format        capture.Format
	width, height int

	mu sync.Mutex
	// queue holds the pictures not shown yet, oldest first: one, the
	// mailbox, or with PaceSmooth up to smoothQueue waiting for their
	// refresh.
	queue   [smoothQueue]decode.Frame
	queued  int
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

	pacing atomic.Uint32
	vsync  vsyncClock
	// now reads the clock the screen times are on.
	now func() int64
	// glassObs sees the screen times, on the platform's thread.
	glassObs atomic.Pointer[Observer]
	// backlog counts the refreshes in a row a picture was left waiting.
	backlog int
	// ready holds when the last pictures were ready to present, by PTS,
	// and phase measures them against the display's refreshes.
	ready [readyRing]struct{ pts, at int64 }
	phase phaselock.Estimator
	// phaseFrom is phaseSettle after ResetPhase last ran: pictures
	// presented before it went the old way to the screen.
	phaseFrom atomic.Int64

	hud hud
}

// New shows pictures from dec, which must decode onto surf's device (see
// Open), in surf, from a present thread of its own. It keeps the timing of
// the last keep pictures shown.
func New(surf *gpusurface.Surface, dec decode.Decoder, keep int) *Player {
	p := startWith(dec, keep, func(f decode.Frame) error { return present(surf, f) }, surf.Resize, screenNow)
	p.surf = surf
	p.hud.sink = surf
	p.setNominalPeriod(nominalPeriod(surf))
	p.phase.SetDirectSurface(surf.Direct())
	watchPresents(surf, p.onScreenGPU)
	return p
}

func start(dec decode.Decoder, keep int, show func(decode.Frame) error, resize func(w, h int) error) *Player {
	return startWith(dec, keep, show, resize, monoNow)
}

var epoch = time.Now()

func monoNow() int64 { return int64(time.Since(epoch)) }

func startWith(dec decode.Decoder, keep int, show func(decode.Frame) error, resize func(w, h int) error, now func() int64) *Player {
	p := &Player{dec: dec, show: show, resize: resize, now: now, timings: make([]Timing, 0, keep),
		wake: make(chan struct{}, 1), calls: make(chan func()), quit: make(chan struct{}), done: make(chan struct{})}
	go p.presentLoop()
	return p
}

// SetObserver sets obs to see the present thread's work. Call it before
// the first Submit.
func (p *Player) SetObserver(obs Observer) {
	p.obs = obs
	p.glassObs.Store(&obs)
}

// SetPacing chooses when pictures are shown; PaceLowestLatency at first.
func (p *Player) SetPacing(m Pacing) {
	p.pacing.Store(uint32(m))
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// readyRing bounds the pictures whose ready time is kept until they
// reach the screen.
const readyRing = 64

// onScreen takes the time a picture presented at submitted reached the
// screen, 0 when a newer one replaced it first.
func (p *Player) onScreen(pts, submitted, presented int64) {
	p.onScreenGPU(pts, submitted, 0, presented)
}

// onScreenGPU is onScreen with the time the GPU finished drawing the
// picture, 0 where the platform does not tell.
func (p *Player) onScreenGPU(pts, submitted, gpuDone, presented int64) {
	if presented == 0 {
		return
	}
	p.vsync.observe(pts, presented)
	var g phaselock.Grid
	if period, anchor, ok := p.vsync.grid(); ok {
		g = phaselock.Grid{Period: period, Anchor: anchor}
	}
	if submitted >= p.phaseFrom.Load() {
		ready := submitted
		p.mu.Lock()
		if r := p.ready[uint64(pts)%readyRing]; r.pts == pts && r.at != 0 && r.at <= submitted {
			ready = r.at
		}
		p.mu.Unlock()
		p.phase.Observe(phaselock.Sample{Frame: uint32(pts), Ready: ready, GPUDone: gpuDone, Glass: presented}, g)
	}
	if o := p.glassObs.Load(); o != nil {
		(*o).Glass(pts, time.Duration(presented-submitted), time.Duration(p.now()-presented))
		if g, ok := (*o).(GPUObserver); ok && gpuDone != 0 {
			g.GPUDone(pts, time.Duration(presented-gpuDone))
		}
	}
}

// GPUObserver is an Observer that also hears, where the platform tells
// (macOS), how long before reaching the screen the GPU finished drawing
// each picture.
type GPUObserver interface {
	GPUDone(pts int64, toGlass time.Duration)
}

// Phase reports how the last pictures that reached the screen lined up
// with its refreshes, and whether it showed them directly: see
// phaselock.Estimator. Call it at a steady rate, as a few times a second.
func (p *Player) Phase() phaselock.Report { return p.phase.Report() }

// phaseSettle is how long after a change of present path its pictures are
// not measured. Going direct at 5120x1440 on an M5 Pro, WindowServer still
// composited about half of the first 60 pictures, and next to none after.
const phaseSettle = int64(500 * time.Millisecond)

// ResetPhase starts the phase and the scan-out mode afresh, from the
// pictures presented phaseSettle after it. Call it when the surface
// changes how it reaches the screen, as gpusurface.Surface.SetDirect does.
func (p *Player) ResetPhase() {
	direct := false
	if p.surf != nil {
		p.setNominalPeriod(nominalPeriod(p.surf))
		direct = p.surf.Direct()
	}
	p.resetPhase(direct)
}

func (p *Player) resetPhase(direct bool) {
	p.phaseFrom.Store(p.now() + phaseSettle)
	p.phase.SetDirectSurface(direct)
	p.phase.Reset()
}

func (p *Player) setNominalPeriod(ns int64) {
	p.vsync.setNominal(ns)
	p.phase.SetNominalPeriod(ns)
}

// smooth reports whether pictures wait for their refresh now.
func (p *Player) smooth() bool {
	if Pacing(p.pacing.Load()) != PaceSmooth {
		return false
	}
	_, _, ok := p.vsync.deadline(p.now())
	return ok
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
	room := 1
	if p.smooth() {
		room = smoothQueue
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.dec.Release(f)
		return Timing{PTS: pts}, errClosed
	}
	var old [smoothQueue]decode.Frame
	n := 0
	for p.queued >= room {
		old[n] = p.popLocked()
		n++
	}
	p.queue[p.queued] = f
	p.queued++
	p.ready[uint64(f.PTS)%readyRing] = struct{ pts, at int64 }{f.PTS, p.now()}
	p.stats.Pictures++
	p.stats.Superseded += n
	p.mu.Unlock()
	for i := range n {
		p.dec.Release(old[i])
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return Timing{PTS: f.PTS, Decode: f.Latency}, nil
}

var errClosed = errors.New("player: closed")

// popLocked takes the oldest picture out of the queue.
func (p *Player) popLocked() decode.Frame {
	f := p.queue[0]
	copy(p.queue[:], p.queue[1:p.queued])
	p.queued--
	p.queue[p.queued] = decode.Frame{}
	return f
}

func (p *Player) presentLoop() {
	defer close(p.done)
	_ = rtprio.Boost()
	tick := time.NewTimer(time.Hour)
	tick.Stop()
	var refresh int64
	for {
		// With PaceSmooth and a picture waiting, the next present starts
		// at its refresh's deadline; otherwise at once.
		var at <-chan time.Time
		p.mu.Lock()
		waiting := p.queued
		p.mu.Unlock()
		if waiting > 0 && Pacing(p.pacing.Load()) == PaceSmooth {
			if start, r, ok := p.vsync.deadline(p.now()); ok {
				refresh = r
				tick.Reset(time.Duration(start - p.now()))
				at = tick.C
			}
		}
		select {
		case <-p.quit:
			tick.Stop()
			p.mu.Lock()
			p.closed = true
			var left [smoothQueue]decode.Frame
			n := copy(left[:], p.queue[:p.queued])
			p.queue, p.queued = [smoothQueue]decode.Frame{}, 0
			p.mu.Unlock()
			for i := range n {
				p.release(&left[i])
			}
			p.release(&p.shown)
			return
		case fn := <-p.calls:
			tick.Stop()
			fn()
		case <-at:
			p.presentOldest(refresh)
		case <-p.wake:
			// With PaceSmooth the loop sets the new picture's deadline.
			tick.Stop()
			if !p.smooth() {
				p.presentNewest()
			}
		}
	}
}

// presentNewest shows the newest picture and gives back older ones.
func (p *Player) presentNewest() {
	p.mu.Lock()
	var old [smoothQueue]decode.Frame
	n := 0
	for p.queued > 1 {
		old[n] = p.popLocked()
		n++
	}
	var f decode.Frame
	if p.queued == 1 {
		f = p.popLocked()
	}
	p.stats.Superseded += n
	p.backlog = 0
	p.mu.Unlock()
	for i := range n {
		p.dec.Release(old[i])
	}
	if f.Surface != 0 {
		p.present(f)
		p.hudResized()
	}
}

// presentOldest shows the oldest waiting picture at refresh, and drops one
// more after catchUpTicks refreshes in a row left one waiting.
func (p *Player) presentOldest(refresh int64) {
	p.mu.Lock()
	if p.queued == 0 {
		p.mu.Unlock()
		return
	}
	f := p.popLocked()
	var drop decode.Frame
	if p.queued > 0 {
		p.backlog++
		if p.backlog >= catchUpTicks {
			drop = p.popLocked()
			p.stats.Superseded++
			p.backlog = 0
		}
	} else {
		p.backlog = 0
	}
	p.mu.Unlock()
	if drop.Surface != 0 {
		p.dec.Release(drop)
	}
	p.vsync.aim(f.PTS, refresh)
	p.present(f)
	p.hudResized()
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
// closes the decoder. The surface stays open, without the HUD.
func (p *Player) Close() error {
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
	<-p.done
	p.hud.stop()
	return p.dec.Close()
}
