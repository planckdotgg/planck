package fec

import (
	"bytes"
	"testing"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestAppendPartialZeroFillsLostShards sends a frame of whole shards in
// three slices: slice 0 loses a shard its parity recovers, slice 1 loses two
// shards without parity, and slice 2 never arrives. The ring slot was used
// before, so stale bytes must not leak into the zeroed shards.
func TestAppendPartialZeroFillsLostShards(t *testing.T) {
	const shards = 30
	enc := NewEncoder(testShard, 0, 0)
	dec := NewDecoder(testShard, nil)

	var warm packets
	stale := wire.VideoInfo{FrameID: 1, SliceCount: 3}
	if _, err := enc.EncodeFrame(stale, frameBytes(shards*testShard, 99), Fixed(0), warm.emit(0)); err != nil {
		t.Fatal(err)
	}
	for _, b := range warm.bufs {
		if _, err := dec.Push(b); err != nil {
			t.Fatal(err)
		}
	}

	frame := frameBytes(shards*testShard, 7)
	id := uint32(1 + ringSize)
	ends := []int{10 * testShard, 20 * testShard, 30 * testShard}
	info := wire.VideoInfo{FrameID: id, SliceCount: 3}
	var pk packets
	lo := 0
	for i, end := range ends {
		policy := ParityPolicy(Fixed(0))
		if i == 0 {
			policy = Fixed(20)
		}
		if _, err := enc.EncodeSlice(info, i, frame[lo:end], policy, pk.emit(0)); err != nil {
			t.Fatal(err)
		}
		lo = end
	}
	for _, b := range pk.bufs {
		var h wire.VideoHeader
		if err := h.Parse(b); err != nil {
			t.Fatal(err)
		}
		lost := h.SliceIndex == 0 && h.ShardIndex == 3 ||
			h.SliceIndex == 1 && (h.ShardIndex == 2 || h.ShardIndex == 9) ||
			h.SliceIndex == 2
		if lost {
			continue
		}
		if _, err := dec.Push(b); err != nil {
			t.Fatal(err)
		}
	}
	got, missing, ok := dec.AppendPartial(nil, id)
	if !ok {
		t.Fatal("frame not in the window")
	}
	if missing != 2 {
		t.Fatalf("missing = %d, want 2", missing)
	}
	want := append([]byte(nil), frame[:20*testShard]...)
	clear(want[12*testShard : 13*testShard])
	clear(want[19*testShard : 20*testShard])
	if !bytes.Equal(got, want) {
		t.Fatalf("partial frame differs: got %d bytes, want %d", len(got), len(want))
	}
	if _, _, ok := dec.AppendPartial(nil, id+1); ok {
		t.Fatal("a frame with no shard reported ok")
	}
}
