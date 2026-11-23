package audio

import (
	"sync"
	"testing"
	"time"
)

func pkt(seq uint16) []byte { return []byte{byte(seq >> 8), byte(seq)} }

type popResult struct {
	st  PopStatus
	seq int // -1 unless st == Packet
}

func pop(t *testing.T, j *JitterBuffer) popResult {
	t.Helper()
	var buf [MaxPacketSize]byte
	n, st := j.Pop(buf[:])
	if st != Packet {
		if n != 0 {
			t.Fatalf("status %d returned %d bytes", st, n)
		}
		return popResult{st, -1}
	}
	if n != 2 {
		t.Fatalf("packet of %d bytes, want 2", n)
	}
	return popResult{st, int(buf[0])<<8 | int(buf[1])}
}

func expect(t *testing.T, j *JitterBuffer, want ...popResult) {
	t.Helper()
	for i, w := range want {
		if got := pop(t, j); got != w {
			t.Fatalf("pop %d = %+v, want %+v", i, got, w)
		}
	}
}

func p(seq int) popResult { return popResult{Packet, seq} }

var (
	buffering = popResult{Buffering, -1}
	missing   = popResult{Missing, -1}
)

func TestJitterBuffersToTargetThenPlaysInOrder(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 20 * time.Millisecond})
	expect(t, j, buffering)
	j.Push(100, pkt(100))
	expect(t, j, buffering)
	j.Push(101, pkt(101))
	expect(t, j, p(100))
	j.Push(102, pkt(102))
	expect(t, j, p(101), p(102))
	if st := j.Stats(); st.Received != 3 || st.Concealment != 0 || st.Target != 20*time.Millisecond {
		t.Fatalf("stats %+v", st)
	}
}

func TestJitterReordersOutOfOrderPackets(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 40 * time.Millisecond})
	for _, s := range []uint16{0, 2, 1, 3} {
		j.Push(s, pkt(s))
	}
	expect(t, j, p(0), p(1), p(2), p(3))
}

func TestJitterConcealsLostPacketAndCountsLateArrival(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 20 * time.Millisecond, MaxDepth: 80 * time.Millisecond})
	for _, s := range []uint16{0, 1, 3, 4} {
		j.Push(s, pkt(s))
	}
	expect(t, j, p(0), p(1), missing)
	j.Push(2, pkt(2))
	expect(t, j, p(3), p(4))
	st := j.Stats()
	if st.Lost != 1 || st.Late != 1 || st.Underruns != 0 {
		t.Fatalf("stats %+v, want Lost=1 Late=1", st)
	}
}

func TestJitterDropsDuplicates(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond})
	j.Push(7, pkt(7))
	j.Push(7, pkt(7))
	expect(t, j, p(7))
	j.Push(7, pkt(7))
	st := j.Stats()
	if st.Duplicate != 1 || st.Late != 1 {
		t.Fatalf("stats %+v, want Duplicate=1 Late=1", st)
	}
}

func TestJitterUnderrunHoldsSequenceAndRaisesTarget(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond})
	j.Push(0, pkt(0))
	expect(t, j, p(0), missing)
	j.Push(1, pkt(1))
	expect(t, j, p(1))
	st := j.Stats()
	if st.Underruns != 1 || st.Lost != 0 || st.Late != 0 {
		t.Fatalf("stats %+v, want one underrun and the late packet played", st)
	}
	if st.Target != 20*time.Millisecond {
		t.Fatalf("target %v after underrun, want 20ms", st.Target)
	}
}

func TestJitterTargetStaysWithinBounds(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{MinDepth: 10 * time.Millisecond, TargetDepth: 20 * time.Millisecond, MaxDepth: 40 * time.Millisecond})
	seq := uint16(0)
	for range 10 {
		for range 5 {
			j.Push(seq, pkt(seq))
			seq++
		}
		for range 5 {
			pop(t, j)
		}
		pop(t, j)
	}
	if st := j.Stats(); st.Target != 40*time.Millisecond {
		t.Fatalf("target %v after repeated underruns, want capped at 40ms", st.Target)
	}
	for range 3 * jitterDecaySlots {
		j.Push(seq, pkt(seq))
		seq++
		pop(t, j)
	}
	if st := j.Stats(); st.Target != 10*time.Millisecond {
		t.Fatalf("target %v after a quiet stretch, want floor 10ms", st.Target)
	}
}

func TestJitterLongStallRebuffers(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 20 * time.Millisecond, MaxDepth: 40 * time.Millisecond})
	j.Push(0, pkt(0))
	j.Push(1, pkt(1))
	expect(t, j, p(0), p(1), missing, missing, missing, missing, missing, buffering)
	j.Push(2, pkt(2))
	expect(t, j, buffering)
}

func TestJitterOverrunCutsBackToTarget(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 20 * time.Millisecond, MaxDepth: 40 * time.Millisecond})
	for s := uint16(0); s < 5; s++ {
		j.Push(s, pkt(s))
	}
	st := j.Stats()
	if st.Overruns != 1 || st.Dropped != 3 || st.Depth != 20*time.Millisecond {
		t.Fatalf("stats %+v, want one overrun dropping 3 to reach 20ms", st)
	}
	expect(t, j, p(3), p(4))
}

func TestJitterTrimsStandingQueue(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond, MaxDepth: 40 * time.Millisecond})
	seq := uint16(0)
	for range 3 {
		j.Push(seq, pkt(seq))
		seq++
	}
	for range jitterTrimSlots + 1 {
		j.Push(seq, pkt(seq))
		seq++
		if got := pop(t, j); got.st != Packet {
			t.Fatalf("pop = %+v", got)
		}
	}
	if st := j.Stats(); st.Dropped != 1 || st.Depth != 20*time.Millisecond {
		t.Fatalf("stats %+v, want one frame trimmed from a 30ms standing queue", st)
	}
}

func TestJitterSequenceWraps(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 20 * time.Millisecond})
	for _, s := range []uint16{65534, 0, 65535, 1} {
		j.Push(s, pkt(s))
	}
	expect(t, j, p(65534), p(65535), p(0), p(1))
}

func TestJitterResetsOnLargeJumpEitherWay(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond})
	j.Push(10, pkt(10))
	expect(t, j, p(10))
	j.Push(5000, pkt(5000))
	expect(t, j, p(5000))
	j.Push(3, pkt(3))
	expect(t, j, p(3))
	if st := j.Stats(); st.Resets != 2 || st.Late != 0 {
		t.Fatalf("stats %+v, want a reset for each jump and nothing late", st)
	}
}

func TestJitterIgnoresBadPayloads(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond})
	j.Push(0, nil)
	j.Push(0, make([]byte, MaxPacketSize+1))
	if st := j.Stats(); st.Received != 0 || st.Depth != 0 {
		t.Fatalf("stats %+v, want bad payloads ignored", st)
	}
}

func TestJitterConcurrentPushPop(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for s := range uint16(2000) {
			j.Push(s, pkt(s))
		}
	}()
	go func() {
		defer wg.Done()
		var buf [MaxPacketSize]byte
		for range 2000 {
			j.Pop(buf[:])
		}
	}()
	wg.Wait()
	if st := j.Stats(); st.Received != 2000 {
		t.Fatalf("received %d, want 2000", st.Received)
	}
}

func TestPlayoutConcealsThenFadesToSilence(t *testing.T) {
	enc, _ := NewEncoder(EncoderConfig{})
	dec, _ := NewDecoder(0)
	j := NewJitterBuffer(JitterConfig{TargetDepth: 10 * time.Millisecond, MaxDepth: 80 * time.Millisecond})
	po := NewPlayout(j, dec)
	n := enc.FrameSamples()
	in := make([]int16, n*Channels)
	out := make([]int16, n*Channels)
	packet := make([]byte, MaxPacketSize)
	energy := func() (e float64) {
		for _, v := range out {
			e += float64(v) * float64(v)
		}
		return e
	}
	if st, err := po.Next(out); st != Buffering || err != nil || energy() != 0 {
		t.Fatalf("empty playout = %v, %v, energy %v; want silent Buffering", st, err, energy())
	}
	for f := 0; f < 10; f++ {
		sineFrame(in, f*n, 1000, 1000)
		size, _ := enc.Encode(in, packet)
		j.Push(uint16(f), packet[:size])
		if st, err := po.Next(out); st != Packet || err != nil {
			t.Fatalf("frame %d = %v, %v", f, st, err)
		}
	}
	var energies []float64
	var head, tail float64
	for i := range concealFrames + 2 {
		st, err := po.Next(out)
		if st != Missing || err != nil {
			t.Fatalf("missing frame = %v, %v", st, err)
		}
		energies = append(energies, energy())
		if i == concealFrames {
			for k := 0; k < n/4; k++ {
				head += float64(out[k*Channels]) * float64(out[k*Channels])
				tail += float64(out[(n-1-k)*Channels]) * float64(out[(n-1-k)*Channels])
			}
		}
	}
	if energies[0] == 0 {
		t.Error("first concealed frame is silent; want Opus concealment")
	}
	if last := energies[len(energies)-1]; last != 0 {
		t.Errorf("frame after the fade has energy %v, want silence", last)
	}
	if head == 0 || tail > head*0.1 {
		t.Errorf("fade frame tail energy %v vs head %v; want a ramp to zero", tail, head)
	}
}
