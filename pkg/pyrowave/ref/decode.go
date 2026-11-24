package ref

import (
	"encoding/binary"
	"math/bits"

	"github.com/planckdotgg/planck/pkg/pyrowave"
)

// Decoder is the CPU reference decoder.
type Decoder struct {
	L      *pyrowave.Layout
	parser *pyrowave.Parser
	upload []byte
	bands  [3][pyrowave.Levels][4][]float32
	// Header is the sequence header of the last frame decoded.
	Header pyrowave.SequenceHeader
}

func NewDecoder(w, h int, chroma420 bool, chunkLen int) (*Decoder, error) {
	l, err := pyrowave.NewLayout(w, h, chroma420)
	if err != nil {
		return nil, err
	}
	d := &Decoder{L: l, parser: pyrowave.NewParser(l, chunkLen)}
	for _, b := range l.Bands {
		d.bands[b.Component][b.Level][b.Kind] = make([]float32, b.Width*b.Height)
	}
	return d, nil
}

// Parser exposes the record parser, for its counters.
func (d *Decoder) Parser() *pyrowave.Parser { return d.parser }

// Decode decodes one chunked frame.
func (d *Decoder) Decode(au []byte) (*Image, error) {
	if len(d.upload) < len(au) {
		d.upload = make([]byte, len(au))
	}
	hdr, _, err := d.parser.Parse(au, d.upload)
	if err != nil {
		return nil, err
	}
	d.Header = hdr
	for _, b := range d.L.Bands {
		clear(d.bands[b.Component][b.Level][b.Kind])
	}
	for _, r := range d.parser.Recs {
		d.dequant(d.upload[4*r.Word:4*(r.Word+r.Words)], d.L.Bands[r.Band], int(r.Block))
	}
	return d.reconstruct(), nil
}

// DecodeRecords decodes block records laid end to end, as Encoder.Encode
// returns them, without chunk framing.
func (d *Decoder) DecodeRecords(records []byte) *Image {
	for _, b := range d.L.Bands {
		clear(d.bands[b.Component][b.Level][b.Kind])
	}
	for off := 0; off < len(records); {
		kind, n, err := pyrowave.NextRecord(records[off:])
		if err != nil || n == 0 {
			break
		}
		if kind == pyrowave.RecordBlock {
			h := pyrowave.ParseBlockHeader(records[off:])
			if b := d.L.BandOf(h.BlockIndex); b >= 0 {
				d.dequant(records[off:off+n], d.L.Bands[b], h.BlockIndex)
			}
		}
		off += n
	}
	return d.reconstruct()
}

func (d *Decoder) dequant(rec []byte, band pyrowave.Band, block int) {
	h := pyrowave.ParseBlockHeader(rec)
	nb := bits.OnesCount16(h.Ballot)
	local := block - band.FirstBlock
	bx, by := local%band.BlocksX, local/band.BlocksX
	out := d.bands[band.Component][band.Level][band.Kind]
	scale32 := pyrowave.Block32x32Scale(h.QuantCode)
	byteAt := func(i int) byte {
		if i < len(rec) {
			return rec[i]
		}
		return 0
	}
	planeOff := 8 + 3*nb
	signBit := 0
	var vals [16][64]uint32
	k := 0
	for j := 0; j < 16; j++ {
		if h.Ballot&(1<<j) == 0 {
			continue
		}
		cw := binary.LittleEndian.Uint16(rec[8+2*k:])
		qs := rec[8+2*nb+k]
		for s := 0; s < 8; s++ {
			np := pyrowave.SubblockPlanes(cw, qs, s)
			for pl := 0; pl < np; pl++ {
				b := byteAt(planeOff)
				planeOff++
				for p := 0; p < 8; p++ {
					vals[k][s*8+p] = vals[k][s*8+p]<<1 | uint32(b>>p&1)
				}
			}
		}
		k++
	}
	signBase := planeOff * 8
	k = 0
	for j := 0; j < 16; j++ {
		if h.Ballot&(1<<j) == 0 {
			continue
		}
		qs := rec[8+2*nb+k]
		scale := scale32 * pyrowave.Block8x8Scale(qs>>4)
		for i := 0; i < 64; i++ {
			v := vals[k][i]
			if v == 0 {
				continue
			}
			sb := signBase + signBit
			signBit++
			c := (float32(v) + 0.5) * scale
			if byteAt(sb>>3)>>(sb&7)&1 != 0 {
				c = -c
			}
			x, y := pyrowave.Unswizzle8x8(i)
			X, Y := bx*32+(j&3)*8+x, by*32+(j>>2)*8+y
			if X < band.Width && Y < band.Height {
				out[Y*band.Width+X] = c
			}
		}
		k++
	}
}

func (d *Decoder) reconstruct() *Image {
	img := &Image{Width: d.L.Width, Height: d.L.Height, Chroma420: d.L.Chroma420}
	for c := 0; c < 3; c++ {
		var plane []float32
		first := d.L.FirstLevel(c)
		for level := pyrowave.Levels - 1; level >= first; level-- {
			bs := d.bands[c][level]
			if level < pyrowave.Levels-1 {
				bs[0] = plane
			}
			w, h := d.L.AlignedWidth>>level, d.L.AlignedHeight>>level
			plane = inverse2D(&bs, w, h)
		}
		pw, _ := d.L.PlaneSize(c)
		iw, ih := img.PlaneSize(c)
		img.Planes[c] = make([]float32, iw*ih)
		for y := 0; y < ih; y++ {
			for x := 0; x < iw; x++ {
				img.Planes[c][y*iw+x] = clamp01(plane[y*pw+x] + 0.5)
			}
		}
	}
	return img
}
