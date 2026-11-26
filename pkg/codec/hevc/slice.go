package hevc

import "fmt"

// Slice types.
const (
	SliceB = 0
	SliceP = 1
	SliceI = 2
)

// SliceHeader is a slice segment header (7.3.6.1), with what a decoder
// derives from it.
type SliceHeader struct {
	NalType    int
	TemporalID int

	FirstSliceSegmentInPic bool
	NoOutputOfPriorPics    bool
	PPSID                  uint32
	Dependent              bool
	SegmentAddress         uint32

	Type           int
	PicOutput      bool
	ColourPlaneID  uint32
	PocLsb         uint32
	ShortTermInSPS bool
	ShortTermIdx   int
	// ShortTerm is the short-term RPS the slice uses, its own or the
	// SPS's; ShortTermBits the bits of the slice's own.
	ShortTerm     ShortTermRPS
	ShortTermBits int

	// Long-term pictures: POC LSBs, the MSB cycle when present, used by
	// the current picture or not.
	NumLongTerm           int
	LtPoc                 [32]int32
	LtMSBPresent          [32]bool
	LtMSBCycle            [32]int32 // DeltaPocMsbCycleLt
	LtUsedByCurr          [32]bool
	TemporalMVP           bool
	SAOLuma               bool
	SAOChroma             bool
	NumRefIdxL0           int // active, not minus 1
	NumRefIdxL1           int
	ListModL0             bool
	ListModL1             bool
	ListEntryL0           [16]uint32
	ListEntryL1           [16]uint32
	MvdL1Zero             bool
	CabacInit             bool
	CollocFromL0          bool
	CollocRefIdx          uint32
	FiveMinusMaxMergeCand uint32

	LumaLog2WeightDenom   uint32
	ChromaLog2WeightDenom int32     // the delta
	LumaWeightL0          [16]int32 // delta_luma_weight, 0 when absent
	LumaOffsetL0          [16]int32
	ChromaWeightL0        [16][2]int32 // delta_chroma_weight
	ChromaOffsetL0        [16][2]int32 // ChromaOffsetL0, derived
	LumaWeightL1          [16]int32
	LumaOffsetL1          [16]int32
	ChromaWeightL1        [16][2]int32
	ChromaOffsetL1        [16][2]int32

	QPDelta                int32
	CbQPOffset, CrQPOffset int32
	DeblockingDisabled     bool
	BetaOffsetDiv2         int32
	TcOffsetDiv2           int32
	LoopFilterAcrossSlices bool
	NumEntryPointOffsets   uint32

	// DataOffset is where the slice data starts, in bytes of the NAL unit
	// as it is, emulation prevention bytes included; EPBInHeader counts
	// those before it.
	DataOffset  int
	EPBInHeader int
}

// IsIRAP reports an IRAP picture's NAL type.
func IsIRAP(t int) bool { return t >= NalBLAWLP && t <= NalRsvIRAP23 }

// IsIDR reports an IDR picture's NAL type.
func IsIDR(t int) bool { return t == NalIDRWRADL || t == NalIDRNLP }

// NumPicTotalCurr is the pictures the current one may reference (7-55).
func (h *SliceHeader) NumPicTotalCurr() int {
	n := 0
	for i := range h.ShortTerm.NumNegative {
		if h.ShortTerm.UsedS0[i] {
			n++
		}
	}
	for i := range h.ShortTerm.NumPositive {
		if h.ShortTerm.UsedS1[i] {
			n++
		}
	}
	for i := range h.NumLongTerm {
		if h.LtUsedByCurr[i] {
			n++
		}
	}
	return n
}

// ParseSliceHeader parses the header of a slice segment NAL unit, header
// included. pps and sps are the parameter sets by id; prev is the
// picture's previous independent segment, whose fields a dependent one
// takes.
func ParseSliceHeader(nal []byte, sps map[uint32]*SPS, pps map[uint32]*PPS, prev *SliceHeader) (*SliceHeader, *SPS, *PPS, error) {
	if len(nal) < 3 {
		return nil, nil, nil, ErrSyntax
	}
	b, epb := rbsp(nal)
	r := &reader{b: b, pos: 16}
	h := &SliceHeader{NalType: int(nal[0]>>1) & 0x3f, TemporalID: int(nal[1]&7) - 1}
	h.FirstSliceSegmentInPic = r.flag()
	if IsIRAP(h.NalType) {
		h.NoOutputOfPriorPics = r.flag()
	}
	h.PPSID = r.ueMax(63)
	p := pps[h.PPSID]
	if p == nil {
		return nil, nil, nil, fmt.Errorf("hevc: slice refers to PPS %d, not seen", h.PPSID)
	}
	s := sps[p.SPSID]
	if s == nil {
		return nil, nil, nil, fmt.Errorf("hevc: PPS %d refers to SPS %d, not seen", p.ID, p.SPSID)
	}
	if !h.FirstSliceSegmentInPic {
		if p.DependentSliceSegments {
			h.Dependent = r.flag()
		}
		h.SegmentAddress = r.u(ceilLog2(s.picWidthInCtbs() * s.picHeightInCtbs()))
	}
	if h.Dependent {
		if prev == nil {
			return nil, nil, nil, fmt.Errorf("hevc: a dependent slice segment with no slice before it")
		}
		dep := *prev
		dep.NalType, dep.TemporalID = h.NalType, h.TemporalID
		dep.FirstSliceSegmentInPic, dep.Dependent, dep.SegmentAddress = false, true, h.SegmentAddress
		h = &dep
	} else {
		r.skip(int(p.NumExtraSliceHeaderBits))
		h.Type = int(r.ueMax(2))
		h.PicOutput = true
		if p.OutputFlagPresent {
			h.PicOutput = r.flag()
		}
		if s.SeparateColourPlane {
			h.ColourPlaneID = r.u(2)
		}
		if !IsIDR(h.NalType) {
			h.PocLsb = r.u(int(s.Log2MaxPocLsb))
			h.ShortTermInSPS = r.flag()
			n := len(s.ShortTermRPS)
			if !h.ShortTermInSPS {
				if err := parseShortTermRPS(r, s.ShortTermRPS, n, n, &h.ShortTerm, &h.ShortTermBits); err != nil {
					return nil, nil, nil, err
				}
			} else {
				if n > 1 {
					h.ShortTermIdx = int(r.u(ceilLog2(n)))
				}
				if h.ShortTermIdx >= n {
					return nil, nil, nil, ErrSyntax
				}
				h.ShortTerm = s.ShortTermRPS[h.ShortTermIdx]
			}
			if s.LongTermRefsPresent {
				nsps := 0
				if len(s.LtRefPicPocLsb) > 0 {
					nsps = int(r.ueMax(uint32(len(s.LtRefPicPocLsb))))
				}
				npics := int(r.ueMax(32))
				h.NumLongTerm = nsps + npics
				if h.NumLongTerm > 32 {
					return nil, nil, nil, ErrSyntax
				}
				var msbCycle int32
				for i := range h.NumLongTerm {
					if i < nsps {
						idx := 0
						if len(s.LtRefPicPocLsb) > 1 {
							idx = int(r.u(ceilLog2(len(s.LtRefPicPocLsb))))
						}
						if idx >= len(s.LtRefPicPocLsb) {
							return nil, nil, nil, ErrSyntax
						}
						h.LtPoc[i], h.LtUsedByCurr[i] = int32(s.LtRefPicPocLsb[idx]), s.LtUsedByCurr[idx]
					} else {
						h.LtPoc[i] = int32(r.u(int(s.Log2MaxPocLsb)))
						h.LtUsedByCurr[i] = r.flag()
					}
					h.LtMSBPresent[i] = r.flag()
					if h.LtMSBPresent[i] {
						// 7-52: DeltaPocMsbCycleLt accumulates, but not across
						// the boundary between SPS and slice entries.
						if i == 0 || i == nsps {
							msbCycle = int32(r.ue())
						} else {
							msbCycle += int32(r.ue())
						}
						h.LtMSBCycle[i] = msbCycle
					}
				}
			}
			if s.TemporalMVP {
				h.TemporalMVP = r.flag()
			}
		}
		if s.SAO {
			h.SAOLuma = r.flag()
			h.SAOChroma = r.flag()
		}
		h.NumRefIdxL0, h.NumRefIdxL1 = 0, 0
		if h.Type == SliceP || h.Type == SliceB {
			h.NumRefIdxL0 = int(p.NumRefIdxL0DefaultMinus1) + 1
			if h.Type == SliceB {
				h.NumRefIdxL1 = int(p.NumRefIdxL1DefaultMinus1) + 1
			}
			if r.flag() {
				h.NumRefIdxL0 = int(r.ueMax(14)) + 1
				if h.Type == SliceB {
					h.NumRefIdxL1 = int(r.ueMax(14)) + 1
				}
			}
			total := h.NumPicTotalCurr()
			if p.ListsModificationPresent && total > 1 {
				bits := ceilLog2(total)
				h.ListModL0 = r.flag()
				if h.ListModL0 {
					for i := range h.NumRefIdxL0 {
						h.ListEntryL0[i] = r.u(bits)
					}
				}
				if h.Type == SliceB {
					h.ListModL1 = r.flag()
					if h.ListModL1 {
						for i := range h.NumRefIdxL1 {
							h.ListEntryL1[i] = r.u(bits)
						}
					}
				}
			}
			if h.Type == SliceB {
				h.MvdL1Zero = r.flag()
			}
			if p.CabacInitPresent {
				h.CabacInit = r.flag()
			}
			if h.TemporalMVP {
				h.CollocFromL0 = true
				if h.Type == SliceB {
					h.CollocFromL0 = r.flag()
				}
				if h.CollocFromL0 && h.NumRefIdxL0 > 1 || !h.CollocFromL0 && h.NumRefIdxL1 > 1 {
					h.CollocRefIdx = r.ueMax(14)
				}
			}
			if p.WeightedPred && h.Type == SliceP || p.WeightedBipred && h.Type == SliceB {
				predWeightTable(r, s, h)
			}
			h.FiveMinusMaxMergeCand = r.ueMax(4)
		}
		h.QPDelta = r.se()
		if p.SliceChromaQPOffsetsPresent {
			h.CbQPOffset, h.CrQPOffset = r.se(), r.se()
		}
		override := false
		if p.DeblockingOverrideEnabled {
			override = r.flag()
		}
		h.DeblockingDisabled, h.BetaOffsetDiv2, h.TcOffsetDiv2 = p.DeblockingDisabled, p.BetaOffsetDiv2, p.TcOffsetDiv2
		if override {
			h.DeblockingDisabled = r.flag()
			if !h.DeblockingDisabled {
				h.BetaOffsetDiv2, h.TcOffsetDiv2 = r.se(), r.se()
			}
		}
		h.LoopFilterAcrossSlices = p.LoopFilterAcrossSlices
		if p.LoopFilterAcrossSlices && (h.SAOLuma || h.SAOChroma || !h.DeblockingDisabled) {
			h.LoopFilterAcrossSlices = r.flag()
		}
	}
	h.NumEntryPointOffsets = 0
	if p.Tiles || p.EntropyCodingSync {
		h.NumEntryPointOffsets = r.ue()
		if h.NumEntryPointOffsets > 0 {
			bits := int(r.ueMax(31)) + 1
			r.skip(bits * int(h.NumEntryPointOffsets))
		}
	}
	if p.SliceSegmentHeaderExtensionPresent {
		r.skip(8 * int(r.ueMax(256)))
	}
	// byte_alignment(): a one, then zeros to the byte.
	if !r.flag() {
		return nil, nil, nil, fmt.Errorf("hevc: slice header: %w (no alignment bit)", ErrSyntax)
	}
	for r.pos&7 != 0 {
		r.u(1)
	}
	if r.err != nil {
		return nil, nil, nil, fmt.Errorf("hevc: slice header: %w", r.err)
	}
	off := r.pos / 8
	h.EPBInHeader = 0
	for _, e := range epb {
		if e < off+h.EPBInHeader {
			h.EPBInHeader++
		}
	}
	h.DataOffset = off + h.EPBInHeader
	return h, s, p, nil
}

// predWeightTable parses pred_weight_table() (7.3.6.3) for a 4:2:0
// stream without high-precision offsets.
func predWeightTable(r *reader, s *SPS, h *SliceHeader) {
	h.LumaLog2WeightDenom = r.ueMax(7)
	h.ChromaLog2WeightDenom = r.se()
	chromaDenom := int32(h.LumaLog2WeightDenom) + h.ChromaLog2WeightDenom
	list := func(n int, lw, lo *[16]int32, cw, co *[16][2]int32) {
		var lf, cf [16]bool
		for i := range n {
			lf[i] = r.flag()
		}
		for i := range n {
			cf[i] = r.flag()
		}
		for i := range n {
			if lf[i] {
				lw[i] = r.se()
				lo[i] = r.se()
			}
			if cf[i] {
				for j := range 2 {
					cw[i][j] = r.se()
					delta := r.se()
					// 7-56 with WpOffsetHalfRangeC = 128 << (BitDepthC - 8),
					// in the 8-bit units VA-API passes.
					weight := int32(1<<uint(chromaDenom)) + cw[i][j]
					half := int32(128)
					off := delta - ((half * weight) >> uint(chromaDenom)) + half
					co[i][j] = max(-half, min(half-1, off))
				}
			}
		}
	}
	list(h.NumRefIdxL0, &h.LumaWeightL0, &h.LumaOffsetL0, &h.ChromaWeightL0, &h.ChromaOffsetL0)
	if h.Type == SliceB {
		list(h.NumRefIdxL1, &h.LumaWeightL1, &h.LumaOffsetL1, &h.ChromaWeightL1, &h.ChromaOffsetL1)
	}
}
