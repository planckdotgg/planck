package transport

import (
	"slices"
	"testing"
	"time"
)

const pacedDatagram = 1452 // wire.MaxLANDatagram

// paceTrace sends size bytes as datagrams through p and returns when each
// one was let go.
func paceTrace(p *Pacer, size int) []int64 {
	at := make([]int64, 0, size/pacedDatagram+1)
	for sent := 0; sent < size; sent += pacedDatagram {
		p.Wait(pacedDatagram)
		at = append(at, Mono())
	}
	return at
}

// maxInWindow is the most bytes let go within any window of w ns.
func maxInWindow(at []int64, w int64) int {
	best, lo := 0, 0
	for hi := range at {
		for at[hi]-at[lo] > w {
			lo++
		}
		best = max(best, (hi-lo+1)*pacedDatagram)
	}
	return best
}

// TestPacingSpreadsFrames: a 5120x1440 frame at 120 fps leaves over about
// its share of the frame interval, in trains no longer than the burst,
// and a keyframe over most of the interval. Unpaced, the whole frame
// left at once.
func TestPacingSpreadsFrames(t *testing.T) {
	const fps, targetKbps = 120, 212_000
	interval := time.Second / fps
	burst := DefaultPacing.BurstDatagrams * pacedDatagram
	for _, tc := range []struct {
		name  string
		size  int
		key   bool
		share float64
	}{
		{"P frame 220 KB", 220_000, false, DefaultPacing.Share},
		{"keyframe 700 KB", 700_000, true, DefaultPacing.KeyShare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rate := DefaultPacing.rate(tc.size, tc.key, fps, targetKbps)
			if due := time.Duration(float64(tc.size) * 1.1 / rate * 1e9); due > time.Duration(tc.share*float64(interval))+time.Microsecond {
				t.Fatalf("paced over %v, more than the %.0f%% share of %v", due, tc.share*100, interval)
			}
			var p Pacer
			p.SetRate(rate, burst)
			// A frame's datagrams stand for 1.1 times its size on the wire,
			// and the first burst leaves at once.
			n := (tc.size + pacedDatagram - 1) / pacedDatagram
			want := time.Duration(float64(n*pacedDatagram-burst) / rate * 1e9)
			var spreads []time.Duration
			for round := 0; round < 20; round++ {
				at := paceTrace(&p, tc.size)
				spread := time.Duration(at[len(at)-1] - at[0])
				// A preempted round only runs long; none may run short.
				if spread < want*7/10 {
					t.Fatalf("round %d: %d datagrams over %v, want about %v", round, len(at), spread, want)
				}
				// In any 200 us: one burst plus what the rate lets out, and
				// a train of slack for a late wake.
				w := 200 * time.Microsecond
				limit := burst + int(rate*w.Seconds()) + burst/2
				if got := maxInWindow(at, int64(w)); got > limit {
					t.Fatalf("round %d: %d bytes within %v, limit %d", round, got, w, limit)
				}
				spreads = append(spreads, spread)
				SleepUntilMono(Mono() + int64(interval))
			}
			slices.Sort(spreads)
			median, worst := spreads[len(spreads)/2], spreads[len(spreads)-1]
			if median > want*3/2 {
				t.Fatalf("median of 20 frames spread over %v, want about %v", median, want)
			}
			t.Logf("%d bytes at %.0f Mbit/s: spread median %v, worst of 20 %v (want %v); median share of %v: %.0f%%",
				tc.size, rate*8/1e6, median, worst, want, interval, 100*float64(median)/float64(interval))
		})
	}
}

func TestPacerDoesNotAllocate(t *testing.T) {
	var p Pacer
	p.SetRate(1e9, 16*pacedDatagram)
	if a := testing.AllocsPerRun(5000, func() { p.Wait(pacedDatagram) }); a != 0 {
		t.Fatalf("Wait allocates %.2f per datagram", a)
	}
}

func TestPacingUnpacedWithoutFPS(t *testing.T) {
	if r := DefaultPacing.rate(200_000, false, 0, 100_000); r != 0 {
		t.Fatalf("rate %v with no frame rate, want unpaced", r)
	}
	if r := (Pacing{}).rate(200_000, false, 120, 100_000); r != 0 {
		t.Fatalf("zero Pacing paces at %v", r)
	}
	small := DefaultPacing.rate(1000, false, 120, 100_000)
	if floor := DefaultPacing.MinRate * 100_000 * 1000 / 8; small != floor {
		t.Fatalf("small frame paced at %v, want the %v floor", small, floor)
	}
}

// BenchmarkPacerWait is one datagram through a pacer at 2 Gbit/s: the
// per-datagram cost of pacing, waits included.
func BenchmarkPacerWait(b *testing.B) {
	var p Pacer
	p.SetRate(2e9/8, 16*pacedDatagram)
	b.ReportAllocs()
	b.SetBytes(pacedDatagram)
	for i := 0; i < b.N; i++ {
		p.Wait(pacedDatagram)
	}
}
