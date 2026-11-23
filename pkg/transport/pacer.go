package transport

// Pacer spaces packets at a fixed byte rate. A zero rate sends every packet
// immediately (burst). Not safe for concurrent use.
type Pacer struct {
	nsPerByte float64
	next      int64
}

func NewPacer(bytesPerSec float64) *Pacer {
	p := &Pacer{}
	if bytesPerSec > 0 {
		p.nsPerByte = 1e9 / bytesPerSec
	}
	return p
}

// Wait blocks until a packet of n bytes may go out.
func (p *Pacer) Wait(n int) {
	if p.nsPerByte == 0 {
		return
	}
	if now := Mono(); p.next < now {
		p.next = now
	}
	SleepUntilMono(p.next)
	p.next += int64(float64(n) * p.nsPerByte)
}
