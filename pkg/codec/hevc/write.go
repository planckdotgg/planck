package hevc

// Stream is what an encoder fixes for a whole stream: what its VPS, SPS
// and PPS say, and what each slice header takes from them. It describes
// a low-delay stream of one temporal layer: IDR pictures and pictures
// predicted from the one before, 4:2:0, no scaling lists, PCM, tiles,
// long-term references or weighted prediction.
type Stream struct {
	ProfileIdc uint32 // 1 Main, 2 Main 10
	LevelIdc   uint32 // 30 times the level
	// Width and Height are the coded size, a multiple of the minimum
	// coding block; the conformance window crops it to VisibleWidth and
	// VisibleHeight.
	Width, Height               uint32
	VisibleWidth, VisibleHeight uint32
	BitDepthMinus8              uint32
	Log2MaxPocLsb               uint32 // 4 to 16

	Log2MinCbMinus3, Log2DiffMaxMinCb uint32
	Log2MinTbMinus2, Log2DiffMaxMinTb uint32
	MaxTHDepthInter, MaxTHDepthIntra  uint32
	AMP, SAO, TemporalMVP             bool
	StrongIntraSmoothing              bool

	InitQPMinus26            int32
	SignDataHiding           bool
	ConstrainedIntraPred     bool
	TransformSkip            bool
	CuQPDelta                bool
	LoopFilterAcrossSlices   bool
	FiveMinusMaxMergeCand    uint32
	Colour                   *Colour
	MaxDecPicBufferingMinus1 uint32
}

// Colour is the VUI colour description, as ITU-T H.273 code points.
type Colour struct {
	Primaries, Transfer, Matrix uint8
}

// Slice is one slice segment of a picture.
type Slice struct {
	IDR  bool
	Type int // SliceI, SliceP or SliceB
	// Address is the slice's first CTB in raster order; 0 starts the
	// picture.
	Address uint32
	PocLsb  uint32
	QPDelta int32
	// SAOLuma and SAOChroma, when the stream has SAO.
	SAOLuma, SAOChroma bool
	TemporalMVP        bool
}

// writer writes bits, most significant first.
type writer struct {
	b    []byte
	bits int
}

func (w *writer) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		if w.bits%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.b[len(w.b)-1] |= 0x80 >> uint(w.bits%8)
		}
		w.bits++
	}
}

func (w *writer) flag(f bool) {
	if f {
		w.u(1, 1)
	} else {
		w.u(1, 0)
	}
}

func (w *writer) ue(v uint32) {
	x := uint64(v) + 1
	n := 0
	for t := x; t > 1; t >>= 1 {
		n++
	}
	w.u(n, 0)
	w.u(n+1, uint32(x))
}

func (w *writer) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

// trailing writes rbsp_trailing_bits or byte_alignment: a one, then
// zeros to the byte.
func (w *writer) trailing() {
	w.u(1, 1)
	for w.bits%8 != 0 {
		w.u(1, 0)
	}
}

// nal is w's RBSP as a NAL unit of type t, with emulation prevention and
// no start code.
func nal(t int, w *writer) []byte {
	return escape([]byte{byte(t << 1), 1}, w.b)
}

// escape appends rbsp to out with emulation prevention bytes.
func escape(out, rbsp []byte) []byte {
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func (s *Stream) profileTierLevel(w *writer) {
	w.u(2, 0) // general_profile_space
	w.u(1, 0) // general_tier_flag: Main tier
	w.u(5, s.ProfileIdc)
	compat := uint32(1) << (31 - s.ProfileIdc)
	if s.ProfileIdc == 1 {
		compat |= 1 << (31 - 2) // a Main stream is Main 10 too
	}
	w.u(32, compat)
	w.u(1, 1) // general_progressive_source_flag
	w.u(1, 0) // general_interlaced_source_flag
	w.u(1, 0) // general_non_packed_constraint_flag
	w.u(1, 1) // general_frame_only_constraint_flag
	w.u(32, 0)
	w.u(11, 0) // the 43 reserved bits
	w.u(1, 0)  // general_inbld_flag
	w.u(8, s.LevelIdc)
}

// VPS is the video parameter set NAL unit.
func (s *Stream) VPS() []byte {
	w := &writer{}
	w.u(4, 0)       // vps_video_parameter_set_id
	w.u(1, 1)       // vps_base_layer_internal_flag
	w.u(1, 1)       // vps_base_layer_available_flag
	w.u(6, 0)       // vps_max_layers_minus1
	w.u(3, 0)       // vps_max_sub_layers_minus1
	w.u(1, 1)       // vps_temporal_id_nesting_flag
	w.u(16, 0xffff) // vps_reserved_0xffff_16bits
	s.profileTierLevel(w)
	w.u(1, 0) // vps_sub_layer_ordering_info_present_flag
	w.ue(s.MaxDecPicBufferingMinus1)
	w.ue(0)   // vps_max_num_reorder_pics
	w.ue(0)   // vps_max_latency_increase_plus1
	w.u(6, 0) // vps_max_layer_id
	w.ue(0)   // vps_num_layer_sets_minus1
	w.u(1, 0) // vps_timing_info_present_flag
	w.u(1, 0) // vps_extension_flag
	w.trailing()
	return nal(NalVPS, w)
}

// SPS is the sequence parameter set NAL unit.
func (s *Stream) SPS() []byte {
	w := &writer{}
	w.u(4, 0) // sps_video_parameter_set_id
	w.u(3, 0) // sps_max_sub_layers_minus1
	w.u(1, 1) // sps_temporal_id_nesting_flag
	s.profileTierLevel(w)
	w.ue(0) // sps_seq_parameter_set_id
	w.ue(1) // chroma_format_idc: 4:2:0
	w.ue(s.Width)
	w.ue(s.Height)
	right, bottom := (s.Width-s.VisibleWidth)/2, (s.Height-s.VisibleHeight)/2
	w.flag(right != 0 || bottom != 0)
	if right != 0 || bottom != 0 {
		w.ue(0)
		w.ue(right)
		w.ue(0)
		w.ue(bottom)
	}
	w.ue(s.BitDepthMinus8)
	w.ue(s.BitDepthMinus8)
	w.ue(s.Log2MaxPocLsb - 4)
	w.u(1, 1) // sps_sub_layer_ordering_info_present_flag
	w.ue(s.MaxDecPicBufferingMinus1)
	w.ue(0) // sps_max_num_reorder_pics
	w.ue(0) // sps_max_latency_increase_plus1
	w.ue(s.Log2MinCbMinus3)
	w.ue(s.Log2DiffMaxMinCb)
	w.ue(s.Log2MinTbMinus2)
	w.ue(s.Log2DiffMaxMinTb)
	w.ue(s.MaxTHDepthInter)
	w.ue(s.MaxTHDepthIntra)
	w.u(1, 0) // scaling_list_enabled_flag
	w.flag(s.AMP)
	w.flag(s.SAO)
	w.u(1, 0) // pcm_enabled_flag
	w.ue(0)   // num_short_term_ref_pic_sets: each slice carries its own
	w.u(1, 0) // long_term_ref_pics_present_flag
	w.flag(s.TemporalMVP)
	w.flag(s.StrongIntraSmoothing)
	w.flag(s.Colour != nil) // vui_parameters_present_flag
	if c := s.Colour; c != nil {
		w.u(1, 0) // aspect_ratio_info_present_flag
		w.u(1, 0) // overscan_info_present_flag
		w.u(1, 1) // video_signal_type_present_flag
		w.u(3, 5) // video_format: unspecified
		w.u(1, 0) // video_full_range_flag
		w.u(1, 1) // colour_description_present_flag
		w.u(8, uint32(c.Primaries))
		w.u(8, uint32(c.Transfer))
		w.u(8, uint32(c.Matrix))
		w.u(1, 0) // chroma_loc_info_present_flag
		w.u(1, 0) // neutral_chroma_indication_flag
		w.u(1, 0) // field_seq_flag
		w.u(1, 0) // frame_field_info_present_flag
		w.u(1, 0) // default_display_window_flag
		w.u(1, 0) // vui_timing_info_present_flag
		w.u(1, 0) // bitstream_restriction_flag
	}
	w.u(1, 0) // sps_extension_present_flag
	w.trailing()
	return nal(NalSPS, w)
}

// PPS is the picture parameter set NAL unit.
func (s *Stream) PPS() []byte {
	w := &writer{}
	w.ue(0)   // pps_pic_parameter_set_id
	w.ue(0)   // pps_seq_parameter_set_id
	w.u(1, 0) // dependent_slice_segments_enabled_flag
	w.u(1, 0) // output_flag_present_flag
	w.u(3, 0) // num_extra_slice_header_bits
	w.flag(s.SignDataHiding)
	w.u(1, 0) // cabac_init_present_flag
	w.ue(0)   // num_ref_idx_l0_default_active_minus1
	w.ue(0)   // num_ref_idx_l1_default_active_minus1
	w.se(s.InitQPMinus26)
	w.flag(s.ConstrainedIntraPred)
	w.flag(s.TransformSkip)
	w.flag(s.CuQPDelta)
	if s.CuQPDelta {
		w.ue(0) // diff_cu_qp_delta_depth
	}
	w.se(0)   // pps_cb_qp_offset
	w.se(0)   // pps_cr_qp_offset
	w.u(1, 0) // pps_slice_chroma_qp_offsets_present_flag
	w.u(1, 0) // weighted_pred_flag
	w.u(1, 0) // weighted_bipred_flag
	w.u(1, 0) // transquant_bypass_enabled_flag
	w.u(1, 0) // tiles_enabled_flag
	w.u(1, 0) // entropy_coding_sync_enabled_flag
	w.flag(s.LoopFilterAcrossSlices)
	w.u(1, 0) // deblocking_filter_control_present_flag
	w.u(1, 0) // pps_scaling_list_data_present_flag
	w.u(1, 0) // lists_modification_present_flag
	w.ue(0)   // log2_parallel_merge_level_minus2
	w.u(1, 0) // slice_segment_header_extension_present_flag
	w.u(1, 0) // pps_extension_present_flag
	w.trailing()
	return nal(NalPPS, w)
}

// picSizeInCtbs is the picture's CTBs.
func (s *Stream) picSizeInCtbs() int {
	c := uint32(1) << (s.Log2MinCbMinus3 + 3 + s.Log2DiffMaxMinCb)
	return int((s.Width+c-1)/c) * int((s.Height+c-1)/c)
}

// SliceHeader is the slice segment NAL unit of h with no slice data: its
// header up to the byte alignment, as a VA-API packed slice header.
// Pictures after an IDR reference the one before them alone.
func (s *Stream) SliceHeader(h Slice) []byte {
	t := 1 // TRAIL_R
	if h.IDR {
		t = NalIDRWRADL
	}
	w := &writer{}
	w.flag(h.Address == 0) // first_slice_segment_in_pic_flag
	if h.IDR {
		w.u(1, 0) // no_output_of_prior_pics_flag
	}
	w.ue(0) // slice_pic_parameter_set_id
	if h.Address != 0 {
		w.u(ceilLog2(s.picSizeInCtbs()), h.Address)
	}
	w.ue(uint32(h.Type))
	if !h.IDR {
		w.u(int(s.Log2MaxPocLsb), h.PocLsb)
		w.u(1, 0) // short_term_ref_pic_set_sps_flag
		w.ue(1)   // num_negative_pics
		w.ue(0)   // num_positive_pics
		w.ue(0)   // delta_poc_s0_minus1: the picture before
		w.u(1, 1) // used_by_curr_pic_s0_flag
		if s.TemporalMVP {
			w.flag(h.TemporalMVP)
		}
	}
	if s.SAO {
		w.flag(h.SAOLuma)
		w.flag(h.SAOChroma)
	}
	if h.Type == SliceP || h.Type == SliceB {
		w.u(1, 0) // num_ref_idx_active_override_flag
		if h.Type == SliceB {
			w.u(1, 0) // mvd_l1_zero_flag
		}
		if h.TemporalMVP && h.Type == SliceB {
			w.u(1, 1) // collocated_from_l0_flag
		}
		w.ue(s.FiveMinusMaxMergeCand)
	}
	w.se(h.QPDelta)
	if s.LoopFilterAcrossSlices {
		// Deblocking stays on (no override in the PPS).
		w.u(1, 1) // slice_loop_filter_across_slices_enabled_flag
	}
	w.trailing()
	return nal(t, w)
}
