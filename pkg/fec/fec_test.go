package fec

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/watzon/planck/pkg/wire"
)

const testShard = 1091

func TestPolicyScalesWithLoss(t *testing.T) {
	p := NewPolicy()
	if got := p.Parity(30); got != 0 {
		t.Fatalf("no loss: parity %d, want 0", got)
	}
	p.SetLoss(0.01)
	if got := p.Parity(30); got < 3 || got > 6 {
		t.Fatalf("1%% loss: parity %d for 30 data, want 10-20%%", got)
	}
	p.SetLoss(0.05)
	if got := p.Parity(30); got != 7 {
		t.Fatalf("5%% loss: parity %d for 30 data, want 7", got)
	}
	if got := p.Parity(10); got < 4 {
		t.Fatalf("5%% loss: parity %d for 10 data, want at least 40%%", got)
	}
	p.MaxRatio = 0.2
	if got := p.Parity(30); got != 6 {
		t.Fatalf("5%% loss capped at 20%%: parity %d, want 6", got)
	}
	if got := (&Policy{Target: 1e-3}).Parity(200); got > wire.MaxShards-200 {
		t.Fatalf("parity %d overflows the 255-shard code", got)
	}
	if got := Fixed(20).Parity(30); got != 6 {
		t.Fatalf("Fixed(20).Parity(30) = %d, want 6", got)
	}
}

func TestPolicyObserveTracksLoss(t *testing.T) {
	p := NewPolicy()
	for i := 0; i < 50; i++ {
		p.Observe(98, 100)
	}
	if l := p.Loss(); l < 0.015 || l > 0.025 {
		t.Fatalf("after steady 2%% loss estimate is %.4f", l)
	}
	risen := NewPolicy()
	risen.Observe(90, 100)
	fell := NewPolicy()
	fell.SetLoss(0.1)
	fell.Observe(100, 100)
	if rise, drop := risen.Loss(), 0.1-fell.Loss(); rise <= drop*3 {
		t.Fatalf("rising loss moved the estimate %.4f, falling %.4f: want rise ~4x faster", rise, drop)
	}
	for i := 0; i < 500; i++ {
		p.Observe(100, 100)
	}
	if l := p.Loss(); l > 0.001 || p.Parity(30) != 0 {
		t.Fatalf("after a clean stretch estimate is %.4f, parity %d", l, p.Parity(30))
	}
}

type packets struct {
	bufs [][]byte
}

func (p *packets) emit(headroom int) func([]byte) error {
	return func(pkt []byte) error {
		p.bufs = append(p.bufs, append([]byte(nil), pkt[headroom:]...))
		return nil
	}
}

func frameBytes(n int, seed uint64) []byte {
	r := rand.New(rand.NewPCG(seed, seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func TestRoundTripShapes(t *testing.T) {
	cases := []struct {
		name   string
		size   int
		slices uint8
		policy ParityPolicy
	}{
		{"tiny", 5, 1, Fixed(0)},
		{"exact shard", testShard, 1, Fixed(20)},
		{"4 slices", 80_000, 4, Fixed(20)},
		{"16 slices", 300_000, 16, Fixed(10)},
		{"multi-block keyframe", 900_000, 1, Fixed(10)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := NewEncoder(testShard, 9, 16)
			frame := frameBytes(tc.size, uint64(tc.size))
			var pk packets
			info := wire.VideoInfo{FrameID: 7, Keyframe: true, CaptureTS: 11, EncodeTS: 22, SliceCount: tc.slices}
			n, err := enc.EncodeFrame(info, frame, tc.policy, pk.emit(9))
			if err != nil || n != len(pk.bufs) {
				t.Fatalf("encode: n=%d bufs=%d err=%v", n, len(pk.bufs), err)
			}
			dec := NewDecoder(testShard, nil)
			var last PushResult
			for i := len(pk.bufs) - 1; i >= 0; i-- {
				res, err := dec.Push(pk.bufs[i])
				if err != nil {
					t.Fatal(err)
				}
				if res.FrameComplete {
					last = res
				}
			}
			if !last.FrameComplete || last.FrameBytes != tc.size || last.Info != info {
				t.Fatalf("frame not complete: %+v", last)
			}
			got, ok := dec.AppendFrame(nil, 7)
			if !ok || !bytes.Equal(got, frame) {
				t.Fatalf("reassembled frame differs (ok=%v len=%d)", ok, len(got))
			}
		})
	}
}

func TestRecoversLossWithinParity(t *testing.T) {
	enc := NewEncoder(testShard, 0, 0)
	dec := NewDecoder(testShard, nil)
	frame := frameBytes(200_000, 1)
	info := wire.VideoInfo{FrameID: 1, SliceCount: 4}
	var pk packets
	if _, err := enc.EncodeFrame(info, frame, Fixed(20), pk.emit(0)); err != nil {
		t.Fatal(err)
	}
	recovered := 0
	for i, b := range pk.bufs {
		var h wire.VideoHeader
		_ = h.Parse(b)
		if int(h.ShardIndex) < int(h.ParityShards) && h.ShardIndex%2 == 0 {
			continue
		}
		res, err := dec.Push(b)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if res.SliceReady && res.Recovered {
			recovered++
		}
	}
	got, ok := dec.AppendFrame(nil, 1)
	if !ok || !bytes.Equal(got, frame) {
		t.Fatal("frame not rebuilt from parity")
	}
	if recovered != 4 {
		t.Fatalf("%d slices reported recovered, want 4", recovered)
	}
}

func TestUnrecoverableLossIsReported(t *testing.T) {
	var stats []FrameStats
	enc := NewEncoder(testShard, 0, 0)
	dec := NewDecoder(testShard, func(s FrameStats) { stats = append(stats, s) })
	for id := uint32(1); id <= 2+ReassemblyWindow; id++ {
		var pk packets
		info := wire.VideoInfo{FrameID: id, SliceCount: 2}
		if _, err := enc.EncodeFrame(info, frameBytes(20_000, uint64(id)), Fixed(0), pk.emit(0)); err != nil {
			t.Fatal(err)
		}
		for i, b := range pk.bufs {
			if id == 1 && i == 0 {
				continue
			}
			if _, err := dec.Push(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(stats) == 0 || stats[0].FrameID != 1 {
		t.Fatalf("frame 1 not finalized: %+v", stats)
	}
	s := stats[0]
	if s.Complete || s.SlicesLost != 1 || s.RecvShards != s.SentShards-1 {
		t.Fatalf("frame 1 stats %+v, want incomplete with one lost slice and one missing shard", s)
	}
	if len(stats) > 1 && !stats[1].Complete {
		t.Fatalf("frame 2 should be complete: %+v", stats[1])
	}
	if dec.Slice(1, 0) != nil {
		t.Fatal("finalized frame still readable")
	}
}

func TestRejectsLateAndInvalid(t *testing.T) {
	enc := NewEncoder(testShard, 0, 0)
	dec := NewDecoder(testShard, nil)
	emitFrame := func(id uint32) [][]byte {
		var pk packets
		if _, err := enc.EncodeFrame(wire.VideoInfo{FrameID: id, SliceCount: 1}, frameBytes(3000, uint64(id)), Fixed(0), pk.emit(0)); err != nil {
			t.Fatal(err)
		}
		return pk.bufs
	}
	old := emitFrame(1)
	for _, b := range emitFrame(1 + ReassemblyWindow) {
		dec.Push(b)
	}
	if res, err := dec.Push(old[0]); err != nil || res.SliceReady || dec.Late != 1 {
		t.Fatalf("late shard: res=%+v err=%v late=%d", res, err, dec.Late)
	}
	other := NewDecoder(500, nil)
	if _, err := other.Push(old[0]); err == nil || other.Invalid != 1 {
		t.Fatal("shard of the wrong size accepted")
	}
	bad := append([]byte(nil), old[1]...)
	bad[3] = 2
	if _, err := dec.Push(emitFrame(20)[0]); err != nil {
		t.Fatal(err)
	}
	bad[12], bad[13], bad[14], bad[15] = 0, 0, 0, 20
	if _, err := dec.Push(bad); err == nil {
		t.Fatal("shard with a conflicting slice count accepted")
	}
}

func TestEncodeSliceErrors(t *testing.T) {
	enc := NewEncoder(100, 0, 0)
	nop := func([]byte) error { return nil }
	if _, err := enc.EncodeSlice(wire.VideoInfo{SliceCount: 1}, 1, []byte{1}, Fixed(0), nop); err == nil {
		t.Fatal("slice index out of range accepted")
	}
	if _, err := enc.EncodeSlice(wire.VideoInfo{SliceCount: 1}, 0, nil, Fixed(0), nop); err == nil {
		t.Fatal("empty slice accepted")
	}
	huge := make([]byte, 100*MaxDataPerBlock*wire.MaxBlocks+1)
	if _, err := enc.EncodeSlice(wire.VideoInfo{SliceCount: 1}, 0, huge, Fixed(0), nop); err == nil {
		t.Fatal("slice beyond MaxBlocks accepted")
	}
}

func TestNoParityPathDoesNotAllocate(t *testing.T) {
	enc := NewEncoder(testShard, 9, 16)
	dec := NewDecoder(testShard, func(FrameStats) {})
	frame := frameBytes(78_000, 3)
	id := uint32(0)
	push := func(pkt []byte) error { _, err := dec.Push(pkt[9:]); return err }
	run := func() {
		id++
		if _, err := enc.EncodeFrame(wire.VideoInfo{FrameID: id, SliceCount: 4}, frame, Fixed(0), push); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2*ringSize; i++ {
		run()
	}
	if allocs := testing.AllocsPerRun(200, run); allocs != 0 {
		t.Fatalf("encode+decode without parity allocates %.2f per frame", allocs)
	}
}

func BenchmarkEncodeDecode(b *testing.B) {
	for _, bc := range []struct {
		name   string
		policy ParityPolicy
		drop   int
	}{
		{"fec0", Fixed(0), 0},
		{"fec20", Fixed(20), 0},
		{"fec20/loss6pct", Fixed(20), 17},
	} {
		b.Run(bc.name, func(b *testing.B) {
			enc := NewEncoder(testShard, 9, 16)
			dec := NewDecoder(testShard, nil)
			frame := frameBytes(78_000, 3)
			n := 0
			push := func(pkt []byte) error {
				n++
				if bc.drop > 0 && n%bc.drop == 0 {
					return nil
				}
				_, err := dec.Push(pkt[9:])
				return err
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := enc.EncodeFrame(wire.VideoInfo{FrameID: uint32(i), SliceCount: 4}, frame, bc.policy, push); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(n)/float64(b.N), "pkts/frame")
		})
	}
}
