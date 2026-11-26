package fec

import (
	"bytes"
	"testing"

	"github.com/planckdotgg/planck/pkg/wire"
)

// TestInterleavedBlocksSurviveBurst: a P frame of two Reed-Solomon blocks
// on a clean path (no loss measured, so no adaptive parity) loses six
// consecutive datagrams, at every place in the frame, and is rebuilt
// whole each time. Sent block by block with no parity, any such burst
// lost the frame and cost the client a keyframe.
func TestInterleavedBlocksSurviveBurst(t *testing.T) {
	const burst = 6
	frame := frameBytes(200*testShard-123, 9)
	enc := NewEncoder(testShard, 0, 0)
	var pk packets
	info := wire.VideoInfo{FrameID: 1, SliceCount: 1}
	if _, err := enc.EncodeFrame(info, frame, NewPolicy(), pk.emit(0)); err != nil {
		t.Fatal(err)
	}
	var first wire.VideoHeader
	if err := first.Parse(pk.bufs[0]); err != nil {
		t.Fatal(err)
	}
	if first.BlockCount != 2 {
		t.Fatalf("frame has %d blocks, want 2", first.BlockCount)
	}
	for start := 0; start+burst <= len(pk.bufs); start++ {
		dec := NewDecoder(testShard, nil)
		for i, b := range pk.bufs {
			if i >= start && i < start+burst {
				continue
			}
			if _, err := dec.Push(b); err != nil {
				t.Fatalf("burst at %d: packet %d: %v", start, i, err)
			}
		}
		got, ok := dec.AppendFrame(nil, 1)
		if !ok || !bytes.Equal(got, frame) {
			t.Fatalf("burst of %d at datagram %d of %d: frame not rebuilt", burst, start, len(pk.bufs))
		}
	}
	t.Logf("%d datagrams (%d data shards, 2 blocks): every %d-datagram burst repaired", len(pk.bufs), (len(frame)+testShard-1)/testShard, burst)
}

// TestBlocksInterleaveOnTheWire: consecutive datagrams of a multi-block
// slice belong to different blocks.
func TestBlocksInterleaveOnTheWire(t *testing.T) {
	enc := NewEncoder(testShard, 0, 0)
	var pk packets
	if _, err := enc.EncodeFrame(wire.VideoInfo{FrameID: 1, SliceCount: 1}, frameBytes(300*testShard, 2), Fixed(0), pk.emit(0)); err != nil {
		t.Fatal(err)
	}
	var prev wire.VideoHeader
	for i, b := range pk.bufs {
		var h wire.VideoHeader
		if err := h.Parse(b); err != nil {
			t.Fatal(err)
		}
		if h.BlockCount != 3 {
			t.Fatalf("%d blocks, want 3", h.BlockCount)
		}
		if i > 0 && h.BlockIndex == prev.BlockIndex {
			t.Fatalf("datagrams %d and %d are both of block %d", i-1, i, h.BlockIndex)
		}
		prev = h
	}
}
