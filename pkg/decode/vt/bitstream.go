// Package vt decodes HEVC and AV1 with Apple VideoToolbox through purego,
// with no cgo. Pictures stay in IOSurface-backed CVPixelBuffers.
//
// The bitstream helpers in this file are portable: VideoToolbox needs the
// stream's parameters as a format description before the first picture.
package vt

import (
	"encoding/binary"
	"errors"
)

// HEVC NAL unit types of the parameter sets.
const (
	nalVPS = 32
	nalSPS = 33
	nalPPS = 34
)

// splitAnnexB returns the NAL units of an Annex B access unit, without
// their start codes.
func splitAnnexB(au []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(au); i++ {
		if au[i] != 0 || au[i+1] != 0 || au[i+2] != 1 {
			continue
		}
		if start >= 0 {
			end := i
			for end > start && au[end-1] == 0 {
				end--
			}
			nals = append(nals, au[start:end])
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(au) {
		nals = append(nals, au[start:])
	}
	return nals
}

func hevcType(nal []byte) int { return int(nal[0]>>1) & 0x3f }

// hevcParams holds an access unit's parameter sets in stream order. A
// stream can carry several of each (VideoToolbox's Main 10 output has four
// PPSs), and slices may refer to any of them.
type hevcParams struct {
	sets [][]byte
	sps  []byte // the first SPS
	has  [3]bool
}

func (p *hevcParams) add(nal []byte) {
	p.sets = append(p.sets, nal)
	t := hevcType(nal)
	p.has[t-nalVPS] = true
	if t == nalSPS && p.sps == nil {
		p.sps = nal
	}
}

func (p hevcParams) complete() bool { return p.has[0] && p.has[1] && p.has[2] }

func (p hevcParams) equal(q hevcParams) bool {
	if len(p.sets) != len(q.sets) {
		return false
	}
	for i := range p.sets {
		if string(p.sets[i]) != string(q.sets[i]) {
			return false
		}
	}
	return true
}

func (p hevcParams) clone() hevcParams {
	var c hevcParams
	for _, s := range p.sets {
		c.add(append([]byte(nil), s...))
	}
	return c
}

// tenBit reads general_profile_idc from the SPS: Main 10 (2) carries 10-bit
// pictures.
func (p hevcParams) tenBit() bool { return len(p.sps) > 3 && p.sps[3]&0x1f == 2 }

// hevcSample splits an Annex B access unit into its parameter sets and the
// rest of its NAL units as one 4-byte length-prefixed sample (hvcC style).
func hevcSample(au []byte, dst []byte) (hevcParams, []byte, error) {
	var p hevcParams
	dst = dst[:0]
	for _, nal := range splitAnnexB(au) {
		if len(nal) < 2 {
			continue
		}
		switch hevcType(nal) {
		case nalVPS, nalSPS, nalPPS:
			p.add(nal)
		default:
			dst = binary.BigEndian.AppendUint32(dst, uint32(len(nal)))
			dst = append(dst, nal...)
		}
	}
	if len(dst) == 0 {
		return p, nil, errors.New("vt: access unit has no picture data")
	}
	return p, dst, nil
}

// AV1 OBU types.
const (
	obuSequenceHeader    = 1
	obuTemporalDelimiter = 2
)

func leb128(b []byte) (v uint64, n int, ok bool) {
	for i := 0; i < len(b) && i < 8; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

// obu is one OBU of a temporal unit: the whole OBU and its payload.
type obu struct {
	typ          int
	raw, payload []byte
}

func splitOBUs(tu []byte) ([]obu, error) {
	var out []obu
	for len(tu) > 0 {
		h := tu[0]
		typ := int(h>>3) & 0xf
		hdr := 1
		if h&0x4 != 0 {
			hdr++
		}
		if h&0x2 == 0 {
			// No size field: the OBU runs to the end.
			if hdr > len(tu) {
				return nil, errors.New("vt: truncated OBU header")
			}
			out = append(out, obu{typ, tu, tu[hdr:]})
			break
		}
		size, n, ok := leb128(tu[min(hdr, len(tu)):])
		end := hdr + n + int(size)
		if !ok || end > len(tu) {
			return nil, errors.New("vt: truncated OBU")
		}
		out = append(out, obu{typ, tu[:end], tu[hdr+n : end]})
		tu = tu[end:]
	}
	return out, nil
}

type bits struct {
	b   []byte
	pos int
	err bool
}

func (r *bits) f(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b)*8 {
			r.err = true
			return 0
		}
		v = v<<1 | uint32(r.b[r.pos/8]>>(7-r.pos%8))&1
		r.pos++
	}
	return v
}

func (r *bits) uvlc() {
	zeros := 0
	for r.f(1) == 0 && !r.err {
		zeros++
	}
	if zeros < 32 {
		r.f(zeros)
	}
}

// av1Sequence is what the av1C box and the decoder need from a sequence
// header.
type av1Sequence struct {
	profile, level, tier       uint32
	highBitDepth, twelveBit    bool
	mono                       bool
	subX, subY, chromaPosition uint32
	width, height              int
}

// parseAV1Sequence reads a sequence header OBU payload (AV1 spec 5.5).
func parseAV1Sequence(p []byte) (av1Sequence, error) {
	r := &bits{b: p}
	var s av1Sequence
	s.profile = r.f(3)
	r.f(1) // still_picture
	reduced := r.f(1) == 1
	decoderModel := false
	var delayLen int
	if reduced {
		s.level = r.f(5)
	} else {
		if r.f(1) == 1 { // timing_info_present_flag
			r.f(32)
			r.f(32)
			if r.f(1) == 1 {
				r.uvlc()
			}
			if r.f(1) == 1 { // decoder_model_info_present_flag
				decoderModel = true
				delayLen = int(r.f(5)) + 1
				r.f(32)
				r.f(5)
				r.f(5)
			}
		}
		initialDelay := r.f(1) == 1
		ops := int(r.f(5)) + 1
		for i := 0; i < ops; i++ {
			r.f(12)
			level := r.f(5)
			var tier uint32
			if level > 7 {
				tier = r.f(1)
			}
			if i == 0 {
				s.level, s.tier = level, tier
			}
			if decoderModel && r.f(1) == 1 {
				r.f(delayLen)
				r.f(delayLen)
				r.f(1)
			}
			if initialDelay && r.f(1) == 1 {
				r.f(4)
			}
		}
	}
	wBits, hBits := int(r.f(4))+1, int(r.f(4))+1
	s.width, s.height = int(r.f(wBits))+1, int(r.f(hBits))+1
	if !reduced && r.f(1) == 1 { // frame_id_numbers_present_flag
		r.f(4)
		r.f(3)
	}
	r.f(3) // 128x128 superblock, filter intra, intra edge filter
	if !reduced {
		r.f(4) // interintra, masked compound, warped motion, dual filter
		orderHint := r.f(1) == 1
		if orderHint {
			r.f(2) // jnt_comp, ref_frame_mvs
		}
		forceScreen := uint32(2)
		if r.f(1) == 0 { // seq_choose_screen_content_tools
			forceScreen = r.f(1)
		}
		if forceScreen > 0 && r.f(1) == 0 { // seq_choose_integer_mv
			r.f(1)
		}
		if orderHint {
			r.f(3)
		}
	}
	r.f(3) // superres, cdef, restoration
	s.highBitDepth = r.f(1) == 1
	if s.profile == 2 && s.highBitDepth {
		s.twelveBit = r.f(1) == 1
	}
	if s.profile != 1 {
		s.mono = r.f(1) == 1
	}
	cp, tc, mc := uint32(2), uint32(2), uint32(2)
	if r.f(1) == 1 {
		cp, tc, mc = r.f(8), r.f(8), r.f(8)
	}
	switch {
	case s.mono:
		r.f(1)
		s.subX, s.subY = 1, 1
	case cp == 1 && tc == 13 && mc == 0:
	default:
		r.f(1) // color_range
		switch s.profile {
		case 0:
			s.subX, s.subY = 1, 1
		case 2:
			s.subX = 1
			if s.twelveBit {
				if s.subX = r.f(1); s.subX == 1 {
					s.subY = r.f(1)
				}
			}
		}
		if s.subX == 1 && s.subY == 1 {
			s.chromaPosition = r.f(2)
		}
	}
	if r.err {
		return s, errors.New("vt: truncated AV1 sequence header")
	}
	return s, nil
}

// av1C builds the AV1CodecConfigurationRecord of a sequence header OBU.
func av1C(s av1Sequence, seqOBU []byte) []byte {
	b := func(v bool) byte {
		if v {
			return 1
		}
		return 0
	}
	c := []byte{
		0x81,
		byte(s.profile<<5 | s.level),
		byte(s.tier<<7) | b(s.highBitDepth)<<6 | b(s.twelveBit)<<5 | b(s.mono)<<4 | byte(s.subX<<3|s.subY<<2|s.chromaPosition),
		0,
	}
	return append(c, seqOBU...)
}

// av1Sample returns the temporal unit's sequence header OBU, if any, and
// the temporal unit without temporal delimiters, as VideoToolbox takes it.
func av1Sample(tu []byte, dst []byte) (seq obu, sample []byte, err error) {
	obus, err := splitOBUs(tu)
	if err != nil {
		return obu{}, nil, err
	}
	dst = dst[:0]
	for _, o := range obus {
		switch o.typ {
		case obuTemporalDelimiter:
			continue
		case obuSequenceHeader:
			seq = o
		}
		dst = append(dst, o.raw...)
	}
	if len(dst) == 0 {
		return seq, nil, errors.New("vt: temporal unit has no OBUs")
	}
	return seq, dst, nil
}
