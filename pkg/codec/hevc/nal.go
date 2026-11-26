package hevc

// NALUnits returns the NAL units of an Annex B access unit, without their
// start codes or trailing zero bytes.
func NALUnits(au []byte) [][]byte {
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

// NALType is a NAL unit's nal_unit_type.
func NALType(nal []byte) int { return int(nal[0]>>1) & 0x3f }
