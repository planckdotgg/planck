package hevc

import (
	"bytes"
	"testing"
)

func testStream() *Stream {
	return &Stream{
		ProfileIdc: 2, LevelIdc: 153, Width: 1920, Height: 1088, VisibleWidth: 1920, VisibleHeight: 1080,
		BitDepthMinus8: 2, Log2MaxPocLsb: 16,
		Log2MinCbMinus3: 0, Log2DiffMaxMinCb: 3, Log2MinTbMinus2: 0, Log2DiffMaxMinTb: 3, MaxTHDepthInter: 2, MaxTHDepthIntra: 2,
		AMP: false, SAO: true, TemporalMVP: true,
		InitQPMinus26: 4, CuQPDelta: true, LoopFilterAcrossSlices: true, MaxDecPicBufferingMinus1: 1,
		Colour: &Colour{Primaries: 9, Transfer: 16, Matrix: 9},
	}
}

// TestWrittenParameterSetsParse writes an SPS and a PPS and reads them
// back with the parser the VA-API decoder uses.
func TestWrittenParameterSetsParse(t *testing.T) {
	s := testStream()
	sps, err := ParseSPS(s.SPS())
	if err != nil {
		t.Fatal(err)
	}
	if w, h := sps.VisibleSize(); w != 1920 || h != 1080 || sps.Width != 1920 || sps.Height != 1088 {
		t.Fatalf("size %dx%d coded %dx%d", w, h, sps.Width, sps.Height)
	}
	if sps.BitDepthLuma != 2 || sps.BitDepthChroma != 2 || sps.Log2MaxPocLsb != 16 || sps.MaxDecPicBufferingMinus1 != 1 {
		t.Fatalf("depth %d/%d poc lsb %d dpb %d", sps.BitDepthLuma, sps.BitDepthChroma, sps.Log2MaxPocLsb, sps.MaxDecPicBufferingMinus1)
	}
	if sps.CtbLog2() != 6 || sps.Log2DiffMaxMinTb != 3 || sps.MaxTHDepthInter != 2 || sps.AMP || !sps.SAO || !sps.TemporalMVP || len(sps.ShortTermRPS) != 0 {
		t.Fatalf("sps %+v", sps)
	}
	pps, err := ParsePPS(s.PPS())
	if err != nil {
		t.Fatal(err)
	}
	if pps.InitQPMinus26 != 4 || !pps.CuQPDeltaEnabled || !pps.LoopFilterAcrossSlices || pps.Tiles || pps.CabacInitPresent {
		t.Fatalf("pps %+v", pps)
	}
	if NALType(s.VPS()) != NalVPS || NALType(s.SPS()) != NalSPS || NALType(s.PPS()) != NalPPS {
		t.Fatal("NAL types")
	}
}

func TestWrittenSliceHeadersParse(t *testing.T) {
	s := testStream()
	sps, _ := ParseSPS(s.SPS())
	pps, _ := ParsePPS(s.PPS())
	spsm, ppsm := map[uint32]*SPS{0: sps}, map[uint32]*PPS{0: pps}
	for _, c := range []Slice{
		{IDR: true, Type: SliceI, SAOLuma: true, SAOChroma: true},
		{Type: SliceP, PocLsb: 7, Address: 150, QPDelta: -3, TemporalMVP: true},
		{Type: SliceB, PocLsb: 65535, QPDelta: 5, SAOLuma: true},
	} {
		b := s.SliceHeader(c)
		h, _, _, err := ParseSliceHeader(b, spsm, ppsm, nil)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		if IsIDR(h.NalType) != c.IDR || h.Type != c.Type || h.SegmentAddress != c.Address || h.QPDelta != c.QPDelta ||
			h.SAOLuma != c.SAOLuma || h.SAOChroma != c.SAOChroma || h.DataOffset != len(b) {
			t.Fatalf("%+v: parsed %+v (%d bytes)", c, h, len(b))
		}
		if !c.IDR {
			if h.PocLsb != c.PocLsb || h.TemporalMVP != c.TemporalMVP || h.ShortTerm.NumNegative != 1 || h.ShortTerm.DeltaPocS0[0] != -1 ||
				!h.ShortTerm.UsedS0[0] || h.NumRefIdxL0 != 1 {
				t.Fatalf("%+v: references %+v", c, h)
			}
		}
	}
}

func TestEscape(t *testing.T) {
	got := escape(nil, []byte{0, 0, 1, 0, 0, 0, 0, 0, 3, 5})
	want := []byte{0, 0, 3, 1, 0, 0, 3, 0, 0, 3, 0, 3, 5}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if rb, _ := rbsp(got); !bytes.Equal(rb, []byte{0, 0, 1, 0, 0, 0, 0, 0, 3, 5}) {
		t.Fatalf("round trip %v", rb)
	}
}
