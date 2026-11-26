package transport

// Pacer spaces packets at a byte rate, letting up to a burst of bytes go
// back to back. A zero rate sends every packet immediately. Not safe for
// concurrent use; it does not allocate.
type Pacer struct {
	nsPerByte float64
	burstNs   int64
	// next is when the bytes sent so far are due at the rate: how far it
	// runs ahead of now is the burst in flight.
	next int64
}

func NewPacer(bytesPerSec float64) *Pacer {
	p := &Pacer{}
	p.SetRate(bytesPerSec, 0)
	return p
}

// SetRate changes the rate and the burst; packets already sent keep their
// place.
func (p *Pacer) SetRate(bytesPerSec float64, burstBytes int) {
	if bytesPerSec <= 0 {
		p.nsPerByte, p.burstNs = 0, 0
		return
	}
	p.nsPerByte = 1e9 / bytesPerSec
	p.burstNs = int64(float64(burstBytes) * p.nsPerByte)
}

// Wait blocks until a packet of n bytes may go out. Past a burst it sleeps
// until half a burst is due, so packets leave in short trains a few tens
// of microseconds apart: one per packet would spin a core for the whole
// frame, as no OS sleeps that briefly. A sender that fell behind (a late
// wake, a gap between frames) starts again from now, not with a catch-up
// burst.
func (p *Pacer) Wait(n int) {
	if p.nsPerByte == 0 {
		return
	}
	now := Mono()
	if p.next < now {
		p.next = now
	}
	if p.next-now > p.burstNs {
		SleepUntilMono(p.next - p.burstNs/2)
	}
	p.next += int64(float64(n) * p.nsPerByte)
}

// Pacing spreads each video frame's datagrams over a share of the frame
// interval, so a frame does not reach the NIC, the switch and the
// receiver's socket as one burst at line rate. The zero value sends
// unpaced.
type Pacing struct {
	// Share and KeyShare are the parts of the frame interval a P frame
	// and a keyframe are spread over. A keyframe is several times larger:
	// at the same share it would leave at several times the rate.
	Share, KeyShare float64
	// MinRate is a floor on the pacing rate, as a multiple of the rate
	// controller's target, so small frames are not stretched out.
	MinRate float64
	// BurstDatagrams go back to back before pacing starts.
	BurstDatagrams int
}

// DefaultPacing adds a P frame at most a third of its interval (2.8 ms at
// 120 fps), most of it hidden behind the next frame's encode; a 16
// datagram train is about 23 KB, under 0.2 ms at 1 Gbit/s.
var DefaultPacing = Pacing{Share: 0.35, KeyShare: 0.8, MinRate: 1.5, BurstDatagrams: 16}

// rate is the pacing rate in bytes a second for a frame of size bytes at
// fps, with the rate controller at targetKbps. Zero means unpaced.
func (pc Pacing) rate(size int, keyframe bool, fps, targetKbps int) float64 {
	share := pc.Share
	if keyframe {
		share = pc.KeyShare
	}
	if share <= 0 || fps <= 0 {
		return 0
	}
	// Parity and headers add about a tenth on the wire.
	wire := float64(size) * 1.1
	r := wire * float64(fps) / share
	return max(r, pc.MinRate*float64(targetKbps)*1000/8)
}
