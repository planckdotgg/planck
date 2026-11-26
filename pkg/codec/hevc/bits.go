// Package hevc parses the HEVC syntax a hardware decoder that takes
// parsed parameters (VA-API) needs from an Annex B stream: NAL units, the
// sequence and picture parameter sets, slice segment headers, reference
// picture sets and picture order counts (ITU-T H.265, 7.3 and 8.3).
//
// It covers the Main and Main 10 profiles: 4:2:0, no range or screen
// content extensions.
package hevc

import "errors"

// ErrSyntax is the error of a parameter set or slice header that ends
// early or holds a value out of range.
var ErrSyntax = errors.New("hevc: malformed syntax")

// rbsp removes the emulation prevention bytes of a NAL unit's payload.
// epb lists the raw offset of each removed byte, for mapping a position
// in the RBSP back to the NAL unit.
func rbsp(nal []byte) (out []byte, epb []int) {
	out = make([]byte, 0, len(nal))
	zeros := 0
	for i, b := range nal {
		if zeros >= 2 && b == 3 {
			epb = append(epb, i)
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, b)
	}
	return out, epb
}

// reader reads bits, most significant first. Reading past the end sets
// err and returns zeros.
type reader struct {
	b   []byte
	pos int // in bits
	err error
}

func (r *reader) u(n int) uint32 {
	var v uint32
	for range n {
		if r.pos>>3 >= len(r.b) {
			r.err = ErrSyntax
			return 0
		}
		v = v<<1 | uint32(r.b[r.pos>>3]>>(7-r.pos&7))&1
		r.pos++
	}
	return v
}

func (r *reader) flag() bool { return r.u(1) == 1 }

func (r *reader) skip(n int) {
	r.pos += n
	if r.pos > len(r.b)*8 {
		r.err = ErrSyntax
	}
}

// ue reads an Exp-Golomb code.
func (r *reader) ue() uint32 {
	zeros := 0
	for !r.flag() {
		if r.err != nil || zeros > 31 {
			r.err = ErrSyntax
			return 0
		}
		zeros++
	}
	if zeros == 0 {
		return 0
	}
	return (1<<zeros - 1) + r.u(zeros)
}

func (r *reader) se() int32 {
	v := r.ue()
	if v&1 == 1 {
		return int32((v + 1) / 2)
	}
	return -int32(v / 2)
}

// ueMax reads an Exp-Golomb code no larger than most.
func (r *reader) ueMax(most uint32) uint32 {
	v := r.ue()
	if v > most {
		r.err = ErrSyntax
		return 0
	}
	return v
}

// ceilLog2 is the bits u(v) takes for values below n.
func ceilLog2(n int) int {
	b := 0
	for 1<<b < n {
		b++
	}
	return b
}
