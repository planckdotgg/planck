package transport

import (
	"bytes"
	"math/rand/v2"
	"testing"
	"time"
)

const testShard = 1152

type capturedPkt struct {
	plain []byte
	h     MediaHeader
}

func packetizeAll(t *testing.T, p *Packetizer, id uint32, frame []byte, slices, pct int) []capturedPkt {
	return packetizeWith(t, p, id, frame, slices, FixedParity(pct))
}

func packetizeWith(t *testing.T, p *Packetizer, id uint32, frame []byte, slices int, policy ParityPolicy) []capturedPkt {
	t.Helper()
	var out []capturedPkt
	err := p.Packetize(id, frame, slices, policy, func(pkt []byte) error {
		c := capturedPkt{plain: append([]byte(nil), pkt[AEADHeaderLen:]...)}
		if err := c.h.Parse(c.plain); err != nil {
			t.Fatalf("emitted header does not parse: %v", err)
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func randomFrame(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func frameFrom(t *testing.T, r *Reassembler, id uint32, slices int) []byte {
	t.Helper()
	var got []byte
	for s := 0; s < slices; s++ {
		d := r.SliceData(id, s)
		if d == nil {
			t.Fatalf("frame %d slice %d not ready", id, s)
		}
		got = append(got, d...)
	}
	return got
}

func TestSliceReassemblyInOrderNoFEC(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	frame := randomFrame(r, 78125)
	pkts := packetizeAll(t, NewPacketizer(testShard), 7, frame, 4, 0)

	ra := NewReassembler(testShard, nil)
	var readyOrder []int
	complete := 0
	for _, p := range pkts {
		res, err := ra.Push(p.plain)
		if err != nil {
			t.Fatal(err)
		}
		if res.SliceReady {
			readyOrder = append(readyOrder, res.SliceIndex)
			if res.SliceRecovered {
				t.Fatal("no shard lost, but slice reported recovered")
			}
		}
		if res.FrameComplete {
			complete++
		}
	}
	if complete != 1 || len(readyOrder) != 4 || readyOrder[0] != 0 {
		t.Fatalf("complete=%d readyOrder=%v", complete, readyOrder)
	}
	if !bytes.Equal(frameFrom(t, ra, 7, 4), frame) {
		t.Fatal("reassembled frame differs")
	}
}

func TestFirstSliceReadyBeforeFrameComplete(t *testing.T) {
	frame := randomFrame(rand.New(rand.NewPCG(3, 4)), 40000)
	pkts := packetizeAll(t, NewPacketizer(testShard), 1, frame, 4, 0)
	ra := NewReassembler(testShard, nil)
	for i, p := range pkts {
		res, _ := ra.Push(p.plain)
		if res.SliceReady && res.SliceIndex == 0 {
			if i >= len(pkts)/4+1 {
				t.Fatalf("slice 0 ready only after packet %d of %d", i, len(pkts))
			}
			return
		}
	}
	t.Fatal("slice 0 never ready")
}

func TestFECRecoversUpToParityLossPerSlice(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, tc := range []struct{ slices, pct, size int }{
		{1, 10, 52083}, {1, 20, 156250}, {4, 10, 78125}, {4, 20, 26041}, {8, 20, 78125},
	} {
		frame := randomFrame(r, tc.size)
		pkts := packetizeAll(t, NewPacketizer(testShard), 42, frame, tc.slices, tc.pct)
		r.Shuffle(len(pkts), func(i, j int) { pkts[i], pkts[j] = pkts[j], pkts[i] })

		dropped := map[[2]int]int{}
		var stats []FrameStats
		ra := NewReassembler(testShard, func(s FrameStats) { stats = append(stats, s) })
		recovered := 0
		for _, p := range pkts {
			key := [2]int{int(p.h.SliceIndex)}
			if int(p.h.ShardIndex) < int(p.h.DataShards) && dropped[key] < int(p.h.ParityShards) {
				dropped[key]++
				continue
			}
			res, err := ra.Push(p.plain)
			if err != nil {
				t.Fatal(err)
			}
			if res.SliceRecovered {
				recovered++
			}
		}
		if recovered != tc.slices {
			t.Fatalf("%+v: %d slices recovered, want %d", tc, recovered, tc.slices)
		}
		if !bytes.Equal(frameFrom(t, ra, 42, tc.slices), frame) {
			t.Fatalf("%+v: recovered frame differs", tc)
		}
		ra.Flush()
		if len(stats) != 1 || !stats[0].Complete || stats[0].SlicesRecovered != tc.slices ||
			stats[0].SentShards != len(pkts) || stats[0].RecvShards >= len(pkts) {
			t.Fatalf("%+v: stats %+v (sent %d)", tc, stats, len(pkts))
		}
	}
}

func TestFECFailsBeyondParity(t *testing.T) {
	frame := randomFrame(rand.New(rand.NewPCG(7, 8)), 30000)
	pkts := packetizeAll(t, NewPacketizer(testShard), 9, frame, 2, 10)
	var stats []FrameStats
	ra := NewReassembler(testShard, func(s FrameStats) { stats = append(stats, s) })
	drop := int(pkts[0].h.ParityShards) + 1
	for _, p := range pkts {
		if p.h.SliceIndex == 1 && int(p.h.ShardIndex) < drop {
			continue
		}
		res, _ := ra.Push(p.plain)
		if res.FrameComplete {
			t.Fatal("frame completed with more losses than parity")
		}
	}
	if ra.SliceData(9, 0) == nil || ra.SliceData(9, 1) != nil {
		t.Fatal("expected slice 0 ready and slice 1 lost")
	}
	ra.Flush()
	if len(stats) != 1 || stats[0].Complete || stats[0].SlicesLost != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestReassemblerWindowFinalizesAndDropsLate(t *testing.T) {
	p := NewPacketizer(testShard)
	frame := randomFrame(rand.New(rand.NewPCG(9, 10)), 5000)
	var finals []uint32
	ra := NewReassembler(testShard, func(s FrameStats) { finals = append(finals, s.FrameID) })
	first := packetizeAll(t, p, 1, frame, 1, 0)
	ra.Push(first[0].plain)
	for id := uint32(2); id <= 1+ReassemblyWindow; id++ {
		for _, c := range packetizeAll(t, p, id, frame, 1, 0) {
			ra.Push(c.plain)
		}
	}
	if len(finals) != 1 || finals[0] != 1 {
		t.Fatalf("finalized %v, want [1]", finals)
	}
	if res, _ := ra.Push(first[1].plain); res.SliceReady || ra.Late != 1 {
		t.Fatalf("late shard accepted: %+v late=%d", res, ra.Late)
	}
}

func TestReassemblerRejectsMalformed(t *testing.T) {
	frame := randomFrame(rand.New(rand.NewPCG(11, 12)), 5000)
	pkts := packetizeAll(t, NewPacketizer(testShard), 1, frame, 2, 20)
	ra := NewReassembler(testShard, nil)
	bad := append([]byte(nil), pkts[0].plain...)
	bad[3] = 250
	if _, err := ra.Push(bad); err == nil {
		t.Fatal("shard index beyond total accepted")
	}
	if _, err := ra.Push(pkts[0].plain[:100]); err == nil {
		t.Fatal("truncated packet accepted")
	}
	if ra.Invalid != 2 {
		t.Fatalf("invalid = %d", ra.Invalid)
	}
}

// Reed-Solomon encode/reconstruct allocate inside klauspost/reedsolomon
// (pool boxing, index slices); BenchmarkSliceFEC reports that cost. Planck's
// own packetize/reassemble/AEAD path must stay at zero.
func TestPacketizeSealOpenReassembleZeroAlloc(t *testing.T) {
	frame := randomFrame(rand.New(rand.NewPCG(13, 14)), 78125)
	k := testKey(t, SuiteAES128GCM)
	seal, _ := NewSealer(k)
	open, _ := NewOpener(k)
	p := NewPacketizer(testShard)
	ra := NewReassembler(testShard, func(FrameStats) {})
	id := uint32(0)
	emit := func(pkt []byte) error {
		wire := seal.Seal(pkt, pkt[AEADHeaderLen:])
		plain, err := open.Open(wire)
		if err != nil {
			return err
		}
		_, err = ra.Push(plain)
		return err
	}
	for i := 0; i < 20; i++ {
		id++
		p.Packetize(id, frame, 4, FixedParity(0), emit)
	}
	allocs := testing.AllocsPerRun(200, func() {
		id++
		if err := p.Packetize(id, frame, 4, FixedParity(0), emit); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%.2f allocs per frame, want 0", allocs)
	}
}

func BenchmarkSliceFEC(b *testing.B) {
	frame := randomFrame(rand.New(rand.NewPCG(13, 14)), 78125)
	for _, bc := range []struct {
		name      string
		slices    int
		pct, drop int
	}{
		{"whole/fec0", 1, 0, 0}, {"slice4/fec0", 4, 0, 0},
		{"slice4/fec20/noloss", 4, 20, 0}, {"slice4/fec20/loss6pct", 4, 20, 17},
		{"whole/fec20/loss6pct", 1, 20, 17},
	} {
		b.Run(bc.name, func(b *testing.B) {
			p := NewPacketizer(testShard)
			ra := NewReassembler(testShard, func(FrameStats) {})
			id, n := uint32(0), 0
			emit := func(pkt []byte) error {
				n++
				if bc.drop > 0 && n%bc.drop == 0 {
					return nil
				}
				_, err := ra.Push(pkt[AEADHeaderLen:])
				return err
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			for i := 0; i < b.N; i++ {
				id++
				if err := p.Packetize(id, frame, bc.slices, FixedParity(bc.pct), emit); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAdaptiveParityTracksLoss(t *testing.T) {
	a := &AdaptiveParity{Target: 1e-3}
	if got := a.Parity(20); got != 0 {
		t.Fatalf("no loss: parity %d, want 0", got)
	}
	prev := 0
	for _, loss := range []float64{0.001, 0.01, 0.05, 0.2} {
		a.SetLoss(loss)
		k := a.Parity(20)
		if k <= prev && loss > 0.001 {
			t.Fatalf("loss %.3f: parity %d did not grow from %d", loss, k, prev)
		}
		if tail := binomialTailAbove(20+k, k, loss); tail >= 1e-3 {
			t.Fatalf("loss %.3f: parity %d leaves tail %.2g", loss, k, tail)
		}
		if k > 0 && binomialTailAbove(20+k-1, k-1, loss) < 1e-3 {
			t.Fatalf("loss %.3f: parity %d is not minimal", loss, k)
		}
		prev = k
	}
	a.MinLoss = 0.01
	a.SetLoss(0)
	if a.Parity(20) == 0 {
		t.Fatal("MinLoss floor ignored")
	}
}

func TestAdaptiveParityRecoversAtMeasuredLoss(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	frame := randomFrame(r, 78125)
	a := &AdaptiveParity{Target: 1e-3}
	a.SetLoss(0.05)
	p := NewPacketizer(testShard)
	lost := 0
	ra := NewReassembler(testShard, func(s FrameStats) { lost += s.SlicesLost })
	for id := uint32(1); id <= 400; id++ {
		p.Packetize(id, frame, 4, a, func(pkt []byte) error {
			if r.Float64() < 0.05 {
				return nil
			}
			_, err := ra.Push(pkt[AEADHeaderLen:])
			return err
		})
	}
	ra.Flush()
	if lost > 2 {
		t.Fatalf("%d of 1600 slices lost at 5%% loss with adaptive parity", lost)
	}
}

func TestImpairerLossRate(t *testing.T) {
	sent := 0
	im := NewImpairer(Impairment{Loss: 0.05, Seed: 99}, 64, func([]byte) error { sent++; return nil })
	for i := 0; i < 200000; i++ {
		im.Send(nil)
	}
	got := float64(im.Dropped) / 200000
	if got < 0.045 || got > 0.055 || sent != int(im.Sent) {
		t.Fatalf("loss %.4f, sent %d/%d", got, sent, im.Sent)
	}
}

func TestImpairerJitterKeepsOrderAndBound(t *testing.T) {
	const jitter = 2 * time.Millisecond
	var got []byte
	var lags []int64
	var sentAt [200]int64
	im := NewImpairer(Impairment{Jitter: jitter, Seed: 7}, 8, func(b []byte) error {
		got = append(got, b[0])
		lags = append(lags, Mono()-sentAt[b[0]])
		return nil
	})
	for i := 0; i < 200; i++ {
		sentAt[byte(i)] = Mono()
		im.Send([]byte{byte(i)})
		if i%20 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if err := im.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 {
		t.Fatalf("delivered %d of 200", len(got))
	}
	var delayed int
	for i, b := range got {
		if b != byte(i) {
			t.Fatalf("reordered at %d: got %d", i, b)
		}
		if lags[i] > int64(jitter)+int64(5*time.Millisecond) {
			t.Fatalf("packet %d delayed %v, jitter bound %v", i, time.Duration(lags[i]), jitter)
		}
		if lags[i] > int64(200*time.Microsecond) {
			delayed++
		}
	}
	if delayed < 50 {
		t.Fatalf("only %d of 200 packets were delayed", delayed)
	}
}
