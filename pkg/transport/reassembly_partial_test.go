package transport

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// TestPartialFrame: lost data shards come back as zeros in place, a slice
// with no shard at all is left out, and FEC-recovered slices come back
// whole.
func TestPartialFrame(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	const slices, perSlice = 3, 6
	frame := randomFrame(r, slices*perSlice*testShard)
	pkts := packetizeAll(t, NewPacketizer(testShard), 11, frame, slices, 20)

	ra := NewReassembler(testShard, nil)
	dropped := map[[2]int]bool{}
	for _, p := range pkts {
		s, i := int(p.h.SliceIndex), int(p.h.ShardIndex)
		switch {
		case s == 0 && i == 2: // recovered by parity
		case s == 1 && (i == 1 || i == 4 || i >= perSlice): // too many lost: 2 data shards and all parity
		case s == 2: // never seen
		default:
			if _, err := ra.Push(p.plain); err != nil {
				t.Fatal(err)
			}
			continue
		}
		dropped[[2]int{s, i}] = true
	}
	got, missing, ok := ra.PartialFrame(11, nil)
	if !ok {
		t.Fatal("frame 11 not in the window")
	}
	if missing != 2 {
		t.Fatalf("missing = %d, want 2", missing)
	}
	want := append([]byte(nil), frame[:2*perSlice*testShard]...)
	for _, k := range []int{1, 4} {
		o := (perSlice + k) * testShard
		clear(want[o : o+testShard])
	}
	if !bytes.Equal(got, want) {
		t.Fatal("partial frame differs from the sent slices with lost shards zeroed")
	}
	if _, _, ok := ra.PartialFrame(12, nil); ok {
		t.Fatal("PartialFrame of a frame never seen reported ok")
	}
	for id := uint32(12); id < 12+ReassemblyWindow; id++ {
		ra.Push(packetizeAll(t, NewPacketizer(testShard), id, frame[:testShard], 1, 0)[0].plain)
	}
	if _, _, ok := ra.PartialFrame(11, nil); ok {
		t.Fatal("PartialFrame of a frame that left the window reported ok")
	}
}

// TestPartialFrameStaleShards: a slot reused by a later frame must not leak
// the earlier frame's bytes into shards that did not arrive.
func TestPartialFrameStaleShards(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	ra := NewReassembler(testShard, nil)
	old := randomFrame(r, 4*testShard)
	for _, p := range packetizeAll(t, NewPacketizer(testShard), 1, old, 1, 0) {
		ra.Push(p.plain)
	}
	next := randomFrame(r, 4*testShard)
	id := uint32(1 + ringSize)
	for _, p := range packetizeAll(t, NewPacketizer(testShard), id, next, 1, 0) {
		if p.h.ShardIndex != 3 {
			ra.Push(p.plain)
		}
	}
	got, missing, ok := ra.PartialFrame(id, nil)
	if !ok || missing != 1 || !bytes.Equal(got[:3*testShard], next[:3*testShard]) || bytes.Count(got[3*testShard:], []byte{0}) != testShard {
		t.Fatalf("ok=%v missing=%d, or the lost shard is not zeros", ok, missing)
	}
}
