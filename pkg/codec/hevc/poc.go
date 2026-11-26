package hevc

// POC derives picture order counts (8.3.1) across a stream.
type POC struct {
	prevLsb, prevMsb int32
	seen             bool
}

// Next is the POC of the picture whose first slice segment is h. noRasl
// is NoRaslOutputFlag: an IDR or BLA picture, or a CRA one that begins
// the stream or follows an end of sequence.
func (p *POC) Next(h *SliceHeader, s *SPS, noRasl bool) int32 {
	max := int32(1) << s.Log2MaxPocLsb
	lsb := int32(h.PocLsb)
	var msb int32
	switch {
	case IsIRAP(h.NalType) && noRasl:
		msb = 0
	case lsb < p.prevLsb && p.prevLsb-lsb >= max/2:
		msb = p.prevMsb + max
	case lsb > p.prevLsb && lsb-p.prevLsb > max/2:
		msb = p.prevMsb - max
	default:
		msb = p.prevMsb
	}
	if h.NalType >= NalBLAWLP && h.NalType <= NalBLAWLP+2 {
		msb = 0 // BLA pictures
	}
	poc := msb + lsb
	// prevTid0Pic: TemporalId 0, not RASL, RADL or a sub-layer non-reference picture.
	if h.TemporalID == 0 && !(h.NalType >= 6 && h.NalType <= 9) && !subLayerNonRef(h.NalType) {
		p.prevLsb, p.prevMsb = lsb, msb
	}
	p.seen = true
	return poc
}

// Seen reports whether a picture came since the stream began.
func (p *POC) Seen() bool { return p.seen }

func subLayerNonRef(t int) bool { return t <= 14 && t%2 == 0 }
