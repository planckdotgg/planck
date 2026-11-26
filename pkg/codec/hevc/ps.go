package hevc

import "fmt"

// NAL unit types (Table 7-1).
const (
	NalTrailN    = 0
	NalRASLR     = 9
	NalBLAWLP    = 16
	NalIDRWRADL  = 19
	NalIDRNLP    = 20
	NalCRA       = 21
	NalRsvIRAP23 = 23
	NalVPS       = 32
	NalSPS       = 33
	NalPPS       = 34
	NalAUD       = 35
)

// ShortTermRPS is a short-term reference picture set (7.4.8): the POC
// deltas before and after the current picture, and which the current
// picture uses.
type ShortTermRPS struct {
	NumNegative, NumPositive int
	DeltaPocS0, DeltaPocS1   [16]int32
	UsedS0, UsedS1           [16]bool
}

func (s *ShortTermRPS) numDelta() int { return s.NumNegative + s.NumPositive }

// ScalingList holds scaling factors in coded (up-right diagonal) order,
// ScalingList[sizeId][matrixId][i] of 7.4.5, and the DC factors of the
// 16x16 and 32x32 lists.
type ScalingList struct {
	List4  [6][16]uint8
	List8  [6][64]uint8
	List16 [6][64]uint8
	List32 [6][64]uint8 // matrixId 0 and 3 are used
	DC16   [6]uint8
	DC32   [6]uint8
}

// SPS is a sequence parameter set, the fields a decoder needs.
type SPS struct {
	ID                                    uint32
	MaxSubLayersMinus1                    uint32
	ChromaFormatIdc                       uint32
	SeparateColourPlane                   bool
	Width, Height                         uint32
	ConfLeft, ConfRight, ConfTop, ConfBot uint32
	BitDepthLuma, BitDepthChroma          uint32 // minus 8
	Log2MaxPocLsb                         uint32 // log2_max_pic_order_cnt_lsb_minus4 + 4
	MaxDecPicBufferingMinus1              uint32 // of the highest sub-layer
	MaxNumReorder                         uint32
	Log2MinCbMinus3, Log2DiffMaxMinCb     uint32
	Log2MinTbMinus2, Log2DiffMaxMinTb     uint32
	MaxTHDepthInter, MaxTHDepthIntra      uint32
	ScalingListEnabled                    bool
	ScalingList                           ScalingList
	AMP, SAO                              bool
	PCM                                   bool
	PCMBitDepthLuma, PCMBitDepthChroma    uint32 // minus 1
	Log2MinPCMMinus3, Log2DiffMaxMinPCM   uint32
	PCMLoopFilterDisabled                 bool
	ShortTermRPS                          []ShortTermRPS
	LongTermRefsPresent                   bool
	LtRefPicPocLsb                        []uint32
	LtUsedByCurr                          []bool
	TemporalMVP                           bool
	StrongIntraSmoothing                  bool
}

// CtbLog2 is CtbLog2SizeY; PicSizeInCtbs the picture's CTBs.
func (s *SPS) CtbLog2() uint32 { return s.Log2MinCbMinus3 + 3 + s.Log2DiffMaxMinCb }

func (s *SPS) picWidthInCtbs() int {
	c := 1 << s.CtbLog2()
	return (int(s.Width) + c - 1) / c
}

func (s *SPS) picHeightInCtbs() int {
	c := 1 << s.CtbLog2()
	return (int(s.Height) + c - 1) / c
}

// VisibleSize is the picture's size inside its conformance window.
func (s *SPS) VisibleSize() (w, h int) {
	sub := uint32(1)
	subH := uint32(1)
	if s.ChromaFormatIdc == 1 {
		sub, subH = 2, 2
	} else if s.ChromaFormatIdc == 2 {
		sub = 2
	}
	return int(s.Width - sub*(s.ConfLeft+s.ConfRight)), int(s.Height - subH*(s.ConfTop+s.ConfBot))
}

// ParseSPS parses an SPS NAL unit, header included.
func ParseSPS(nal []byte) (*SPS, error) {
	b, _ := rbsp(nal)
	if len(b) < 3 {
		return nil, ErrSyntax
	}
	r := &reader{b: b, pos: 16}
	s := &SPS{}
	r.u(4) // sps_video_parameter_set_id
	s.MaxSubLayersMinus1 = r.u(3)
	r.u(1)
	profileTierLevel(r, s.MaxSubLayersMinus1)
	s.ID = r.ueMax(15)
	s.ChromaFormatIdc = r.ueMax(3)
	if s.ChromaFormatIdc == 3 {
		s.SeparateColourPlane = r.flag()
	}
	s.Width, s.Height = r.ue(), r.ue()
	if r.flag() {
		s.ConfLeft, s.ConfRight, s.ConfTop, s.ConfBot = r.ue(), r.ue(), r.ue(), r.ue()
	}
	s.BitDepthLuma, s.BitDepthChroma = r.ueMax(8), r.ueMax(8)
	s.Log2MaxPocLsb = r.ueMax(12) + 4
	allLayers := r.flag()
	first := s.MaxSubLayersMinus1
	if allLayers {
		first = 0
	}
	for i := first; i <= s.MaxSubLayersMinus1; i++ {
		s.MaxDecPicBufferingMinus1 = r.ueMax(15)
		s.MaxNumReorder = r.ue()
		r.ue() // sps_max_latency_increase_plus1
	}
	s.Log2MinCbMinus3, s.Log2DiffMaxMinCb = r.ueMax(3), r.ueMax(3)
	s.Log2MinTbMinus2, s.Log2DiffMaxMinTb = r.ueMax(3), r.ueMax(3)
	s.MaxTHDepthInter, s.MaxTHDepthIntra = r.ueMax(4), r.ueMax(4)
	s.ScalingListEnabled = r.flag()
	if s.ScalingListEnabled {
		s.ScalingList = DefaultScalingList()
		if r.flag() {
			scalingListData(r, &s.ScalingList)
		}
	}
	s.AMP, s.SAO = r.flag(), r.flag()
	s.PCM = r.flag()
	if s.PCM {
		s.PCMBitDepthLuma, s.PCMBitDepthChroma = r.u(4), r.u(4)
		s.Log2MinPCMMinus3, s.Log2DiffMaxMinPCM = r.ue(), r.ue()
		s.PCMLoopFilterDisabled = r.flag()
	}
	n := int(r.ueMax(64))
	s.ShortTermRPS = make([]ShortTermRPS, n)
	for i := range n {
		if err := parseShortTermRPS(r, s.ShortTermRPS, i, n, &s.ShortTermRPS[i], nil); err != nil {
			return nil, err
		}
	}
	s.LongTermRefsPresent = r.flag()
	if s.LongTermRefsPresent {
		n := int(r.ueMax(32))
		for range n {
			s.LtRefPicPocLsb = append(s.LtRefPicPocLsb, r.u(int(s.Log2MaxPocLsb)))
			s.LtUsedByCurr = append(s.LtUsedByCurr, r.flag())
		}
	}
	s.TemporalMVP, s.StrongIntraSmoothing = r.flag(), r.flag()
	if r.err != nil {
		return nil, fmt.Errorf("hevc: SPS: %w", r.err)
	}
	if s.ChromaFormatIdc != 1 {
		return nil, fmt.Errorf("hevc: SPS: chroma_format_idc %d: only 4:2:0 is supported", s.ChromaFormatIdc)
	}
	return s, nil
}

func profileTierLevel(r *reader, maxSubLayersMinus1 uint32) {
	r.skip(88) // general profile space, tier, idc, compatibility, constraints
	r.skip(8)  // general_level_idc
	var profile, level [8]bool
	for i := range maxSubLayersMinus1 {
		profile[i], level[i] = r.flag(), r.flag()
	}
	if maxSubLayersMinus1 > 0 {
		r.skip(2 * (8 - int(maxSubLayersMinus1)))
	}
	for i := range maxSubLayersMinus1 {
		if profile[i] {
			r.skip(88)
		}
		if level[i] {
			r.skip(8)
		}
	}
}

// parseShortTermRPS parses st_ref_pic_set(idx) of a parameter set with
// num sets (idx == num in a slice header) into out, from sets before it.
// bits, if not nil, gets the bits it read.
func parseShortTermRPS(r *reader, sets []ShortTermRPS, idx, num int, out *ShortTermRPS, bits *int) error {
	start := r.pos
	defer func() {
		if bits != nil {
			*bits = r.pos - start
		}
	}()
	inter := idx != 0 && r.flag()
	if inter {
		delta := 1
		if idx == num {
			delta = int(r.ueMax(uint32(idx-1))) + 1
		}
		if delta > idx {
			return ErrSyntax
		}
		ref := &sets[idx-delta]
		sign := r.flag()
		abs := int32(r.ueMax(1<<15-1)) + 1
		deltaRps := abs
		if sign {
			deltaRps = -abs
		}
		n := ref.numDelta()
		var used, useDelta [33]bool
		for j := 0; j <= n; j++ {
			used[j] = r.flag()
			useDelta[j] = true
			if !used[j] {
				useDelta[j] = r.flag()
			}
		}
		// 7.4.8, equations 7-61 and 7-62.
		i := 0
		for j := ref.NumPositive - 1; j >= 0; j-- {
			d := ref.DeltaPocS1[j] + deltaRps
			if d < 0 && useDelta[ref.NumNegative+j] {
				if i >= 16 {
					return ErrSyntax
				}
				out.DeltaPocS0[i], out.UsedS0[i] = d, used[ref.NumNegative+j]
				i++
			}
		}
		if deltaRps < 0 && useDelta[n] {
			if i >= 16 {
				return ErrSyntax
			}
			out.DeltaPocS0[i], out.UsedS0[i] = deltaRps, used[n]
			i++
		}
		for j := 0; j < ref.NumNegative; j++ {
			d := ref.DeltaPocS0[j] + deltaRps
			if d < 0 && useDelta[j] {
				if i >= 16 {
					return ErrSyntax
				}
				out.DeltaPocS0[i], out.UsedS0[i] = d, used[j]
				i++
			}
		}
		out.NumNegative = i
		i = 0
		for j := ref.NumNegative - 1; j >= 0; j-- {
			d := ref.DeltaPocS0[j] + deltaRps
			if d > 0 && useDelta[j] {
				if i >= 16 {
					return ErrSyntax
				}
				out.DeltaPocS1[i], out.UsedS1[i] = d, used[j]
				i++
			}
		}
		if deltaRps > 0 && useDelta[n] {
			if i >= 16 {
				return ErrSyntax
			}
			out.DeltaPocS1[i], out.UsedS1[i] = deltaRps, used[n]
			i++
		}
		for j := 0; j < ref.NumPositive; j++ {
			d := ref.DeltaPocS1[j] + deltaRps
			if d > 0 && useDelta[ref.NumNegative+j] {
				if i >= 16 {
					return ErrSyntax
				}
				out.DeltaPocS1[i], out.UsedS1[i] = d, used[ref.NumNegative+j]
				i++
			}
		}
		out.NumPositive = i
		return r.err
	}
	out.NumNegative, out.NumPositive = int(r.ueMax(16)), int(r.ueMax(16))
	if out.NumNegative+out.NumPositive > 16 {
		return ErrSyntax
	}
	poc := int32(0)
	for i := range out.NumNegative {
		poc -= int32(r.ueMax(1<<15-1)) + 1
		out.DeltaPocS0[i], out.UsedS0[i] = poc, r.flag()
	}
	poc = 0
	for i := range out.NumPositive {
		poc += int32(r.ueMax(1<<15-1)) + 1
		out.DeltaPocS1[i], out.UsedS1[i] = poc, r.flag()
	}
	return r.err
}

// PPS is a picture parameter set, the fields a decoder needs.
type PPS struct {
	ID, SPSID                               uint32
	DependentSliceSegments                  bool
	OutputFlagPresent                       bool
	NumExtraSliceHeaderBits                 uint32
	SignDataHiding, CabacInitPresent        bool
	NumRefIdxL0DefaultMinus1                uint32
	NumRefIdxL1DefaultMinus1                uint32
	InitQPMinus26                           int32
	ConstrainedIntraPred, TransformSkip     bool
	CuQPDeltaEnabled                        bool
	DiffCuQPDeltaDepth                      uint32
	CbQPOffset, CrQPOffset                  int32
	SliceChromaQPOffsetsPresent             bool
	WeightedPred, WeightedBipred            bool
	TransquantBypass                        bool
	Tiles, EntropyCodingSync                bool
	NumTileColumnsMinus1, NumTileRowsMinus1 uint32
	UniformSpacing                          bool
	ColumnWidthMinus1                       []uint32
	RowHeightMinus1                         []uint32
	LoopFilterAcrossTiles                   bool
	LoopFilterAcrossSlices                  bool
	DeblockingOverrideEnabled               bool
	DeblockingDisabled                      bool
	BetaOffsetDiv2, TcOffsetDiv2            int32
	ScalingListPresent                      bool
	ScalingList                             ScalingList
	ListsModificationPresent                bool
	Log2ParallelMergeLevelMinus2            uint32
	SliceSegmentHeaderExtensionPresent      bool
}

// ParsePPS parses a PPS NAL unit, header included.
func ParsePPS(nal []byte) (*PPS, error) {
	b, _ := rbsp(nal)
	if len(b) < 3 {
		return nil, ErrSyntax
	}
	r := &reader{b: b, pos: 16}
	p := &PPS{}
	p.ID, p.SPSID = r.ueMax(63), r.ueMax(15)
	p.DependentSliceSegments, p.OutputFlagPresent = r.flag(), r.flag()
	p.NumExtraSliceHeaderBits = r.u(3)
	p.SignDataHiding, p.CabacInitPresent = r.flag(), r.flag()
	p.NumRefIdxL0DefaultMinus1, p.NumRefIdxL1DefaultMinus1 = r.ueMax(14), r.ueMax(14)
	p.InitQPMinus26 = r.se()
	p.ConstrainedIntraPred, p.TransformSkip = r.flag(), r.flag()
	p.CuQPDeltaEnabled = r.flag()
	if p.CuQPDeltaEnabled {
		p.DiffCuQPDeltaDepth = r.ue()
	}
	p.CbQPOffset, p.CrQPOffset = r.se(), r.se()
	p.SliceChromaQPOffsetsPresent = r.flag()
	p.WeightedPred, p.WeightedBipred = r.flag(), r.flag()
	p.TransquantBypass = r.flag()
	p.Tiles, p.EntropyCodingSync = r.flag(), r.flag()
	if p.Tiles {
		p.NumTileColumnsMinus1, p.NumTileRowsMinus1 = r.ueMax(18), r.ueMax(20)
		p.UniformSpacing = r.flag()
		if !p.UniformSpacing {
			for range p.NumTileColumnsMinus1 {
				p.ColumnWidthMinus1 = append(p.ColumnWidthMinus1, r.ue())
			}
			for range p.NumTileRowsMinus1 {
				p.RowHeightMinus1 = append(p.RowHeightMinus1, r.ue())
			}
		}
		p.LoopFilterAcrossTiles = r.flag()
	}
	p.LoopFilterAcrossSlices = r.flag()
	if r.flag() { // deblocking_filter_control_present_flag
		p.DeblockingOverrideEnabled = r.flag()
		p.DeblockingDisabled = r.flag()
		if !p.DeblockingDisabled {
			p.BetaOffsetDiv2, p.TcOffsetDiv2 = r.se(), r.se()
		}
	}
	p.ScalingListPresent = r.flag()
	if p.ScalingListPresent {
		p.ScalingList = DefaultScalingList()
		scalingListData(r, &p.ScalingList)
	}
	p.ListsModificationPresent = r.flag()
	p.Log2ParallelMergeLevelMinus2 = r.ue()
	p.SliceSegmentHeaderExtensionPresent = r.flag()
	if r.err != nil {
		return nil, fmt.Errorf("hevc: PPS: %w", r.err)
	}
	return p, nil
}

// TileSizes are the tiles' column widths and row heights in CTBs, minus
// 1, for uniform spacing too (6.5.1).
func (p *PPS) TileSizes(s *SPS) (cols, rows []uint32) {
	if !p.UniformSpacing {
		cols = append(cols, p.ColumnWidthMinus1...)
		rows = append(rows, p.RowHeightMinus1...)
		// The last column and row take what is left; VA-API wants them too.
		w, h := uint32(s.picWidthInCtbs()), uint32(s.picHeightInCtbs())
		for _, c := range cols {
			w -= c + 1
		}
		for _, r := range rows {
			h -= r + 1
		}
		return append(cols, w-1), append(rows, h-1)
	}
	nc, nr := int(p.NumTileColumnsMinus1)+1, int(p.NumTileRowsMinus1)+1
	w, h := s.picWidthInCtbs(), s.picHeightInCtbs()
	for i := range nc {
		cols = append(cols, uint32((i+1)*w/nc-i*w/nc-1))
	}
	for j := range nr {
		rows = append(rows, uint32((j+1)*h/nr-j*h/nr-1))
	}
	return cols, rows
}

var defaultIntra8 = [64]uint8{
	16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 16, 17, 16, 17, 18, 17, 18, 18, 17, 18, 21, 19, 20,
	21, 20, 19, 21, 24, 22, 22, 24, 24, 22, 22, 24, 25, 25, 27, 30, 27, 25, 25, 29, 31, 35, 35, 31,
	29, 36, 41, 44, 41, 36, 47, 54, 54, 47, 65, 70, 65, 88, 88, 115,
}

var defaultInter8 = [64]uint8{
	16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 17, 18, 18, 18, 18, 18, 18, 20, 20, 20,
	20, 20, 20, 20, 24, 24, 24, 24, 24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 28, 28, 28, 28, 28,
	28, 33, 33, 33, 33, 33, 41, 41, 41, 41, 54, 54, 54, 71, 71, 91,
}

// DefaultScalingList is Table 7-5 and 7-6, in coded order.
func DefaultScalingList() ScalingList {
	var l ScalingList
	for m := range 6 {
		for i := range l.List4[m] {
			l.List4[m][i] = 16
		}
		d := defaultIntra8
		if m >= 3 {
			d = defaultInter8
		}
		l.List8[m], l.List16[m], l.List32[m] = d, d, d
		l.DC16[m], l.DC32[m] = 16, 16
	}
	return l
}

// scalingListData parses scaling_list_data() (7.3.4) into l, which holds
// the defaults.
func scalingListData(r *reader, l *ScalingList) {
	list := func(size, m int) []uint8 {
		switch size {
		case 0:
			return l.List4[m][:]
		case 1:
			return l.List8[m][:]
		case 2:
			return l.List16[m][:]
		}
		return l.List32[m][:]
	}
	dc := func(size, m int) *uint8 {
		if size == 2 {
			return &l.DC16[m]
		}
		return &l.DC32[m]
	}
	def := DefaultScalingList()
	for size := range 4 {
		step := 1
		if size == 3 {
			step = 3
		}
		for m := 0; m < 6; m += step {
			if !r.flag() { // scaling_list_pred_mode_flag
				delta := int(r.ue()) * step
				if delta > m {
					r.err = ErrSyntax
					return
				}
				ref := m - delta
				if delta == 0 {
					copy(list(size, m), defList(&def, size, m))
					if size >= 2 {
						*dc(size, m) = 16
					}
				} else {
					copy(list(size, m), list(size, ref))
					if size >= 2 {
						*dc(size, m) = *dc(size, ref)
					}
				}
				continue
			}
			next := int32(8)
			n := min(64, 1<<(4+size*2))
			if size >= 2 {
				next = r.se() + 8
				*dc(size, m) = uint8(next)
			}
			out := list(size, m)
			for i := range n {
				next = (next + r.se() + 256) % 256
				out[i] = uint8(next)
			}
		}
	}
	// 32x32 chroma lists (matrixId 1, 2, 4, 5) are those of 16x16 in
	// version 1 streams (7.4.5, ChromaArrayType 3 only).
}

func defList(d *ScalingList, size, m int) []uint8 {
	switch size {
	case 0:
		return d.List4[m][:]
	case 1:
		return d.List8[m][:]
	case 2:
		return d.List16[m][:]
	}
	return d.List32[m][:]
}
