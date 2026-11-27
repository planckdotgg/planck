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
	// now and sleepUntil stand in for Mono and SleepUntilMono in tests.
	now        func() int64
	sleepUntil func(int64)
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
	until := p.Reserve(n)
	switch {
	case until == 0:
	case p.sleepUntil != nil:
		p.sleepUntil(until)
	default:
		SleepUntilMono(until)
	}
}

// Reserve is Wait without the sleep: it books n bytes and returns the
// Mono time to sleep until before they go, or 0 to send now. A sender
// that batches packets flushes its batch before it sleeps.
func (p *Pacer) Reserve(n int) int64 {
	if p.nsPerByte == 0 {
		return 0
	}
	var now int64
	if p.now != nil {
		now = p.now()
	} else {
		now = Mono()
	}
	if p.next < now {
		p.next = now
	}
	var until int64
	if p.next-now > p.burstNs {
		until = p.next - p.burstNs/2
	}
	p.next += int64(float64(n) * p.nsPerByte)
	return until
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
	// LinkMbps, on a LAN path, is the slowest link on it: frames go out at
	// no less than linkShare of it, which only spreads them where the
	// sender's link is faster than the receiver's. Over Tailscale and
	// relays the path's capacity is not a link's; leave it 0.
	LinkMbps int
	// BurstDatagrams go back to back before pacing starts.
	BurstDatagrams int
	// Queue, when set, is the receiver's path below its socket: a frame
	// bigger than it is spread so the queue drains as it fills.
	Queue ReceiveQueue
}

// ReceiveQueue is a receiver whose path below the socket holds Bytes and
// at times drains them at no more than DrainMbps: a frame of Bytes plus x
// is spread over at least the time DrainMbps takes to carry x, or what
// does not fit is dropped.
type ReceiveQueue struct {
	Bytes, DrainMbps int
}

// MacReceiveQueue is a macOS client on wired Ethernet: a Thunderbolt Intel
// I225 (DriverKit) on an M5 Pro kept the first 127 to 158 datagrams of a
// burst and dropped the rest when its drain fell behind, and frames over
// it were lost at 2 Gbit/s but not when the excess came at about 15
// datagrams a millisecond (docs/perf/live-5k120-loss.md). 112 datagrams
// of 1452 bytes leave a margin.
var MacReceiveQueue = ReceiveQueue{Bytes: 112 * 1452, DrainMbps: 175}

// linkShare of a LAN path's slowest link is the slowest a frame goes out:
// spread over the frame interval alone, a 1080p frame of 50 KB took 2.6
// ms that line rate sends in a fifth of one.
const linkShare = 0.8

// LANPacing is DefaultPacing on a LAN whose slowest link is linkMbps, or
// 1 Gbit/s when not known.
func LANPacing(linkMbps int) Pacing {
	p := DefaultPacing
	p.LinkMbps = linkMbps
	if p.LinkMbps <= 0 {
		p.LinkMbps = 1000
	}
	return p
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
	r = max(r, pc.MinRate*float64(targetKbps)*1000/8, linkShare*float64(pc.LinkMbps)*1e6/8)
	if q := pc.Queue; q.Bytes > 0 && q.DrainMbps > 0 && wire > float64(q.Bytes) {
		spread := (wire - float64(q.Bytes)) / (float64(q.DrainMbps) * 1e6 / 8)
		r = min(r, wire/spread)
	}
	return r
}
