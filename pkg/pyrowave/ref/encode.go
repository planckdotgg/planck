package ref

import (
	"encoding/binary"
	"math"
	"math/bits"

	"github.com/planckdotgg/planck/pkg/pyrowave"
)

// Rate control constants shared with the shaders.
const (
	// MaxDrop is the most bit-planes rate control drops from a block; a drop
	// of MaxDrop removes the block.
	MaxDrop    = 16
	NumBuckets = 128
	maxMag     = 1<<18 - 1
)

// Encoder is the CPU reference encoder.
type Encoder struct {
	L      *pyrowave.Layout
	Matrix Matrix
	Header pyrowave.SequenceHeader
	// Coefs[c][level][kind] holds every band, LL of each level included.
	Coefs [3][pyrowave.Levels][4][]float32
	seq   uint8
	// Drops is each block's dropped plane count from the last Encode.
	Drops []int
}

func NewEncoder(w, h int, chroma420 bool) (*Encoder, error) {
	l, err := pyrowave.NewLayout(w, h, chroma420)
	if err != nil {
		return nil, err
	}
	hdr := pyrowave.SequenceHeader{Width: w, Height: h, Chroma: pyrowave.ChromaRes444}
	if chroma420 {
		hdr.Chroma = pyrowave.ChromaRes420
	}
	return &Encoder{L: l, Matrix: BT709, Header: hdr, Drops: make([]int, l.TotalBlocks)}, nil
}

// Transform fills Coefs from img: clamp-to-edge padding to the aligned
// size, DC shift by -0.5, then the forward DWT.
func (e *Encoder) Transform(img *Image) {
	for c := 0; c < 3; c++ {
		pw, ph := e.L.PlaneSize(c)
		iw, ih := img.PlaneSize(c)
		plane := make([]float32, pw*ph)
		for y := 0; y < ph; y++ {
			for x := 0; x < pw; x++ {
				plane[y*pw+x] = img.Planes[c][min(y, ih-1)*iw+min(x, iw-1)] - 0.5
			}
		}
		for level := e.L.FirstLevel(c); level < pyrowave.Levels; level++ {
			forward2D(plane, pw, ph, &e.Coefs[c][level])
			plane, pw, ph = e.Coefs[c][level][0], pw/2, ph/2
		}
	}
}

// blockQ is one 32x32 block quantized at full precision.
type blockQ struct {
	absCoef [16][64]float32 // |coefficient|, 0 outside the band
	neg     [16][64]bool
	v       [16][64]uint32 // quantized magnitude
	subMax  [16][8]uint32
	max8    [16]uint32
	code    [16]uint8 // QScale high nibble
	scale   [16]float32
	rdo     float32
	qcode   uint8 // band quant_code before plane drops
}

func (e *Encoder) quantize(bi int, q *blockQ) {
	band := e.L.Bands[e.L.BandOf(bi)]
	local := bi - band.FirstBlock
	bx, by := local%band.BlocksX, local/band.BlocksX
	coefs := e.Coefs[band.Component][band.Level][band.Kind]
	qres := pyrowave.QuantResolution(band.Level, band.Component, band.Kind)
	q.rdo = pyrowave.RDOScale(band.Level, band.Component, band.Kind, e.L.Chroma420)
	q.qcode = pyrowave.EncodeQuant(1 / qres)
	for j := 0; j < 16; j++ {
		var m float32
		for i := 0; i < 64; i++ {
			x, y := pyrowave.Unswizzle8x8(i)
			X, Y := bx*32+(j&3)*8+x, by*32+(j>>2)*8+y
			var c float32
			if X < band.Width && Y < band.Height {
				c = coefs[Y*band.Width+X]
			}
			q.absCoef[j][i] = float32(math.Abs(float64(c)))
			q.neg[j][i] = c < 0
			m = max(m, q.absCoef[j][i]*qres)
		}
		code, qs := pyrowave.QuantCode8x8(m)
		q.code[j] = code
		q.scale[j] = qres * qs
		q.max8[j] = 0
		for s := 0; s < 8; s++ {
			q.subMax[j][s] = 0
			for p := 0; p < 8; p++ {
				v := min(uint32(q.absCoef[j][s*8+p]*qres*qs), maxMag)
				q.v[j][s*8+p] = v
				q.subMax[j][s] = max(q.subMax[j][s], v)
			}
			q.max8[j] = max(q.max8[j], q.subMax[j][s])
		}
	}
}

func bitLen(v uint32) int { return bits.Len32(v) }

// planes8x8 returns the shared quality planes and each subblock's extra
// planes of 8x8 block j after dropping drop planes, or ok false when the
// block quantizes to zero.
func (q *blockQ) planes8x8(j, drop int) (qbits int, codes [8]int, ok bool) {
	m := q.max8[j] >> drop
	if m == 0 {
		return 0, codes, false
	}
	qbits = max(bitLen(m)-3, 0)
	for s := 0; s < 8; s++ {
		codes[s] = bitLen(q.subMax[j][s] >> drop >> qbits)
	}
	return qbits, codes, true
}

// cost returns the record words and weighted distortion of the block after
// dropping drop planes.
func (q *blockQ) cost(drop int) (words int, dist float32) {
	if drop > int(q.qcode>>3) {
		drop = MaxDrop
	}
	n, planes, signs := 0, 0, 0
	for j := 0; j < 16; j++ {
		qbits, codes, ok := q.planes8x8(j, drop)
		inv := 1 / q.scale[j]
		for i := 0; i < 64; i++ {
			var vq uint32
			if ok && drop < 32 {
				vq = q.v[j][i] >> drop
			}
			var rec float32
			if vq != 0 {
				rec = (float32(vq) + 0.5) * float32(uint32(1)<<drop) * inv
				signs++
			}
			d := q.absCoef[j][i] - rec
			dist += d * d * q.rdo
		}
		if !ok {
			continue
		}
		n++
		for s := 0; s < 8; s++ {
			planes += qbits + codes[s]
		}
	}
	if n == 0 {
		return 0, dist
	}
	return 2 + (3*n+planes+(signs+7)/8+3)/4, dist
}

// Bucket maps the distortion added per word saved to a log-scale bucket,
// about 1.5 dB apart.
func Bucket(dd float32, saving int) int {
	v := 60 + 2*float32(math.Log2(float64(max(dd, 0)/float32(saving)))) + 0.5
	if !(v > 0) {
		return 0
	}
	return int(min(v, 1e6))
}

// steps returns each plane drop's saving in words and its bucket; buckets
// rise strictly so a block's drops apply in order.
func steps(words [MaxDrop + 1]int, dist [MaxDrop + 1]float32) (saving [MaxDrop + 1]int, bucket [MaxDrop + 1]int) {
	last := 0
	for i := 1; i <= MaxDrop; i++ {
		b := 0
		if s := words[last] - words[i]; s > 0 {
			saving[i] = words[i-1] - words[i]
			b = Bucket(dist[i]-dist[last], s)
		}
		b = min(b, NumBuckets-1-MaxDrop+i)
		b = max(b, bucket[i-1]+1)
		bucket[i] = b
		if saving[i] > 0 {
			last = i
		}
	}
	return
}

// Encode codes img in about budget bytes of records and returns the block
// records, in block order.
func (e *Encoder) Encode(img *Image, budget int) []byte {
	e.Transform(img)
	nb := e.L.TotalBlocks
	qs := make([]blockQ, nb)
	type rc struct {
		saving, bucket [MaxDrop + 1]int
	}
	rcs := make([]rc, nb)
	var hist [NumBuckets]int
	consumed := 0
	for bi := 0; bi < nb; bi++ {
		e.quantize(bi, &qs[bi])
		var words [MaxDrop + 1]int
		var dist [MaxDrop + 1]float32
		for d := 0; d <= MaxDrop; d++ {
			words[d], dist[d] = qs[bi].cost(d)
		}
		rcs[bi].saving, rcs[bi].bucket = steps(words, dist)
		consumed += words[0]
		for i := 1; i <= MaxDrop; i++ {
			hist[rcs[bi].bucket[i]] += rcs[bi].saving[i]
		}
	}
	threshold, remaining := Threshold(hist[:], consumed-budget/4)
	counter := 0
	for bi := 0; bi < nb; bi++ {
		drop := 0
		r := &rcs[bi]
		for i := 1; i <= MaxDrop; i++ {
			if r.saving[i] == 0 {
				continue
			}
			if r.bucket[i] < threshold {
				drop = i
			} else if r.bucket[i] == threshold {
				if counter < remaining {
					drop = i
				}
				counter += r.saving[i]
			}
		}
		e.Drops[bi] = drop
	}
	e.Header.Sequence = e.seq
	out := make([]byte, 0, budget+4096)
	for bi := 0; bi < nb; bi++ {
		out = e.pack(out, bi, &qs[bi], e.Drops[bi])
	}
	e.seq = (e.seq + 1) & 7
	return out
}

// Threshold finds the bucket where cumulative savings first cover required
// words and how much of that bucket is still needed; -1 when nothing must
// be dropped.
func Threshold(hist []int, required int) (bucket, remaining int) {
	if required <= 0 {
		return -1, 0
	}
	sum := 0
	for b, s := range hist {
		if sum+s >= required {
			return b, required - sum
		}
		sum += s
	}
	return len(hist), 0
}

func (e *Encoder) pack(out []byte, bi int, q *blockQ, drop int) []byte {
	if drop > int(q.qcode>>3) || drop >= MaxDrop {
		return out
	}
	words, _ := q.cost(drop)
	if words == 0 {
		return out
	}
	start := len(out)
	out = append(out, make([]byte, 4*words)...)
	rec := out[start:]
	var ballot uint16
	n := 0
	for j := 0; j < 16; j++ {
		if q.max8[j]>>drop != 0 {
			ballot |= 1 << j
			n++
		}
	}
	pyrowave.BlockHeader{Ballot: ballot, PayloadWords: words, Sequence: e.seq,
		QuantCode: q.qcode - uint8(drop<<3), BlockIndex: bi}.Put(rec)
	planeOff := 8 + 3*n
	var signBits []bool
	k := 0
	for j := 0; j < 16; j++ {
		qbits, codes, ok := q.planes8x8(j, drop)
		if !ok {
			continue
		}
		var cw uint16
		for s := 0; s < 8; s++ {
			cw |= uint16(codes[s]) << (2 * s)
		}
		binary.LittleEndian.PutUint16(rec[8+2*k:], cw)
		rec[8+2*n+k] = uint8(qbits) | q.code[j]<<4
		k++
		for s := 0; s < 8; s++ {
			np := qbits + codes[s]
			for pl := np - 1; pl >= 0; pl-- {
				var b byte
				for p := 0; p < 8; p++ {
					b |= byte((q.v[j][s*8+p]>>drop)>>pl&1) << p
				}
				rec[planeOff] = b
				planeOff++
			}
			for p := 0; p < 8; p++ {
				if q.v[j][s*8+p]>>drop != 0 {
					signBits = append(signBits, q.neg[j][s*8+p])
				}
			}
		}
	}
	for i, neg := range signBits {
		if neg {
			rec[planeOff+i/8] |= 1 << (i % 8)
		}
	}
	return out
}
