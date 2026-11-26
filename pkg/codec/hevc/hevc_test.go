package hevc

import (
	"encoding/binary"
	"os"
	"testing"
)

// TestParseNVENCClip parses pkg/decode/vt's NVENC clip, which lost frames
// 20 to 22 and invalidated them as references: every picture's POC rises,
// and every reference its slices use is a picture seen before.
func TestParseNVENCClip(t *testing.T) {
	b, err := os.ReadFile("../../decode/vt/testdata/hevc_refinv_640x360.bin")
	if err != nil {
		t.Fatal(err)
	}
	spss, ppss := map[uint32]*SPS{}, map[uint32]*PPS{}
	var poc POC
	seen := map[int32]bool{}
	last := int32(-1)
	pictures := 0
	for len(b) > 0 {
		frame := binary.BigEndian.Uint32(b)
		n := binary.BigEndian.Uint32(b[4:])
		au := b[8 : 8+n]
		b = b[8+n:]
		var prev *SliceHeader
		for _, nal := range NALUnits(au) {
			switch typ := NALType(nal); {
			case typ == NalSPS:
				s, err := ParseSPS(nal)
				if err != nil {
					t.Fatal(err)
				}
				if w, h := s.VisibleSize(); w != 640 || h != 360 {
					t.Fatalf("SPS: visible %dx%d (coded %dx%d)", w, h, s.Width, s.Height)
				}
				spss[s.ID] = s
			case typ == NalPPS:
				p, err := ParsePPS(nal)
				if err != nil {
					t.Fatal(err)
				}
				ppss[p.ID] = p
			case typ < NalVPS:
				h, s, _, err := ParseSliceHeader(nal, spss, ppss, prev)
				if err != nil {
					t.Fatalf("frame %d: %v", frame, err)
				}
				prev = h
				if !h.FirstSliceSegmentInPic {
					continue
				}
				pictures++
				cur := poc.Next(h, s, IsIDR(h.NalType) || !poc.Seen())
				if cur <= last {
					t.Fatalf("frame %d: POC %d after %d", frame, cur, last)
				}
				for i := range h.ShortTerm.NumNegative {
					if h.ShortTerm.UsedS0[i] && !seen[cur+h.ShortTerm.DeltaPocS0[i]] {
						t.Errorf("frame %d (POC %d) references POC %d, never decoded", frame, cur, cur+h.ShortTerm.DeltaPocS0[i])
					}
				}
				if h.DataOffset <= 2 || h.DataOffset >= len(nal) {
					t.Fatalf("frame %d: slice data at %d of %d", frame, h.DataOffset, len(nal))
				}
				seen[cur] = true
				last = cur
			}
		}
	}
	if pictures != 45 {
		t.Fatalf("%d pictures, want 45", pictures)
	}
}

func TestExpGolomb(t *testing.T) {
	// 1, 010, 011, 00100, 00101 -> ue 0, 1, 2, 3, 4; then se of 00110 (5) = 3.
	r := &reader{b: []byte{0b10100110, 0b01000010, 0b10011000}}
	for want := uint32(0); want < 5; want++ {
		if got := r.ue(); got != want {
			t.Fatalf("ue %d, want %d", got, want)
		}
	}
	if got := r.se(); got != 3 {
		t.Fatalf("se %d, want 3", got)
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
}

func TestRBSPRemovesEmulationPrevention(t *testing.T) {
	out, epb := rbsp([]byte{0x40, 0x01, 0x00, 0x00, 0x03, 0x01, 0x00, 0x00, 0x03})
	if string(out) != string([]byte{0x40, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00}) || len(epb) != 2 || epb[0] != 4 || epb[1] != 8 {
		t.Fatalf("rbsp %x, epb %v", out, epb)
	}
}

func TestUniformTiles(t *testing.T) {
	s := &SPS{Width: 1920, Height: 1080, Log2MinCbMinus3: 0, Log2DiffMaxMinCb: 3} // 64x64 CTBs: 30 x 17
	p := &PPS{Tiles: true, UniformSpacing: true, NumTileColumnsMinus1: 3, NumTileRowsMinus1: 1}
	cols, rows := p.TileSizes(s)
	sum := func(v []uint32) (n uint32) {
		for _, x := range v {
			n += x + 1
		}
		return n
	}
	if len(cols) != 4 || len(rows) != 2 || sum(cols) != 30 || sum(rows) != 17 {
		t.Fatalf("cols %v rows %v", cols, rows)
	}
}
