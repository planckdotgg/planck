package pyrowave

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Chunk framing. A frame is a run of chunks of ChunkLen bytes (the
// transport's shard size). Each chunk starts with the sequence header and
// holds whole records in its first ChunkLen&^3 bytes; a zero word ends it.
// A block too big for one chunk is split at 8x8 boundaries into records with
// the same block_index and disjoint ballots.

const (
	// MinChunkLen leaves room for the header and the largest 8x8 block.
	MinChunkLen = 256
	// MaxSliceChunks is the data shard count per slice the packer aims for,
	// leaving room for parity under the 255-shard Reed-Solomon limit.
	MaxSliceChunks = 192
	maxSlices      = 16
	maxShards      = 255
)

var ErrTooLarge = errors.New("pyrowave: frame needs more than 16 slices of 255 chunks")

// Packer arranges an encoder's block records into chunks. Not safe for
// concurrent use; it allocates only in NewPacker.
type Packer struct {
	layout    *Layout
	chunkLen  int
	cap       int
	offsets   []int32
	sliceEnds []int
}

func NewPacker(l *Layout, chunkLen int) (*Packer, error) {
	if chunkLen < MinChunkLen {
		return nil, fmt.Errorf("pyrowave: chunk length %d below %d", chunkLen, MinChunkLen)
	}
	return &Packer{layout: l, chunkLen: chunkLen, cap: chunkLen &^ 3,
		offsets: make([]int32, l.TotalBlocks), sliceEnds: make([]int, 0, maxSlices)}, nil
}

// MaxFrameLen is the largest packed frame.
func (p *Packer) MaxFrameLen() int { return maxSlices * maxShards * p.chunkLen }

// Overhead is the share of chunk bytes that do not carry block data, for
// budgeting: the per-chunk header and tail, plus split record headers and
// padding (about 2% measured at 1080p).
func (p *Packer) Overhead() float64 {
	return float64(p.chunkLen-p.cap+8)/float64(p.chunkLen) + 0.025
}

// Packed is one packed frame.
type Packed struct {
	Data []byte
	// SliceEnds are the end offsets in Data of each slice; every slice is
	// whole chunks.
	SliceEnds []int
	// Critical is the number of leading slices that hold levels 4 and 3.
	Critical int
	Chunks   int
}

// Pack writes the frame's chunks to dst (grown if needed). payload holds
// block records in any order, as the GPU wrote them; seq is the frame's
// sequence header. The returned SliceEnds alias the packer and are valid
// until the next Pack.
func (p *Packer) Pack(dst []byte, seq SequenceHeader, payload []byte) (Packed, error) {
	for i := range p.offsets {
		p.offsets[i] = -1
	}
	for off := 0; off < len(payload); {
		kind, n, err := NextRecord(payload[off:])
		if err != nil {
			return Packed{}, fmt.Errorf("pyrowave: encoder payload at %d: %w", off, err)
		}
		if kind == RecordEnd {
			break
		}
		if kind == RecordBlock {
			h := ParseBlockHeader(payload[off:])
			if h.BlockIndex >= len(p.offsets) {
				return Packed{}, fmt.Errorf("pyrowave: encoder block %d out of range", h.BlockIndex)
			}
			p.offsets[h.BlockIndex] = int32(off)
		}
		off += n
	}
	seq.TotalBlocks = 0
	for _, o := range p.offsets {
		if o >= 0 {
			seq.TotalBlocks++
		}
	}
	w := chunkWriter{p: p, dst: dst[:0], seq: seq}
	critical := -1
	for b, o := range p.offsets {
		if b == p.layout.CriticalBlocks {
			critical = w.chunks
			w.close()
		}
		if o < 0 {
			continue
		}
		if err := w.record(payload[o:]); err != nil {
			return Packed{}, err
		}
	}
	if critical < 0 {
		critical = w.chunks
	}
	if w.chunks == 0 {
		w.open()
	}
	return p.slices(w.dst, w.chunks, critical)
}

func (p *Packer) slices(data []byte, chunks, critical int) (Packed, error) {
	p.sliceEnds = p.sliceEnds[:0]
	rest := chunks - critical
	cs := (critical + MaxSliceChunks - 1) / MaxSliceChunks
	rs := (rest + MaxSliceChunks - 1) / MaxSliceChunks
	if cs+rs > maxSlices {
		rs = maxSlices - cs
		if rs < 1 || (rest+rs-1)/rs > maxShards-1 {
			return Packed{}, ErrTooLarge
		}
	}
	split := func(from, n, k int) {
		for i := 0; i < k; i++ {
			p.sliceEnds = append(p.sliceEnds, (from+n*(i+1)/k)*p.chunkLen)
		}
	}
	split(0, critical, cs)
	split(critical, rest, rs)
	return Packed{Data: data, SliceEnds: p.sliceEnds, Critical: cs, Chunks: chunks}, nil
}

type chunkWriter struct {
	p      *Packer
	dst    []byte
	seq    SequenceHeader
	start  int
	used   int // bytes used in the open chunk, 0 when none is open
	chunks int
}

func (w *chunkWriter) open() {
	n := len(w.dst) + w.p.chunkLen
	if cap(w.dst) < n {
		grown := make([]byte, len(w.dst), max(n, 2*cap(w.dst)))
		copy(grown, w.dst)
		w.dst = grown
	}
	w.start = len(w.dst)
	w.dst = w.dst[:n]
	clear(w.dst[w.start:])
	w.seq.Put(w.dst[w.start:])
	w.used = 8
	w.chunks++
}

func (w *chunkWriter) close() { w.used = 0 }

// room returns n bytes in the open chunk, opening one when needed.
func (w *chunkWriter) room(n int) []byte {
	if w.used == 0 || w.used+n > w.p.cap {
		w.close()
		w.open()
	}
	b := w.dst[w.start+w.used : w.start+w.used+n]
	w.used += n
	return b
}

func (w *chunkWriter) free() int {
	if w.used == 0 {
		return 0
	}
	return w.p.cap - w.used
}

// record places rec in the open chunk, or splits it at 8x8 boundaries so
// its first part fills what the open chunk has left.
func (w *chunkWriter) record(rec []byte) error {
	h := ParseBlockHeader(rec)
	n := 4 * h.PayloadWords
	if n <= w.free() || (n <= w.p.cap-8 && w.free() < splitMin) {
		copy(w.room(n), rec[:n])
		return nil
	}
	return w.split(rec[:n], h)
}

// splitMin is the least room worth starting a split record in.
const splitMin = 64

// blockGeometry returns, for each 8x8 block of a record in ballot order,
// its plane byte count and sign bit count.
func blockGeometry(rec []byte, nb int, planes, signs *[16]int) (planeTotal, signTotal int, ok bool) {
	cwOff, qsOff := 8, 8+2*nb
	off := 8 + 3*nb
	for i := 0; i < nb; i++ {
		cw := binary.LittleEndian.Uint16(rec[cwOff+2*i:])
		qs := rec[qsOff+i]
		p, s := 0, 0
		for sb := 0; sb < 8; sb++ {
			np := SubblockPlanes(cw, qs, sb)
			if off+np > len(rec) {
				return 0, 0, false
			}
			var or byte
			for _, v := range rec[off : off+np] {
				or |= v
			}
			s += bits.OnesCount8(or)
			off += np
			p += np
		}
		planes[i], signs[i] = p, s
		planeTotal += p
		signTotal += s
	}
	return planeTotal, signTotal, off+(signTotal+7)/8 <= len(rec)
}

func recordLen(n, planes, signs int) int { return (8 + 3*n + planes + (signs+7)/8 + 3) &^ 3 }

func (w *chunkWriter) split(rec []byte, h BlockHeader) error {
	nb := bits.OnesCount16(h.Ballot)
	var planes, signs [16]int
	pt, _, ok := blockGeometry(rec, nb, &planes, &signs)
	if !ok {
		return fmt.Errorf("pyrowave: encoder block %d is malformed", h.BlockIndex)
	}
	var idx [16]int
	k := 0
	for b := 0; b < 16; b++ {
		if h.Ballot&(1<<b) != 0 {
			idx[k] = b
			k++
		}
	}
	planeBase := 8 + 3*nb
	signBase := (planeBase + pt) * 8
	planeOff, signOff := 0, 0
	for first := 0; first < nb; {
		room := w.free()
		if room < splitMin || recordLen(1, planes[first], signs[first]) > room {
			w.close()
			room = w.p.cap - 8
		}
		last, p, s := first, 0, 0
		for last < nb && recordLen(last-first+1, p+planes[last], s+signs[last]) <= room {
			p += planes[last]
			s += signs[last]
			last++
		}
		if last == first {
			return fmt.Errorf("pyrowave: an 8x8 block of block %d does not fit a %d-byte chunk", h.BlockIndex, w.p.chunkLen)
		}
		n := last - first
		size := recordLen(n, p, s)
		out := w.room(size)
		clear(out)
		var ballot uint16
		for i := first; i < last; i++ {
			ballot |= 1 << idx[i]
		}
		BlockHeader{Ballot: ballot, PayloadWords: size / 4, Sequence: h.Sequence, QuantCode: h.QuantCode, BlockIndex: h.BlockIndex}.Put(out)
		copy(out[8:], rec[8+2*first:8+2*last])
		copy(out[8+2*n:], rec[8+2*nb+first:8+2*nb+last])
		copy(out[8+3*n:], rec[planeBase+planeOff:planeBase+planeOff+p])
		dst := (8 + 3*n + p) * 8
		for i := 0; i < s; i++ {
			src := signBase + signOff + i
			if rec[src>>3]>>(src&7)&1 != 0 {
				d := dst + i
				out[d>>3] |= 1 << (d & 7)
			}
		}
		planeOff += p
		signOff += s
		first = last
	}
	return nil
}

// Rec is one validated block record copied to the decoder's upload buffer.
type Rec struct {
	// Word is the record's offset in the upload buffer, in 32-bit words.
	Word  int32
	Words int32
	Block int32
	// Band indexes Layout.Bands.
	Band int32
}

// Parser validates the records of a received frame and gathers them,
// word aligned, for the GPU. Not safe for concurrent use.
type Parser struct {
	layout   *Layout
	chunkLen int
	ballots  []uint16
	Recs     []Rec
	// Rejected counts records dropped as malformed, conflicting or stale.
	Rejected int
}

func NewParser(l *Layout, chunkLen int) *Parser {
	return &Parser{layout: l, chunkLen: chunkLen, ballots: make([]uint16, l.TotalBlocks), Recs: make([]Rec, 0, 2*l.TotalBlocks)}
}

// ErrNoHeader means no chunk of the frame arrived with a sequence header.
var ErrNoHeader = errors.New("pyrowave: no sequence header in the frame")

// Parse walks the chunks of au, copies every valid block record to upload
// and fills Recs. upload must hold len(au) bytes. Missing chunks are zero
// and skipped. It returns the frame's sequence header and the bytes used in
// upload.
func (p *Parser) Parse(au, upload []byte) (SequenceHeader, int, error) {
	clear(p.ballots)
	p.Recs = p.Recs[:0]
	p.Rejected = 0
	var seq SequenceHeader
	have := false
	used := 0
	for c := 0; c < len(au); c += p.chunkLen {
		chunk := au[c:min(c+p.chunkLen, len(au))]
		chunk = chunk[:len(chunk)&^3]
		for off := 0; off < len(chunk); {
			kind, n, err := NextRecord(chunk[off:])
			if err != nil {
				p.Rejected++
				break
			}
			if kind == RecordEnd {
				break
			}
			rec := chunk[off : off+n]
			off += n
			switch kind {
			case RecordSequence:
				s, _ := ParseSequenceHeader(rec)
				if !have {
					if s.Width != p.layout.Width || s.Height != p.layout.Height || (s.Chroma == ChromaRes420) != p.layout.Chroma420 {
						return s, 0, fmt.Errorf("pyrowave: frame is %dx%d chroma %d, decoder is %dx%d", s.Width, s.Height, s.Chroma, p.layout.Width, p.layout.Height)
					}
					seq, have = s, true
				} else if s.Sequence != seq.Sequence {
					p.Rejected++
				}
			case RecordBlock:
				h := ParseBlockHeader(rec)
				if !p.accept(rec, h, have, seq.Sequence) {
					p.Rejected++
					continue
				}
				p.ballots[h.BlockIndex] |= h.Ballot
				copy(upload[used:], rec)
				p.Recs = append(p.Recs, Rec{Word: int32(used / 4), Words: int32(n / 4), Block: int32(h.BlockIndex), Band: int32(p.layout.BandOf(h.BlockIndex))})
				used += n
			}
		}
	}
	if !have {
		return seq, 0, ErrNoHeader
	}
	return seq, used, nil
}

func (p *Parser) accept(rec []byte, h BlockHeader, have bool, sequence uint8) bool {
	if h.BlockIndex >= len(p.ballots) || p.ballots[h.BlockIndex]&h.Ballot != 0 || (have && h.Sequence != sequence) {
		return false
	}
	nb := bits.OnesCount16(h.Ballot)
	need := 8 + 3*nb
	if need > len(rec) {
		return false
	}
	for i := 0; i < nb; i++ {
		cw := binary.LittleEndian.Uint16(rec[8+2*i:])
		qs := rec[8+2*nb+i]
		for sb := 0; sb < 8; sb++ {
			need += SubblockPlanes(cw, qs, sb)
		}
	}
	return need <= len(rec)
}
