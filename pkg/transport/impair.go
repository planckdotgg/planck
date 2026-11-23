package transport

import (
	"sync"
	"time"
)

// Impairment simulates a lossy, jittery path in front of a send function.
type Impairment struct {
	Loss   float64
	Jitter time.Duration
	Seed   uint64
}

const delayLineLen = 4096

type delayed struct {
	buf     []byte
	n       int
	release int64
}

// Impairer drops packets at random and, when Jitter is set, delays the rest
// by up to Jitter in FIFO order. With jitter, send runs on the Impairer's own
// goroutine; without it, on the caller's.
type Impairer struct {
	cfg     Impairment
	send    func([]byte) error
	rng     uint64
	lossCut uint64

	Dropped uint64
	Sent    uint64

	slots   []delayed
	head    int
	queue   chan int
	free    chan int
	last    int64
	wg      sync.WaitGroup
	sendErr error
}

func NewImpairer(cfg Impairment, bufLen int, send func([]byte) error) *Impairer {
	seed := cfg.Seed
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	im := &Impairer{cfg: cfg, send: send, rng: seed}
	if cfg.Loss > 0 {
		im.lossCut = uint64(cfg.Loss * float64(1<<53))
	}
	if cfg.Jitter > 0 {
		im.slots = make([]delayed, delayLineLen)
		im.queue = make(chan int, delayLineLen)
		im.free = make(chan int, delayLineLen)
		for i := range im.slots {
			im.slots[i].buf = make([]byte, bufLen)
			im.free <- i
		}
		im.wg.Add(1)
		go im.run()
	}
	return im
}

func (im *Impairer) next() uint64 {
	im.rng ^= im.rng << 13
	im.rng ^= im.rng >> 7
	im.rng ^= im.rng << 17
	return im.rng
}

// Send forwards pkt, drops it, or queues a copy for delayed delivery.
func (im *Impairer) Send(pkt []byte) error {
	if im.lossCut > 0 && im.next()>>11 < im.lossCut {
		im.Dropped++
		return nil
	}
	im.Sent++
	if im.queue == nil {
		return im.send(pkt)
	}
	i := <-im.free
	s := &im.slots[i]
	s.buf = s.buf[:cap(s.buf)]
	s.n = copy(s.buf, pkt)
	release := Mono() + int64(im.next()%uint64(im.cfg.Jitter))
	if release < im.last {
		release = im.last
	}
	im.last = release
	s.release = release
	im.queue <- i
	return nil
}

func (im *Impairer) run() {
	defer im.wg.Done()
	for i := range im.queue {
		s := &im.slots[i]
		SleepUntilMono(s.release)
		if err := im.send(s.buf[:s.n]); err != nil && im.sendErr == nil {
			im.sendErr = err
		}
		im.free <- i
	}
}

// Close drains the delay line and returns the first delayed send error.
func (im *Impairer) Close() error {
	if im.queue != nil {
		close(im.queue)
		im.wg.Wait()
	}
	return im.sendErr
}
