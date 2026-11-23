package audio

import "sync"

// pcmRing hands PCM from a pushing writer to a device that pulls bytes. Reads
// never block and never pad: the oto mixer plays silence for a source that
// returns nothing, while padding a short read would insert silence into the
// middle of the stream. A write past max samples drops the oldest audio, so
// queued latency stays bounded when the writer's clock runs faster than the
// device's.
type pcmRing struct {
	mu      sync.Mutex
	buf     []int16
	r, n    int
	max     int
	dropped uint64 // samples
}

func newPCMRing(maxSamples int) *pcmRing {
	return &pcmRing{buf: make([]int16, maxSamples), max: maxSamples}
}

func (q *pcmRing) write(pcm []int16) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(pcm) > q.max {
		q.dropped += uint64(len(pcm) - q.max)
		pcm = pcm[len(pcm)-q.max:]
	}
	if over := q.n + len(pcm) - q.max; over > 0 {
		q.r = (q.r + over) % len(q.buf)
		q.n -= over
		q.dropped += uint64(over)
	}
	w := (q.r + q.n) % len(q.buf)
	c := copy(q.buf[w:], pcm)
	copy(q.buf, pcm[c:])
	q.n += len(pcm)
}

// read copies up to len(p)/2 queued samples into p as little-endian int16
// and returns the bytes written.
func (q *pcmRing) read(p []byte) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	have := min(len(p)/2, q.n)
	for i := 0; i < have; i++ {
		v := uint16(q.buf[(q.r+i)%len(q.buf)])
		p[2*i], p[2*i+1] = byte(v), byte(v>>8)
	}
	q.r = (q.r + have) % len(q.buf)
	q.n -= have
	return 2 * have
}
