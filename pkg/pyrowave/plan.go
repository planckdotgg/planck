package pyrowave

import (
	"embed"
	"math"
)

//go:embed shaders
var shaderFS embed.FS

func shaderSource(path string) string {
	b, err := shaderFS.ReadFile("shaders/" + path)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Sizes shared with the shaders.
const (
	bandWords = 12
	statWords = 16
	rcWords   = 132
	// outPayload is where records start in the encoder's output buffer, in
	// bytes; the first word counts the payload words.
	outPayload = 16
	// maxBlockWords bounds one block record: 16 8x8 blocks of 18 planes
	// and 64 sign bits each, plus headers.
	maxBlockWords = 640
	groupsXMax    = 256
	// outputPool is a decoder's picture count: one decoding, one waiting
	// in the presenter's mailbox, one on screen.
	outputPool = 3
)

// plan is the GPU memory layout of one picture size: every band of every
// component in one float buffer, the coded bands first (in block order,
// so a decoder clears them with one range), then the LL bands of levels 0
// to 3, then the full-size planes.
type plan struct {
	L        *Layout
	bandOff  [3][Levels][4]uint32
	planeOff [3]uint32
	coded    uint32
	total    uint32
	table    []uint32
	passes   []dwtPass // forward order
}

// dwtPass is one level of one component: the forward transform reads in
// (w x h) and writes the four bands at out; the inverse does the reverse.
type dwtPass struct {
	in   uint32
	w, h int
	out  [4]uint32
}

func newPlan(l *Layout) *plan {
	p := &plan{L: l}
	off := uint32(0)
	for _, b := range l.Bands {
		p.bandOff[b.Component][b.Level][b.Kind] = off
		off += uint32(b.Width * b.Height)
	}
	p.coded = off
	for c := 0; c < 3; c++ {
		for level := l.FirstLevel(c); level < Levels-1; level++ {
			p.bandOff[c][level][0] = off
			off += uint32((l.AlignedWidth >> (level + 1)) * (l.AlignedHeight >> (level + 1)))
		}
	}
	for c := 0; c < 3; c++ {
		w, h := l.PlaneSize(c)
		p.planeOff[c] = off
		off += uint32(w * h)
	}
	p.total = off
	p.table = make([]uint32, bandWords*len(l.Bands))
	for i, b := range l.Bands {
		t := p.table[i*bandWords:]
		qres := QuantResolution(b.Level, b.Component, b.Kind)
		t[0] = p.bandOff[b.Component][b.Level][b.Kind]
		t[1], t[2], t[3] = uint32(b.Width), uint32(b.Height), uint32(b.BlocksX)
		t[4], t[5] = uint32(b.FirstBlock), uint32(b.Blocks())
		t[6] = uint32(EncodeQuant(1 / qres))
		t[7] = math.Float32bits(qres)
		t[8] = math.Float32bits(RDOScale(b.Level, b.Component, b.Kind, l.Chroma420))
	}
	for c := 0; c < 3; c++ {
		first := l.FirstLevel(c)
		for level := first; level < Levels; level++ {
			d := dwtPass{w: l.AlignedWidth >> level, h: l.AlignedHeight >> level, out: p.bandOff[c][level]}
			if level == first {
				d.in = p.planeOff[c]
			} else {
				d.in = p.bandOff[c][level-1][0]
			}
			p.passes = append(p.passes, d)
		}
	}
	return p
}

// groups splits n groups into a grid of at most groupsXMax columns.
func groups(n int) (x, y int) {
	if n <= groupsXMax {
		return max(n, 1), 1
	}
	return groupsXMax, (n + groupsXMax - 1) / groupsXMax
}

// params mirrors the shaders' constant buffer (cbuffer Params / struct
// Params), 80 bytes.
type params struct {
	ImageW, ImageH   int32
	PlaneW, PlaneH   int32
	InOff            uint32
	Chroma420        uint32
	TotalBlocks      uint32
	BudgetWords      uint32
	OutOff           [4]uint32
	Seq              uint32
	NumBands         uint32
	Kr, Kb           float32
	ChromaW, ChromaH int32
	GroupsX          uint32
	TenBit           uint32
}

// matrixWeights are the luma weights Kr and Kb of a sequence header's
// YCbCr matrix.
func matrixWeights(code uint8) (kr, kb float32) {
	if code == MatrixBT2020 {
		return 0.2627, 0.0593
	}
	return 0.2126, 0.0722
}

func (p *plan) baseParams() params {
	cw, ch := p.L.PlaneSize(1)
	var c420 uint32
	if p.L.Chroma420 {
		c420 = 1
	}
	return params{
		ImageW: int32(p.L.Width), ImageH: int32(p.L.Height),
		PlaneW: int32(p.L.AlignedWidth), PlaneH: int32(p.L.AlignedHeight),
		Chroma420: c420, TotalBlocks: uint32(p.L.TotalBlocks), NumBands: uint32(len(p.L.Bands)),
		OutOff:  [4]uint32{p.planeOff[0], p.planeOff[1], p.planeOff[2], 0},
		ChromaW: int32(cw), ChromaH: int32(ch),
	}
}
