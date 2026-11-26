package vaapi

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/codec/hevc"
	"github.com/planckdotgg/planck/pkg/decode"
	"github.com/planckdotgg/planck/pkg/decode/internal/hostbuf"
	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/gpu"
)

// picParamHEVC is VAPictureParameterBufferHEVC (604 bytes).
type picParamHEVC struct {
	CurrPic                          pictureHEVC
	Refs                             [15]pictureHEVC
	Width, Height                    uint16
	PicFields                        uint32
	MaxDecPicBufferingMinus1         uint8
	BitDepthLumaMinus8               uint8
	BitDepthChromaMinus8             uint8
	PCMBitDepthLumaMinus1            uint8
	PCMBitDepthChromaMinus1          uint8
	Log2MinCbMinus3                  uint8
	Log2DiffMaxMinCb                 uint8
	Log2MinTbMinus2                  uint8
	Log2DiffMaxMinTb                 uint8
	Log2MinPCMMinus3                 uint8
	Log2DiffMaxMinPCM                uint8
	MaxTHDepthIntra, MaxTHDepthInter uint8
	InitQPMinus26                    int8
	DiffCuQPDeltaDepth               uint8
	CbQPOffset, CrQPOffset           int8
	Log2ParallelMergeLevelMinus2     uint8
	NumTileColumnsMinus1             uint8
	NumTileRowsMinus1                uint8
	ColumnWidthMinus1                [19]uint16
	RowHeightMinus1                  [21]uint16
	SliceParsingFields               uint32
	Log2MaxPocLsbMinus4              uint8
	NumShortTermRPS                  uint8
	NumLongTermRefPicSPS             uint8
	NumRefIdxL0DefaultMinus1         uint8
	NumRefIdxL1DefaultMinus1         uint8
	BetaOffsetDiv2, TcOffsetDiv2     int8
	NumExtraSliceHeaderBits          uint8
	STRPSBits                        uint32
	_                                [8]uint32
}

// pictureHEVC is VAPictureHEVC.
type pictureHEVC struct {
	ID    uint32
	POC   int32
	Flags uint32
	_     [4]uint32
}

const (
	picInvalid       = 0x1
	picLongTerm      = 0x8
	picStCurrBefore  = 0x10
	picStCurrAfter   = 0x20
	picLtCurr        = 0x40
	maxReferenceList = 15
)

// sliceParamHEVC is VASliceParameterBufferHEVC (264 bytes).
type sliceParamHEVC struct {
	DataSize, DataOffset, DataFlag, DataByteOffset uint32
	SegmentAddress                                 uint32
	RefPicList                                     [2][15]uint8
	LongSliceFlags                                 uint32
	CollocatedRefIdx                               uint8
	NumRefIdxL0ActiveMinus1                        uint8
	NumRefIdxL1ActiveMinus1                        uint8
	QPDelta, CbQPOffset, CrQPOffset                int8
	BetaOffsetDiv2, TcOffsetDiv2                   int8
	LumaLog2WeightDenom                            uint8
	DeltaChromaLog2WeightDenom                     int8
	DeltaLumaWeightL0                              [15]int8
	LumaOffsetL0                                   [15]int8
	DeltaChromaWeightL0                            [15][2]int8
	ChromaOffsetL0                                 [15][2]int8
	DeltaLumaWeightL1                              [15]int8
	LumaOffsetL1                                   [15]int8
	DeltaChromaWeightL1                            [15][2]int8
	ChromaOffsetL1                                 [15][2]int8
	FiveMinusMaxNumMergeCand                       uint8
	NumEntryPointOffsets                           uint16
	EntryOffsetToSubsetArray                       uint16
	DataNumEmuPrevnBytes                           uint16
	_                                              [2]uint32
}

// iqMatrixHEVC is VAIQMatrixBufferHEVC (1016 bytes).
type iqMatrixHEVC struct {
	List4  [6][16]uint8
	List8  [6][64]uint8
	List16 [6][64]uint8
	List32 [2][64]uint8
	DC16   [6]uint8
	DC32   [2]uint8
	_      [4]uint32
}

// surface is one VA surface: the picture it holds while it is a
// reference (dpb) and while pictures of it are out (out).
type surface struct {
	id       uint32
	poc      int32
	longTerm bool
	dpb      bool
	out      int
	// dmabuf is the exported descriptor.
	dmabuf   capture.DMABuf
	exported bool
}

// HEVCDecoder decodes Main and Main 10 HEVC with VA-API.
type HEVCDecoder struct {
	d       *display
	accepts func(uint32, uint64) bool

	sps map[uint32]*hevc.SPS
	pps map[uint32]*hevc.PPS
	poc hevc.POC
	// afterEOS tells that the next picture begins a coded video sequence.
	afterEOS bool

	config, context uint32
	profile         int32
	width, height   int // coded
	tenBit          bool
	surfaces        []*surface
	mu              sync.Mutex // surfaces' out counts

	// copy, once DMA-BUFs are not wanted or cannot be exported.
	copy    bool
	pool    *hostbuf.Pool
	pools   []*hostbuf.Pool
	image   vaImage
	imaged  bool
	buffers []uint32

	// C-side arguments.
	pic   picParamHEVC
	slice sliceParamHEVC
	iq    iqMatrixHEVC
	desc  drmPrimeDescriptor
}

var serials atomic.Uint64

// OpenHEVC makes an HEVC decoder on the first render node that takes
// HEVC, preferring t.Adapter's vendor.
func OpenHEVC(t decode.Target, cfg decode.Config) (*HEVCDecoder, error) {
	if cfg.Codec != encode.HEVC {
		return nil, fmt.Errorf("%w: %s", decode.ErrUnsupported, cfg.Codec)
	}
	d, err := openDisplay(t.Adapter.Vendor)
	if err != nil {
		return nil, err
	}
	p := d.profiles()
	if !p[vaProfileHEVCMain] {
		d.close()
		return nil, fmt.Errorf("%w: %s (%s) decodes no HEVC", decode.ErrUnsupported, d.node.Path, d.vendor)
	}
	return &HEVCDecoder{d: d, accepts: t.Accepts, sps: map[uint32]*hevc.SPS{}, pps: map[uint32]*hevc.PPS{},
		config: vaInvalidID, context: vaInvalidID, afterEOS: true, copy: t.Accepts == nil}, nil
}

// Vendor is the VA driver's vendor string.
func (v *HEVCDecoder) Vendor() string { return v.d.vendor }

// slice is one slice segment of the access unit.
type sliceNAL struct {
	nal []byte
	h   *hevc.SliceHeader
}

// Decode decodes one access unit.
func (v *HEVCDecoder) Decode(au []byte, pts int64) (decode.Frame, error) {
	start := time.Now()
	var slices []sliceNAL
	var s *hevc.SPS
	var p *hevc.PPS
	var prev *hevc.SliceHeader
	for _, nal := range hevc.NALUnits(au) {
		if len(nal) < 2 {
			continue
		}
		switch t := hevc.NALType(nal); {
		case t == hevc.NalSPS:
			sps, err := hevc.ParseSPS(nal)
			if err != nil {
				return decode.Frame{}, err
			}
			v.sps[sps.ID] = sps
		case t == hevc.NalPPS:
			pps, err := hevc.ParsePPS(nal)
			if err != nil {
				return decode.Frame{}, err
			}
			v.pps[pps.ID] = pps
		case t == 36 || t == 37: // end of sequence, end of bitstream
			v.afterEOS = true
		case t < hevc.NalVPS && (t <= hevc.NalRASLR || hevc.IsIRAP(t)):
			h, hs, hp, err := hevc.ParseSliceHeader(nal, v.sps, v.pps, prev)
			if err != nil {
				return decode.Frame{}, err
			}
			if len(slices) == 0 && !h.FirstSliceSegmentInPic {
				return decode.Frame{}, errors.New("vaapi: access unit starts in the middle of a picture")
			}
			if !h.Dependent {
				prev = h
			}
			if len(slices) == 0 {
				s, p = hs, hp
			}
			slices = append(slices, sliceNAL{nal, h})
		}
	}
	if len(slices) == 0 {
		return decode.Frame{}, decode.ErrNoFrame
	}
	if err := v.ensureContext(s); err != nil {
		return decode.Frame{}, err
	}
	first := slices[0].h
	noRasl := hevc.IsIDR(first.NalType) || first.NalType >= hevc.NalBLAWLP && first.NalType <= hevc.NalBLAWLP+2 || v.afterEOS
	if first.NalType >= 8 && first.NalType <= 9 && v.afterEOS {
		return decode.Frame{}, decode.ErrNoFrame // RASL after a CRA that began the stream
	}
	if !hevc.IsIRAP(first.NalType) && !v.poc.Seen() {
		return decode.Frame{}, decode.ErrNoFrame // waiting for the first random access point
	}
	v.afterEOS = false
	poc := v.poc.Next(first, s, hevc.IsIRAP(first.NalType) && noRasl)
	refs, err := v.applyRPS(first, s, poc, hevc.IsIRAP(first.NalType) && noRasl)
	if err != nil {
		return decode.Frame{}, err
	}
	cur := v.freeSurface()
	if cur == nil {
		return decode.Frame{}, errors.New("vaapi: every surface is a reference or still shown")
	}
	v.fillPicture(s, p, first, cur, poc, refs)
	if err := v.render(s, p, slices, cur, refs); err != nil {
		return decode.Frame{}, err
	}
	cur.poc, cur.longTerm, cur.dpb = poc, false, true
	frame, err := v.output(cur, s, pts)
	if err != nil {
		return decode.Frame{}, err
	}
	frame.Latency = time.Since(start)
	return frame, nil
}

// ensureContext makes the config, surfaces and context for s's size and
// depth, anew when they change.
func (v *HEVCDecoder) ensureContext(s *hevc.SPS) error {
	tenBit := s.BitDepthLuma > 0
	w, h := int(s.Width), int(s.Height)
	if v.context != vaInvalidID && w == v.width && h == v.height && tenBit == v.tenBit {
		return nil
	}
	v.destroyContext()
	profile, rt, fourcc := int32(vaProfileHEVCMain), uint32(vaRTFormatYUV420), uint32(vaFourccNV12)
	if tenBit {
		profile, rt, fourcc = vaProfileHEVCMain10, vaRTFormatYUV420_10, vaFourccP010
		if !v.d.profiles()[vaProfileHEVCMain10] {
			return fmt.Errorf("%w: %s decodes no HEVC Main 10", decode.ErrUnsupported, v.d.vendor)
		}
	}
	attr := [2]uint32{vaConfigAttribRTFormat, rt}
	if err := status("vaCreateConfig", call(lib.createConfig, v.d.dpy, uintptr(profile), vaEntrypointVLD, uintptr(unsafe.Pointer(&attr[0])), 1, uintptr(unsafe.Pointer(&v.config)))); err != nil {
		v.config = vaInvalidID
		return err
	}
	// The DPB, the picture being decoded, and the pictures the player
	// holds: one on screen, one waiting, and one being replaced.
	n := int(s.MaxDecPicBufferingMinus1) + 1 + 1 + 4
	ids := make([]uint32, n)
	attrs := [1]surfaceAttrib{{typ: vaSurfaceAttribPixelFormat, flags: vaSurfaceAttribSettable, valueType: vaGenericValueInteger, value: int64(fourcc)}}
	if err := status("vaCreateSurfaces", call(lib.createSurfaces, v.d.dpy, uintptr(rt), uintptr(w), uintptr(h), uintptr(unsafe.Pointer(&ids[0])), uintptr(n), uintptr(unsafe.Pointer(&attrs[0])), 1)); err != nil {
		return err
	}
	v.surfaces = v.surfaces[:0]
	for _, id := range ids {
		v.surfaces = append(v.surfaces, &surface{id: id})
	}
	if err := status("vaCreateContext", call(lib.createContext, v.d.dpy, uintptr(v.config), uintptr(w), uintptr(h), vaProgressive, uintptr(unsafe.Pointer(&ids[0])), uintptr(n), uintptr(unsafe.Pointer(&v.context)))); err != nil {
		v.context = vaInvalidID
		return err
	}
	v.profile, v.width, v.height, v.tenBit = profile, w, h, tenBit
	v.imaged = false
	return nil
}

func (v *HEVCDecoder) destroyContext() {
	if v.context != vaInvalidID {
		call(lib.destroyContext, v.d.dpy, uintptr(v.context))
		v.context = vaInvalidID
	}
	v.mu.Lock()
	for _, s := range v.surfaces {
		s.dpb = false
		closeFDs(&s.dmabuf)
		s.exported = false
	}
	if len(v.surfaces) > 0 {
		ids := make([]uint32, len(v.surfaces))
		for i, s := range v.surfaces {
			ids[i] = s.id
		}
		call(lib.destroySurfaces, v.d.dpy, uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	}
	v.surfaces = nil
	v.mu.Unlock()
	if v.imaged {
		call(lib.destroyImage, v.d.dpy, uintptr(v.image.id))
		v.imaged = false
	}
	if v.config != vaInvalidID {
		call(lib.destroyConfig, v.d.dpy, uintptr(v.config))
		v.config = vaInvalidID
	}
}

// refSet is the current picture's RPS as surfaces (8.3.2): the lists the
// current picture uses, then the pictures kept for later ones.
type refSet struct {
	stBefore, stAfter, ltCurr []*surface
	foll                      []*surface
}

// applyRPS finds the RPS's pictures in the DPB and drops every other
// picture from it. A reference the stream lost stands in as the nearest
// picture kept, so that decoding goes on, damaged, to the next keyframe.
func (v *HEVCDecoder) applyRPS(h *hevc.SliceHeader, s *hevc.SPS, poc int32, irapNoRasl bool) (refSet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var rs refSet
	if irapNoRasl {
		for _, sf := range v.surfaces {
			sf.dpb = false
		}
		return rs, nil
	}
	keep := map[*surface]bool{}
	find := func(want int32, lsbOnly bool) *surface {
		mask := int32(1)<<s.Log2MaxPocLsb - 1
		for _, sf := range v.surfaces {
			if !sf.dpb {
				continue
			}
			if lsbOnly && sf.poc&mask == want || !lsbOnly && sf.poc == want {
				return sf
			}
		}
		return nil
	}
	missing := false
	st := &h.ShortTerm
	for i := range st.NumNegative {
		sf := find(poc+st.DeltaPocS0[i], false)
		switch {
		case sf == nil && st.UsedS0[i]:
			missing = true
		case sf == nil:
		case st.UsedS0[i]:
			rs.stBefore = append(rs.stBefore, sf)
		default:
			rs.foll = append(rs.foll, sf)
		}
		if sf != nil {
			keep[sf], sf.longTerm = true, false
		}
	}
	for i := range st.NumPositive {
		sf := find(poc+st.DeltaPocS1[i], false)
		switch {
		case sf == nil && st.UsedS1[i]:
			missing = true
		case sf == nil:
		case st.UsedS1[i]:
			rs.stAfter = append(rs.stAfter, sf)
		default:
			rs.foll = append(rs.foll, sf)
		}
		if sf != nil {
			keep[sf], sf.longTerm = true, false
		}
	}
	maxLsb := int32(1) << s.Log2MaxPocLsb
	for i := range h.NumLongTerm {
		want, lsbOnly := h.LtPoc[i], true
		if h.LtMSBPresent[i] {
			want = poc - h.LtMSBCycle[i]*maxLsb - (int32(h.PocLsb) - h.LtPoc[i])
			lsbOnly = false
		}
		sf := find(want, lsbOnly)
		switch {
		case sf == nil && h.LtUsedByCurr[i]:
			missing = true
		case sf == nil:
		case h.LtUsedByCurr[i]:
			rs.ltCurr = append(rs.ltCurr, sf)
		default:
			rs.foll = append(rs.foll, sf)
		}
		if sf != nil {
			keep[sf], sf.longTerm = true, true
		}
	}
	if missing {
		var near *surface
		for _, sf := range v.surfaces {
			if sf.dpb && (near == nil || sf.poc > near.poc) {
				near = sf
			}
		}
		if near == nil {
			return rs, errors.New("vaapi: the picture references pictures never decoded")
		}
		keep[near] = true
		if len(rs.stBefore)+len(rs.stAfter)+len(rs.ltCurr) == 0 {
			rs.stBefore = append(rs.stBefore, near)
		}
	}
	for _, sf := range v.surfaces {
		if sf.dpb && !keep[sf] {
			sf.dpb = false
		}
	}
	return rs, nil
}

func (v *HEVCDecoder) freeSurface() *surface {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, sf := range v.surfaces {
		if !sf.dpb && sf.out == 0 {
			return sf
		}
	}
	return nil
}

func flag(b bool, bit uint) uint32 {
	if b {
		return 1 << bit
	}
	return 0
}

func (v *HEVCDecoder) fillPicture(s *hevc.SPS, p *hevc.PPS, h *hevc.SliceHeader, cur *surface, poc int32, rs refSet) {
	pp := &v.pic
	*pp = picParamHEVC{}
	pp.CurrPic = pictureHEVC{ID: cur.id, POC: poc}
	i := 0
	add := func(list []*surface, flags uint32) {
		for _, sf := range list {
			if i >= maxReferenceList {
				return
			}
			f := flags
			if sf.longTerm {
				f |= picLongTerm
			}
			pp.Refs[i] = pictureHEVC{ID: sf.id, POC: sf.poc, Flags: f}
			i++
		}
	}
	add(rs.stBefore, picStCurrBefore)
	add(rs.stAfter, picStCurrAfter)
	add(rs.ltCurr, picLtCurr)
	add(rs.foll, 0)
	for ; i < maxReferenceList; i++ {
		pp.Refs[i] = pictureHEVC{ID: vaInvalidID, Flags: picInvalid}
	}
	pp.Width, pp.Height = uint16(s.Width), uint16(s.Height)
	pp.PicFields = s.ChromaFormatIdc | flag(s.SeparateColourPlane, 2) | flag(s.PCM, 3) | flag(s.ScalingListEnabled, 4) |
		flag(p.TransformSkip, 5) | flag(s.AMP, 6) | flag(s.StrongIntraSmoothing, 7) | flag(p.SignDataHiding, 8) |
		flag(p.ConstrainedIntraPred, 9) | flag(p.CuQPDeltaEnabled, 10) | flag(p.WeightedPred, 11) | flag(p.WeightedBipred, 12) |
		flag(p.TransquantBypass, 13) | flag(p.Tiles, 14) | flag(p.EntropyCodingSync, 15) | flag(p.LoopFilterAcrossSlices, 16) |
		flag(p.LoopFilterAcrossTiles, 17) | flag(s.PCMLoopFilterDisabled, 18) | flag(s.MaxNumReorder == 0, 19)
	pp.MaxDecPicBufferingMinus1 = uint8(s.MaxDecPicBufferingMinus1)
	pp.BitDepthLumaMinus8, pp.BitDepthChromaMinus8 = uint8(s.BitDepthLuma), uint8(s.BitDepthChroma)
	pp.PCMBitDepthLumaMinus1, pp.PCMBitDepthChromaMinus1 = uint8(s.PCMBitDepthLuma), uint8(s.PCMBitDepthChroma)
	pp.Log2MinCbMinus3, pp.Log2DiffMaxMinCb = uint8(s.Log2MinCbMinus3), uint8(s.Log2DiffMaxMinCb)
	pp.Log2MinTbMinus2, pp.Log2DiffMaxMinTb = uint8(s.Log2MinTbMinus2), uint8(s.Log2DiffMaxMinTb)
	pp.Log2MinPCMMinus3, pp.Log2DiffMaxMinPCM = uint8(s.Log2MinPCMMinus3), uint8(s.Log2DiffMaxMinPCM)
	pp.MaxTHDepthIntra, pp.MaxTHDepthInter = uint8(s.MaxTHDepthIntra), uint8(s.MaxTHDepthInter)
	pp.InitQPMinus26 = int8(p.InitQPMinus26)
	pp.DiffCuQPDeltaDepth = uint8(p.DiffCuQPDeltaDepth)
	pp.CbQPOffset, pp.CrQPOffset = int8(p.CbQPOffset), int8(p.CrQPOffset)
	pp.Log2ParallelMergeLevelMinus2 = uint8(p.Log2ParallelMergeLevelMinus2)
	if p.Tiles {
		pp.NumTileColumnsMinus1, pp.NumTileRowsMinus1 = uint8(p.NumTileColumnsMinus1), uint8(p.NumTileRowsMinus1)
		cols, rows := p.TileSizes(s)
		for k, c := range cols {
			if k < len(pp.ColumnWidthMinus1) {
				pp.ColumnWidthMinus1[k] = uint16(c)
			}
		}
		for k, r := range rows {
			if k < len(pp.RowHeightMinus1) {
				pp.RowHeightMinus1[k] = uint16(r)
			}
		}
	}
	pp.SliceParsingFields = flag(p.ListsModificationPresent, 0) | flag(s.LongTermRefsPresent, 1) | flag(s.TemporalMVP, 2) |
		flag(p.CabacInitPresent, 3) | flag(p.OutputFlagPresent, 4) | flag(p.DependentSliceSegments, 5) |
		flag(p.SliceChromaQPOffsetsPresent, 6) | flag(s.SAO, 7) | flag(p.DeblockingOverrideEnabled, 8) |
		flag(p.DeblockingDisabled, 9) | flag(p.SliceSegmentHeaderExtensionPresent, 10) |
		flag(hevc.IsIRAP(h.NalType), 11) | flag(hevc.IsIDR(h.NalType), 12)
	pp.Log2MaxPocLsbMinus4 = uint8(s.Log2MaxPocLsb - 4)
	pp.NumShortTermRPS = uint8(len(s.ShortTermRPS))
	pp.NumLongTermRefPicSPS = uint8(len(s.LtRefPicPocLsb))
	pp.NumRefIdxL0DefaultMinus1, pp.NumRefIdxL1DefaultMinus1 = uint8(p.NumRefIdxL0DefaultMinus1), uint8(p.NumRefIdxL1DefaultMinus1)
	pp.BetaOffsetDiv2, pp.TcOffsetDiv2 = int8(p.BetaOffsetDiv2), int8(p.TcOffsetDiv2)
	pp.NumExtraSliceHeaderBits = uint8(p.NumExtraSliceHeaderBits)
	if !h.ShortTermInSPS {
		pp.STRPSBits = uint32(h.ShortTermBits)
	}
}

// index is sf's place in the picture's reference frames.
func (v *HEVCDecoder) index(sf *surface) uint8 {
	for i := range v.pic.Refs {
		if v.pic.Refs[i].ID == sf.id && v.pic.Refs[i].Flags&picInvalid == 0 {
			return uint8(i)
		}
	}
	return 0xff
}

// render sends the picture's parameters and slices and waits for the
// picture.
func (v *HEVCDecoder) render(s *hevc.SPS, p *hevc.PPS, slices []sliceNAL, cur *surface, rs refSet) error {
	allIntra := true
	for _, sl := range slices {
		allIntra = allIntra && sl.h.Type == hevc.SliceI
	}
	v.pic.SliceParsingFields = v.pic.SliceParsingFields&^(1<<13) | flag(allIntra, 13)
	v.buffers = v.buffers[:0]
	defer func() {
		for _, b := range v.buffers {
			call(lib.destroyBuffer, v.d.dpy, uintptr(b))
		}
	}()
	b, err := v.d.buffer(v.context, vaPictureParameterBufferType, unsafe.Pointer(&v.pic), int(unsafe.Sizeof(v.pic)))
	if err != nil {
		return err
	}
	v.buffers = append(v.buffers, b)
	if s.ScalingListEnabled {
		l := &s.ScalingList
		if p.ScalingListPresent {
			l = &p.ScalingList
		}
		v.iq = iqMatrixHEVC{List4: l.List4, List8: l.List8, List16: l.List16, DC16: l.DC16}
		v.iq.List32[0], v.iq.List32[1] = l.List32[0], l.List32[3]
		v.iq.DC32[0], v.iq.DC32[1] = l.DC32[0], l.DC32[3]
		b, err := v.d.buffer(v.context, vaIQMatrixBufferType, unsafe.Pointer(&v.iq), int(unsafe.Sizeof(v.iq)))
		if err != nil {
			return err
		}
		v.buffers = append(v.buffers, b)
	}
	if err := status("vaBeginPicture", call(lib.beginPicture, v.d.dpy, uintptr(v.context), uintptr(cur.id))); err != nil {
		return err
	}
	if err := status("vaRenderPicture", call(lib.renderPicture, v.d.dpy, uintptr(v.context), uintptr(unsafe.Pointer(&v.buffers[0])), uintptr(len(v.buffers)))); err != nil {
		call(lib.endPicture, v.d.dpy, uintptr(v.context))
		return err
	}
	// The lists every slice builds from (8.3.4): before, after, long term.
	var list0, list1 []uint8
	for _, sf := range rs.stBefore {
		list0 = append(list0, v.index(sf))
	}
	for _, sf := range rs.stAfter {
		list0 = append(list0, v.index(sf))
	}
	for _, sf := range rs.ltCurr {
		list0 = append(list0, v.index(sf))
	}
	for _, sf := range rs.stAfter {
		list1 = append(list1, v.index(sf))
	}
	for _, sf := range rs.stBefore {
		list1 = append(list1, v.index(sf))
	}
	for _, sf := range rs.ltCurr {
		list1 = append(list1, v.index(sf))
	}
	for i, sl := range slices {
		v.fillSlice(sl, i == len(slices)-1, list0, list1)
		ids := [2]uint32{}
		var err error
		if ids[0], err = v.d.buffer(v.context, vaSliceParameterBufferType, unsafe.Pointer(&v.slice), int(unsafe.Sizeof(v.slice))); err == nil {
			v.buffers = append(v.buffers, ids[0])
			ids[1], err = v.d.buffer(v.context, vaSliceDataBufferType, unsafe.Pointer(&sl.nal[0]), len(sl.nal))
			if err == nil {
				v.buffers = append(v.buffers, ids[1])
				err = status("vaRenderPicture", call(lib.renderPicture, v.d.dpy, uintptr(v.context), uintptr(unsafe.Pointer(&ids[0])), 2))
			}
		}
		if err != nil {
			call(lib.endPicture, v.d.dpy, uintptr(v.context))
			return err
		}
	}
	if err := status("vaEndPicture", call(lib.endPicture, v.d.dpy, uintptr(v.context))); err != nil {
		return err
	}
	return status("vaSyncSurface", call(lib.syncSurface, v.d.dpy, uintptr(cur.id)))
}

func (v *HEVCDecoder) fillSlice(sl sliceNAL, last bool, list0, list1 []uint8) {
	h := sl.h
	sp := &v.slice
	*sp = sliceParamHEVC{}
	sp.DataSize = uint32(len(sl.nal))
	sp.DataByteOffset = uint32(h.DataOffset)
	sp.SegmentAddress = h.SegmentAddress
	for l := range 2 {
		for i := range sp.RefPicList[l] {
			sp.RefPicList[l][i] = 0xff
		}
	}
	build := func(out *[15]uint8, n int, base []uint8, mod bool, entry *[16]uint32) {
		if len(base) == 0 {
			return
		}
		// RefPicListTemp repeats the lists to num_ref_idx_active entries.
		total := max(n, len(base))
		temp := make([]uint8, 0, total)
		for len(temp) < total {
			temp = append(temp, base...)
		}
		for i := 0; i < n && i < 15; i++ {
			if mod && int(entry[i]) < len(temp) {
				out[i] = temp[entry[i]]
			} else {
				out[i] = temp[i]
			}
		}
	}
	if h.Type != hevc.SliceI {
		build(&sp.RefPicList[0], h.NumRefIdxL0, list0, h.ListModL0, &h.ListEntryL0)
	}
	if h.Type == hevc.SliceB {
		build(&sp.RefPicList[1], h.NumRefIdxL1, list1, h.ListModL1, &h.ListEntryL1)
	}
	sp.LongSliceFlags = flag(last, 0) | flag(h.Dependent, 1) | uint32(h.Type)<<2 | h.ColourPlaneID<<4 |
		flag(h.SAOLuma, 6) | flag(h.SAOChroma, 7) | flag(h.MvdL1Zero, 8) | flag(h.CabacInit, 9) |
		flag(h.TemporalMVP, 10) | flag(h.DeblockingDisabled, 11) | flag(h.CollocFromL0, 12) | flag(h.LoopFilterAcrossSlices, 13)
	sp.CollocatedRefIdx = uint8(h.CollocRefIdx)
	if h.NumRefIdxL0 > 0 {
		sp.NumRefIdxL0ActiveMinus1 = uint8(h.NumRefIdxL0 - 1)
	}
	if h.NumRefIdxL1 > 0 {
		sp.NumRefIdxL1ActiveMinus1 = uint8(h.NumRefIdxL1 - 1)
	}
	sp.QPDelta, sp.CbQPOffset, sp.CrQPOffset = int8(h.QPDelta), int8(h.CbQPOffset), int8(h.CrQPOffset)
	sp.BetaOffsetDiv2, sp.TcOffsetDiv2 = int8(h.BetaOffsetDiv2), int8(h.TcOffsetDiv2)
	sp.LumaLog2WeightDenom, sp.DeltaChromaLog2WeightDenom = uint8(h.LumaLog2WeightDenom), int8(h.ChromaLog2WeightDenom)
	for i := range 15 {
		sp.DeltaLumaWeightL0[i], sp.LumaOffsetL0[i] = int8(h.LumaWeightL0[i]), int8(h.LumaOffsetL0[i])
		sp.DeltaLumaWeightL1[i], sp.LumaOffsetL1[i] = int8(h.LumaWeightL1[i]), int8(h.LumaOffsetL1[i])
		for j := range 2 {
			sp.DeltaChromaWeightL0[i][j], sp.ChromaOffsetL0[i][j] = int8(h.ChromaWeightL0[i][j]), int8(h.ChromaOffsetL0[i][j])
			sp.DeltaChromaWeightL1[i][j], sp.ChromaOffsetL1[i][j] = int8(h.ChromaWeightL1[i][j]), int8(h.ChromaOffsetL1[i][j])
		}
	}
	sp.FiveMinusMaxNumMergeCand = uint8(h.FiveMinusMaxMergeCand)
	sp.NumEntryPointOffsets = uint16(h.NumEntryPointOffsets)
	sp.DataNumEmuPrevnBytes = uint16(h.EPBInHeader)
}

// output gives cur out: exported, where the surface takes DMA-BUFs, else
// copied into memory.
func (v *HEVCDecoder) output(cur *surface, s *hevc.SPS, pts int64) (decode.Frame, error) {
	vw, vh := s.VisibleSize()
	format, fourcc := capture.FormatNV12, capture.FourccNV12
	if v.tenBit {
		format, fourcc = capture.FormatP010, capture.FourccP010
	}
	if !v.copy {
		if err := v.export(cur); err != nil || !v.accepts(cur.dmabuf.Fourcc, cur.dmabuf.Modifier) {
			v.copy = true
		}
	}
	if !v.copy {
		cur.dmabuf.Width, cur.dmabuf.Height = vw, vh
		v.mu.Lock()
		cur.out++
		v.mu.Unlock()
		return decode.Frame{Surface: cur.dmabuf.Handle(), Format: format, Width: vw, Height: vh, PTS: pts, Ref: uintptr(cur.id) + 1}, nil
	}
	if !v.pool.Matches(fourcc, vw, vh) {
		v.pool = hostbuf.New(fourcc, vw, vh, 8)
		v.pools = append(v.pools, v.pool)
	}
	b, err := v.pool.Get()
	if err != nil {
		return decode.Frame{}, err
	}
	if err := v.readBack(cur, b, vw, vh); err != nil {
		v.pool.Put(b)
		return decode.Frame{}, err
	}
	return decode.Frame{Surface: b.DMABuf.Handle(), Format: format, Width: vw, Height: vh, PTS: pts, Ref: hostRef | uintptr(b.Index())}, nil
}

// hostRef marks a Frame.Ref of a buffer in memory; others are a surface
// ID plus 1.
const hostRef = 1 << 40

// export makes sf's DMA-BUF descriptor, once.
func (v *HEVCDecoder) export(sf *surface) error {
	if sf.exported {
		return nil
	}
	v.desc = drmPrimeDescriptor{}
	if err := status("vaExportSurfaceHandle", call(lib.exportSurface, v.d.dpy, uintptr(sf.id), vaMemTypeDRMPrime2, vaExportReadOnly|vaExportComposedLayers, uintptr(unsafe.Pointer(&v.desc)))); err != nil {
		return err
	}
	d := &v.desc
	if d.numLayers != 1 || d.layers[0].numPlanes > 4 {
		for i := range d.numObjects {
			closeFD(d.objects[i].fd)
		}
		return errors.New("vaapi: export gave more than one layer")
	}
	l := &d.layers[0]
	out := &sf.dmabuf
	*out = capture.DMABuf{Fourcc: l.drmFormat, Modifier: d.objects[0].modifier, Planes: int(l.numPlanes), Serial: serials.Add(1)}
	for i := range int(l.numPlanes) {
		out.FDs[i] = d.objects[l.objectIndex[i]].fd
		out.Offsets[i], out.Pitches[i] = l.offset[i], l.pitch[i]
	}
	sf.exported = true
	return nil
}

// readBack copies sf's visible picture into b, through vaDeriveImage
// where the driver maps surfaces, else vaGetImage.
func (v *HEVCDecoder) readBack(sf *surface, b *hostbuf.Buffer, w, h int) error {
	img := vaImage{id: vaInvalidID}
	derived := call(lib.deriveImage, v.d.dpy, uintptr(sf.id), uintptr(unsafe.Pointer(&img))) == vaStatusSuccess
	if !derived {
		if !v.imaged {
			fourcc := uint32(vaFourccNV12)
			bits := uint32(12)
			if v.tenBit {
				fourcc, bits = vaFourccP010, 24
			}
			format := [12]uint32{fourcc, 1, bits}
			if err := status("vaCreateImage", call(lib.createImage, v.d.dpy, uintptr(unsafe.Pointer(&format[0])), uintptr(v.width), uintptr(v.height), uintptr(unsafe.Pointer(&v.image)))); err != nil {
				return err
			}
			v.imaged = true
		}
		if err := status("vaGetImage", call(lib.getImage, v.d.dpy, uintptr(sf.id), 0, 0, uintptr(v.width), uintptr(v.height), uintptr(v.image.id))); err != nil {
			return err
		}
		img = v.image
	} else {
		defer call(lib.destroyImage, v.d.dpy, uintptr(img.id))
	}
	var mapped uintptr
	if err := status("vaMapBuffer", call(lib.mapBuffer, v.d.dpy, uintptr(img.buf), uintptr(unsafe.Pointer(&mapped)))); err != nil {
		return err
	}
	defer call(lib.unmapBuffer, v.d.dpy, uintptr(img.buf))
	src := unsafe.Slice((*byte)(ptr(mapped)), img.dataSize)
	bytes := 1
	if v.tenBit {
		bytes = 2
	}
	rowY, rowC := w*bytes, (w+1)/2*2*bytes
	for r := range h {
		o := int(img.offsets[0]) + r*int(img.pitches[0])
		copy(b.Luma[r*b.Pitch:r*b.Pitch+rowY], src[o:o+rowY])
	}
	for r := range (h + 1) / 2 {
		o := int(img.offsets[1]) + r*int(img.pitches[1])
		copy(b.Chroma[r*b.Pitch:r*b.Pitch+rowC], src[o:o+rowC])
	}
	return nil
}

// Release gives a picture back.
func (v *HEVCDecoder) Release(f decode.Frame) {
	if f.Ref&hostRef != 0 {
		for _, p := range v.pools {
			if b := p.At(int(f.Ref &^ hostRef)); b != nil && b.DMABuf.Handle() == f.Surface {
				p.Put(b)
				return
			}
		}
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, sf := range v.surfaces {
		if uintptr(sf.id)+1 == f.Ref && sf.dmabuf.Handle() == f.Surface && sf.out > 0 {
			sf.out--
			return
		}
	}
}

// Close frees the decoder; every picture must be released.
func (v *HEVCDecoder) Close() error {
	v.destroyContext()
	for _, p := range v.pools {
		p.Close()
	}
	v.pools, v.pool = nil, nil
	v.d.close()
	return nil
}

var _ decode.Decoder = (*HEVCDecoder)(nil)

func closeFD(fd int32) {
	if fd > 0 {
		syscall.Close(int(fd))
	}
}

// closeFDs closes an exported descriptor's objects, each once.
func closeFDs(d *capture.DMABuf) {
	seen := map[int32]bool{}
	for i := range d.Planes {
		if fd := d.FDs[i]; fd > 0 && !seen[fd] {
			seen[fd] = true
			closeFD(fd)
		}
	}
	*d = capture.DMABuf{}
}

// HEVCProfiles reports whether a render node decodes HEVC Main, and Main
// 10.
func HEVCProfiles() (main, main10 bool) {
	d, err := openDisplay(gpu.VendorUnknown)
	if err != nil {
		return false, false
	}
	defer d.close()
	p := d.profiles()
	return p[vaProfileHEVCMain], p[vaProfileHEVCMain10]
}
